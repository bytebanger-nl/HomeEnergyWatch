package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin/mhi"
)

// A simulated MHI air conditioner (WF-RAC module) for the mhi plugin: the
// beaver API on HTTPS with a self-signed certificate. Reads always work;
// setAirconStat answers result 2 until updateAccountInfo registered the
// sender, checks the command half's CRC, and then takes over the frame's
// receive half as the new state. The indoor temperature is that of the
// simulated room (room.go), which the unit heats while it is on in heating
// mode; it starts switched off. Add it in the PWA as an MHI device with
// address 127.0.0.1:<port>.

// mhiSample is a getAirconStat blob captured from a real unit (heating,
// 21.5 °C, fan quiet, room 23.2 °C, outdoor 2.2 °C).
const mhiSample = "AAezqKv/AAAAAAASigAAAAAAAf/////ejoEEESArmAAAgAIAAgAAAAAAAAOAIJj/gBBi/5QQAADRSA=="

type mhiUnit struct {
	mu         sync.Mutex
	cmdHalf    []byte // command half of the captured frame, sent back as is
	content    []byte // 18 state bytes of the receive half
	trailer    []byte // count byte and temperature segments after them
	accounts   map[string]bool
	lastWriter string
	room       *room
}

func newMHIUnit(r *room) *mhiUnit {
	raw, _ := base64.StdEncoding.DecodeString(mhiSample)
	start := int(int8(raw[18]))*4 + 21
	u := &mhiUnit{
		cmdHalf:  append([]byte(nil), raw[:start]...),
		content:  append([]byte(nil), raw[start:start+18]...),
		trailer:  append([]byte(nil), raw[start+18:len(raw)-2]...),
		accounts: map[string]bool{},
		room:     r,
	}
	u.content[2] &^= 3 // switched off
	u.toRoom()
	return u
}

// The receive half's state bits, as the mhi plugin decodes them.
func (u *mhiUnit) on() bool        { return u.content[2]&3 == 1 }
func (u *mhiUnit) heating() bool   { return u.content[2]&60 == 16 }
func (u *mhiUnit) preset() float64 { return float64(u.content[4]) / 2 }

// toRoom tells the room whether the unit heats, after a change. u.mu must
// be held (or u not yet shared).
func (u *mhiUnit) toRoom() {
	u.room.SetAC(u.on() && u.heating(), u.preset())
}

func (u *mhiUnit) blob() string {
	content := append([]byte(nil), u.content...)
	if _, ac := u.room.Heating(); ac {
		content[9] |= 2 // compressor running
	} else {
		content[9] &^= 2
	}
	trailer := append([]byte(nil), u.trailer...)
	// temperature segments after the count byte: 0x80, sensor, value, 0
	for i := 1; i+3 < len(trailer); i += 4 {
		if trailer[i] != 0x80 {
			continue
		}
		switch trailer[i+1] {
		case 16:
			trailer[i+2] = mhi.OutdoorByte(u.room.Outdoor())
		case 32:
			trailer[i+2] = mhi.IndoorByte(u.room.Temp())
		}
	}
	b := append(append(append([]byte(nil), u.cmdHalf...), content...), trailer...)
	b = append(b, 0, 0) // the receive-half checksum is not read by clients
	return base64.StdEncoding.EncodeToString(b)
}

func mhiCRC(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func (u *mhiUnit) handler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, result int, contents any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "contents": contents})
	}
	mux.HandleFunc("POST /beaver/command/{cmd}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OperatorID string         `json:"operatorId"`
			Contents   map[string]any `json:"contents"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		switch r.PathValue("cmd") {
		case "getDeviceInfo":
			reply(w, 0, map[string]any{"airconId": "e8165617a001", "macAddress": "e8:16:56:17:a0:01", "apMode": 0})
		case "getAirconStat":
			reply(w, 0, map[string]any{"airconId": "e8165617a001", "airconStat": u.blob(), "timezone": "Europe/Amsterdam",
				"numOfAccount": len(u.accounts), "firmType": "WF-RAC-HTTPS"})
		case "updateAccountInfo":
			id, _ := req.Contents["accountId"].(string)
			if id == "" || (!u.accounts[id] && len(u.accounts) >= 4) {
				reply(w, 2, nil)
				return
			}
			u.accounts[id] = true
			log.Printf("MHI      account %q registered", id)
			reply(w, 0, nil)
		case "deleteAccountInfo":
			id, _ := req.Contents["accountId"].(string)
			delete(u.accounts, id)
			reply(w, 0, nil)
		case "setAirconStat":
			if !u.accounts[req.OperatorID] {
				reply(w, 2, nil)
				return
			}
			s, _ := req.Contents["airconStat"].(string)
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil || len(raw) < 50 || raw[18] != 1 {
				reply(w, 12, nil)
				return
			}
			if got := uint16(raw[23]) | uint16(raw[24])<<8; got != mhiCRC(raw[:23]) {
				log.Printf("MHI      setAirconStat with a bad command CRC")
				reply(w, 12, nil)
				return
			}
			copy(u.content, raw[25:43])
			u.lastWriter = req.OperatorID
			u.toRoom()
			power := "uit"
			if u.on() {
				power = "aan"
			}
			log.Printf("MHI      state written by %q: %s, verwarmen %v, %.1f °C", req.OperatorID, power, u.heating(), u.preset())
			reply(w, 0, nil)
		default:
			reply(w, 2, nil)
		}
	})
	return mux
}

// selfSignedCert is a throwaway certificate, like the module's own.
func selfSignedCert() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "WF-RAC"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

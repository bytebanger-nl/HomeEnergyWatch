package main

import (
	"encoding/json"
	"log"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin/tuya"
)

// A simulated Tuya electric radiator for the tuya plugin: the local
// protocol (3.4, with the session handshake) on TCP 6668, a discovery
// broadcast to 127.0.0.1:6666 every second, and the parts of the Tuya
// OpenAPI that a scan uses (token, device list, device model) on the price
// port. Add a Tuya / Smart Life plugin in the PWA with any Access ID and
// secret and region http://127.0.0.1:18080 (the price port), then "Netwerk scannen". The
// device must be on port 6668 because a scan finds an IP, not a port.

const (
	tuyaSimID  = "bfsimradiator0001"
	tuyaSimKey = "hwsim-local-key!" // 16 bytes
)

// tuyaSimModel is the thing model of the simulated radiator, as the cloud's
// /v2.0/cloud/thing/{id}/model returns it (inside the "model" string).
const tuyaSimModel = `{"modelId":"sim","services":[{"code":"","properties":[
 {"abilityId":1,"code":"switch","accessMode":"rw","typeSpec":{"type":"bool"}},
 {"abilityId":2,"code":"temp_set","accessMode":"rw","typeSpec":{"type":"value","min":50,"max":350,"step":5,"scale":1,"unit":"℃"}},
 {"abilityId":3,"code":"temp_current","accessMode":"ro","typeSpec":{"type":"value","min":0,"max":500,"step":1,"scale":1,"unit":"℃"}},
 {"abilityId":4,"code":"mode","accessMode":"rw","typeSpec":{"type":"enum","range":["comfort","eco","anti_frost"]}},
 {"abilityId":7,"code":"child_lock","accessMode":"rw","typeSpec":{"type":"bool"}},
 {"abilityId":19,"code":"temp_correction","accessMode":"rw","typeSpec":{"type":"value","min":-9,"max":9,"step":1,"scale":0,"unit":"℃"}}
]}]}`

func startTuyaRadiator(mux *http.ServeMux, cloudPort int) {
	sim := tuya.NewSimDevice(tuyaSimID, []byte(tuyaSimKey), tuya.V34, map[string]any{
		"1": false, "2": float64(180), "3": float64(192), "4": "eco", "7": false, "19": float64(0),
	})
	sim.OnSet = func(dps map[string]any) { log.Printf("tuya     radiator set %v", dps) }
	ln, err := net.Listen("tcp", net.JoinHostPort(listenHost, "6668"))
	if err != nil {
		log.Printf("tuya     radiator not started: %v", err)
		return
	}
	go sim.Serve(ln)

	// The room warms towards the setpoint while on and cools to 15 °C when off.
	go func() {
		for range time.Tick(10 * time.Second) {
			sim.Update(func(dps map[string]any) {
				cur, _ := dps["3"].(float64)
				target := 150.0
				if on, _ := dps["1"].(bool); on {
					target, _ = dps["2"].(float64)
				}
				dps["3"] = cur + math.Copysign(math.Min(2, math.Abs(target-cur)), target-cur)
			})
		}
	}()

	// Discovery: the plaintext broadcast of port 6666, sent to this machine.
	go func() {
		conn, err := net.Dial("udp4", "127.0.0.1:6666")
		if err != nil {
			return
		}
		msg, _ := json.Marshal(map[string]string{"ip": "127.0.0.1", "gwId": tuyaSimID, "version": "3.4"})
		for range time.Tick(time.Second) {
			conn.Write(msg)
		}
	}()

	ok := func(w http.ResponseWriter, result any) {
		writeJSON(w, 200, map[string]any{"success": true, "result": result, "t": time.Now().UnixMilli()})
	}
	mux.HandleFunc("GET /v1.0/token", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"access_token": "hwsim", "expire_time": 7200})
	})
	mux.HandleFunc("GET /v1.0/iot-01/associated-users/devices", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"devices": []map[string]any{{
			"id": tuyaSimID, "name": "Radiator (sim)", "local_key": tuyaSimKey,
			"category": "qn", "product_name": "Elektrische radiator",
		}}})
	})
	mux.HandleFunc("GET /v2.0/cloud/thing/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"model": tuyaSimModel})
	})
	log.Printf("Tuya     radiator (3.4)  %s (add a Tuya plugin with region http://127.0.0.1:%d and scan)", ln.Addr(), cloudPort)
}

package mhi

import (
	"encoding/base64"
	"fmt"
)

// The frame setAirconStat takes, after the manufacturer's AirconStatCoder
// (via pywfrac): a command half and a receive (echo) half, each 18 state
// bytes plus an empty variable trailer and a CRC16-CCITT. A value in the
// command half only takes effect with its set-bit, and every write here
// sets all of them, so the whole state must be complete: encode a state
// read from the unit with only the wanted field changed.

var (
	cmdMode = map[string]byte{"AUTO": 32, "COOL": 40, "HEAT": 48, "FAN": 44, "DRY": 36}
	cmdFan  = map[string]byte{"AUTO": 15, "QUIET": 8, "LOW": 9, "MEDIUM": 10, "HIGH": 14}
	rcvMode = map[string]byte{"AUTO": 0, "COOL": 8, "HEAT": 16, "FAN": 12, "DRY": 4}
	rcvFan  = map[string]byte{"AUTO": 7, "QUIET": 0, "LOW": 1, "MEDIUM": 2, "HIGH": 6}
	// vertical vanes -> (byte 2, byte 3); horizontal -> (byte 12, byte 11)
	cmdSwingUD = [5][2]byte{{192, 128}, {128, 128}, {128, 144}, {128, 160}, {128, 176}}
	cmdSwingLR = [8][2]byte{{3, 16}, {2, 16}, {2, 17}, {2, 18}, {2, 19}, {2, 20}, {2, 21}, {2, 22}}
)

// variableSuffix is the "nothing to send" variable trailer.
var variableSuffix = []byte{1, 255, 255, 255, 255}

func emptyStat() []byte {
	return []byte{0, 0, 0, 0, 0, 255, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

// encodeStat builds the base64 setAirconStat frame for a state.
func encodeStat(st State) (string, error) {
	cmd, err := commandBytes(st)
	if err != nil {
		return "", err
	}
	rcv, err := receiveBytes(st)
	if err != nil {
		return "", err
	}
	frame := addCRC(append(cmd, variableSuffix...))
	frame = append(frame, addCRC(append(rcv, variableSuffix...))...)
	return base64.StdEncoding.EncodeToString(frame), nil
}

// The unit accepts setpoints from 16 to 31 °C.
const (
	minPresetC = 16
	maxPresetC = 31
)

func presetByte(c float64) (byte, error) {
	if c < minPresetC || c > maxPresetC {
		return 0, fmt.Errorf("temperatuur %.1f °C buiten %d-%d", c, minPresetC, maxPresetC)
	}
	return byte(int(c / 0.5)), nil
}

func commandBytes(st State) ([]byte, error) {
	b := emptyStat()
	if st.Power {
		b[2] |= 3
	} else {
		b[2] |= 2
	}
	m, ok := cmdMode[st.Mode]
	if !ok {
		return nil, fmt.Errorf("onbekende modus %q", st.Mode)
	}
	b[2] |= m
	f, ok := cmdFan[st.Fan]
	if !ok {
		return nil, fmt.Errorf("onbekende ventilatorstand %q", st.Fan)
	}
	b[3] |= f
	if st.SwingUD >= 0 && st.SwingUD < len(cmdSwingUD) {
		b[2] |= cmdSwingUD[st.SwingUD][0]
		b[3] |= cmdSwingUD[st.SwingUD][1]
	}
	if st.SwingLR >= 0 && st.SwingLR < len(cmdSwingLR) {
		b[12] |= cmdSwingLR[st.SwingLR][0]
		b[11] |= cmdSwingLR[st.SwingLR][1]
	}
	p, err := presetByte(st.PresetC)
	if err != nil {
		return nil, err
	}
	b[4] |= p + 128
	if st.Entrust {
		b[12] |= 12
	} else {
		b[12] |= 8
	}
	if !st.CoolHotJudge {
		b[8] |= 8
	}
	if st.ModelNr == 1 && st.Vacant {
		b[10] |= 1
	}
	if st.ModelNr == 1 || st.ModelNr == 2 {
		if st.SelfClean {
			b[12] |= 144
		} else {
			b[12] |= 128
		}
	}
	return b, nil
}

func receiveBytes(st State) ([]byte, error) {
	b := emptyStat()
	if st.Power {
		b[2] |= 1
	}
	m, ok := rcvMode[st.Mode]
	if !ok {
		return nil, fmt.Errorf("onbekende modus %q", st.Mode)
	}
	b[2] |= m
	f, ok := rcvFan[st.Fan]
	if !ok {
		return nil, fmt.Errorf("onbekende ventilatorstand %q", st.Fan)
	}
	b[3] |= f
	switch {
	case st.SwingUD == 0:
		b[2] |= 64
	case st.SwingUD >= 2 && st.SwingUD <= 4:
		b[3] |= byte(st.SwingUD-1) * 16
	}
	switch {
	case st.SwingLR == 0:
		b[12] |= 1
	case st.SwingLR >= 1 && st.SwingLR <= 7:
		b[11] |= byte(st.SwingLR - 1)
	}
	p, err := presetByte(st.PresetC)
	if err != nil {
		return nil, err
	}
	b[4] |= p
	if st.Entrust {
		b[12] |= 4
	}
	if !st.CoolHotJudge {
		b[8] |= 8
	}
	// the model byte is echoed as reported; a mismatch is refused (result 12)
	if st.ModelRaw >= 0 && st.ModelRaw <= 127 {
		b[0] |= byte(st.ModelRaw)
	}
	if st.ModelNr == 1 && st.Vacant {
		b[10] |= 1
	}
	if st.ModelNr == 1 || st.ModelNr == 2 {
		if st.SelfClean {
			b[12] |= 144
		} else {
			b[12] |= 128
		}
	}
	return b, nil
}

// crc16 is CRC16-CCITT (polynomial 0x1021, initial 0xFFFF, MSB first).
func crc16(data []byte) uint16 {
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

// addCRC appends the CRC of b, low byte first.
func addCRC(b []byte) []byte {
	c := crc16(b)
	return append(b, byte(c), byte(c>>8))
}

package mhi

import (
	"encoding/base64"
	"testing"
)

// The CRC of the command half in a real frame from the unit must match
// ours. (The unit's receive half carries a checksum of its own making over
// the temperature segments; nothing reads it, and pywfrac does not either.)
func TestCRCMatchesUnit(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(sampleStat)
	start := int(int8(raw[18]))*4 + 21
	if got, want := crc16(raw[:start-2]), uint16(raw[start-2])|uint16(raw[start-1])<<8; got != want {
		t.Errorf("command half CRC %04x, unit %04x", got, want)
	}
}

// Encoding a decoded state and decoding it again gives the same settings.
func TestEncodeRoundTrip(t *testing.T) {
	st, err := decodeStat(sampleStat)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*State){
		func(*State) {},
		func(s *State) { s.Power = false },
		func(s *State) { s.Mode = "COOL"; s.PresetC = 24.5 },
		func(s *State) { s.Fan = "HIGH"; s.SwingUD = 0 },
		func(s *State) { s.Mode = "DRY"; s.Fan = "AUTO"; s.SwingLR = 4; s.Entrust = true },
	} {
		want := st
		change(&want)
		b64, err := encodeStat(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeStat(b64)
		if err != nil {
			t.Fatal(err)
		}
		if got.Power != want.Power || got.Mode != want.Mode || got.PresetC != want.PresetC || got.Fan != want.Fan ||
			got.SwingUD != want.SwingUD || got.SwingLR != want.SwingLR || got.Entrust != want.Entrust ||
			got.CoolHotJudge != want.CoolHotJudge || got.ModelRaw != want.ModelRaw || got.Vacant != want.Vacant {
			t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
		}
		raw, _ := base64.StdEncoding.DecodeString(b64)
		if len(raw) != 50 || raw[4]&128 == 0 || raw[2]&3 == 0 {
			t.Errorf("command half without set-bits: % x", raw[:25])
		}
	}
	if _, err := encodeStat(State{Mode: "HEAT", Fan: "AUTO", PresetC: 40}); err == nil {
		t.Error("out-of-range setpoint encoded")
	}
}

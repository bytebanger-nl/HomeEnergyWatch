package mhi

import "testing"

// A real getAirconStat blob captured from a WF-RAC unit (heating, setpoint
// 21.5 °C, room 23.2 °C, outdoor 2.2 °C, fan quiet, no fault).
const sampleStat = "AAezqKv/AAAAAAASigAAAAAAAf/////ejoEEESArmAAAgAIAAgAAAAAAAAOAIJj/gBBi/5QQAADRSA=="

func TestDecodeStat(t *testing.T) {
	st, err := decodeStat(sampleStat)
	if err != nil {
		t.Fatalf("decodeStat: %v", err)
	}
	if !st.Power {
		t.Error("Power = false, want true")
	}
	if st.Mode != "HEAT" {
		t.Errorf("Mode = %q, want HEAT", st.Mode)
	}
	if st.PresetC != 21.5 {
		t.Errorf("PresetC = %v, want 21.5", st.PresetC)
	}
	if st.Fan != "QUIET" {
		t.Errorf("Fan = %q, want QUIET", st.Fan)
	}
	if st.SwingUD != 3 {
		t.Errorf("SwingUD = %d, want 3", st.SwingUD)
	}
	if !st.CompressorRunning {
		t.Error("CompressorRunning = false, want true")
	}
	if st.ErrorCode != "00" {
		t.Errorf("ErrorCode = %q, want 00", st.ErrorCode)
	}
	if st.ModelRaw != 1 {
		t.Errorf("ModelRaw = %d, want 1", st.ModelRaw)
	}
	if st.IndoorC == nil || *st.IndoorC != 23.2 {
		t.Errorf("IndoorC = %v, want 23.2", st.IndoorC)
	}
	if st.OutdoorC == nil || *st.OutdoorC != 2.2 {
		t.Errorf("OutdoorC = %v, want 2.2", st.OutdoorC)
	}
}

func TestDecodeStatShort(t *testing.T) {
	if _, err := decodeStat("AAAA"); err == nil {
		t.Error("want error for a too-short blob")
	}
}

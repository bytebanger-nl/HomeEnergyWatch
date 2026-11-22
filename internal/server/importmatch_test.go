package server

import (
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/importer"
)

func socket(id string, importKWh float64) gateway.DeviceView {
	return gateway.DeviceView{ID: id, Name: id, Kind: homewizard.KindSocket,
		Reading: &homewizard.Reading{Values: map[string]float64{"energy_import_kwh": importKWh}}}
}

func TestMatchByCounter(t *testing.T) {
	last := func(v float64) *importer.Result { return &importer.Result{Kind: importer.KindMeter, LastImportKWh: &v} }
	two := []gateway.DeviceView{socket("skt", 1425.1), socket("skt2", 1873.0)}

	for _, tc := range []struct {
		name  string
		cands []gateway.DeviceView
		res   *importer.Result
		want  string // "" = undecided
	}{
		{"first socket", two, last(1421.5), "skt"},
		{"second socket", two, last(1871.2), "skt2"},
		{"only one candidate", two[:1], last(5000), "skt"},
		{"file above every counter", two, last(9000), ""},
		{"both close", []gateway.DeviceView{socket("a", 100.4), socket("b", 100.6)}, last(100), ""},
		{"offline device ignored", []gateway.DeviceView{{ID: "off", Kind: homewizard.KindSocket}, socket("on", 51)}, last(50), "on"},
		{"no counter in file", two, &importer.Result{Kind: importer.KindMeter}, ""},
	} {
		got, ok := matchByCounter(tc.cands, tc.res)
		if tc.want == "" && ok {
			t.Errorf("%s: picked %s, want undecided", tc.name, got.ID)
		}
		if tc.want != "" && (!ok || got.ID != tc.want) {
			t.Errorf("%s: got %q (%v), want %s", tc.name, got.ID, ok, tc.want)
		}
	}
}

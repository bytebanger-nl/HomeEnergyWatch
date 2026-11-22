package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	_ "github.com/bytebanger-nl/homeenergywatch/internal/plugin/mhi" // a provider for the test device
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// tileServer is a server with one plugin device ("d1") and tiles.
func tileServer(t *testing.T) http.Handler { return withTiles(t, newServer(t)).Handler() }

// withTiles gives s one plugin device ("d1") and tiles.
func withTiles(t *testing.T, s *Server) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tiles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.SavePlugin(ctx, store.PluginInstance{ID: "p1", Type: "mhi", Name: "Airco", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePluginDevice(ctx, store.PluginDevice{ID: "d1", PluginID: "p1", Name: "Woonkamer", Address: "127.0.0.1:1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Plugins = plugin.NewManager(st, s.Hub, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	if err := s.Plugins.Load(ctx); err != nil {
		t.Fatal(err)
	}
	s.Tiles = st
	return s
}

func TestDashboardTilesAPI(t *testing.T) {
	h := tileServer(t)
	const lan = "192.168.1.20:5000"
	ct := map[string]string{"Content-Type": "application/json"}
	list := func() []store.DashboardTile {
		t.Helper()
		w := do(h, "GET", "/api/dashboard/tiles", lan, "", nil)
		var res struct{ Tiles []store.DashboardTile }
		if w.Code != http.StatusOK || decode(w.Body.Bytes(), &res) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return res.Tiles
	}
	// there are no tiles until the user adds one
	if tiles := list(); len(tiles) != 0 {
		t.Fatalf("new gateway has tiles: %+v", tiles)
	}

	for _, bad := range []struct{ body, want string }{
		{`{"name":"  ","items":[]}`, "naam"},
		{`{"name":"x","size":"huge","items":[]}`, "grootte"},
		{`{"name":"x","items":[{"type":"value","device":"nope","metric":"t"}]}`, "onbekend apparaat"},
		{`{"name":"x","items":[{"type":"value","device":"d1"}]}`, "meetwaarde"},
		{`{"name":"x","items":[{"type":"chart","series":[]}]}`, "lijnen"},
		{`{"name":"x","items":[{"type":"chart","series":[{"device":"d1","metric":"t"}],"hours":5}]}`, "periode"},
		{`{"name":"x","items":[{"type":"control","device":"d1"}]}`, "bediening"},
		{`{"name":"x","items":[{"type":"gauge","device":"d1"}]}`, "onbekende soort"},
	} {
		w := do(h, "POST", "/api/dashboard/tiles", lan, bad.body, ct)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), bad.want) {
			t.Errorf("%s: %d %s, want 400 with %q", bad.body, w.Code, w.Body, bad.want)
		}
	}

	w := do(h, "POST", "/api/dashboard/tiles", lan,
		`{"name":" Klimaat ","items":[{"type":"value","device":"d1","metric":"outdoor_c","hours":6},{"type":"chart","series":[{"device":"d1","metric":"room_c"}]}]}`, ct)
	if w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	var a store.DashboardTile
	if err := decode(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	// trimmed name, default size, chart period 24 h, no stray fields
	if a.Name != "Klimaat" || a.Size != "small" ||
		string(a.Items) != `[{"type":"value","device":"d1","metric":"outdoor_c"},{"type":"chart","series":[{"device":"d1","metric":"room_c"}],"hours":24}]` {
		t.Fatalf("added %+v %s", a, a.Items)
	}
	w = do(h, "POST", "/api/dashboard/tiles", lan, `{"name":"Bediening","size":"wide","items":[{"type":"control","device":"d1","control":"power"}]}`, ct)
	var b store.DashboardTile
	if w.Code != http.StatusCreated || decode(w.Body.Bytes(), &b) != nil {
		t.Fatalf("add second: %d %s", w.Code, w.Body)
	}

	w = do(h, "PUT", "/api/dashboard/tiles/order", lan, `{"ids":[`+itoa(b.ID)+`,`+itoa(a.ID)+`]}`, ct)
	if w.Code != http.StatusNoContent {
		t.Fatalf("order: %d %s", w.Code, w.Body)
	}
	if tiles := list(); len(tiles) != 2 || tiles[0].ID != b.ID {
		t.Fatalf("after order: %+v", tiles)
	}

	w = do(h, "PUT", "/api/dashboard/tiles/"+itoa(a.ID), lan, `{"name":"Buiten","size":"wide","items":[]}`, ct)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"Buiten"`) {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/api/dashboard/tiles/999", lan, `{"name":"x","items":[]}`, ct); w.Code != http.StatusNotFound {
		t.Fatalf("update unknown: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/dashboard/tiles/"+itoa(a.ID), lan, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if tiles := list(); len(tiles) != 1 || tiles[0].ID != b.ID {
		t.Fatalf("after delete: %+v", tiles)
	}
	// management from outside the LAN is refused
	if w := do(h, "POST", "/api/dashboard/tiles", "8.8.8.8:5000", `{"name":"x","items":[]}`, ct); w.Code != http.StatusForbidden {
		t.Fatalf("public client: %d", w.Code)
	}
}

// Without plugins there are no tile endpoints.
func TestDashboardTilesNeedPlugins(t *testing.T) {
	h := newServer(t).Handler()
	if w := do(h, "GET", "/api/dashboard/tiles", "192.168.1.20:5000", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

// The form of a tile and the options of its items are kept; the extras are
// off unless asked for, and a status item takes a second metric.
func TestDashboardTileStyleAndOptions(t *testing.T) {
	h := tileServer(t)
	const lan = "192.168.1.20:5000"
	ct := map[string]string{"Content-Type": "application/json"}
	body := `{"name":"Huis","style":"list","items":[` +
		`{"type":"value","device":"d1","metric":"indoor","label":"Binnen","target":"preset","show_trend":true,"metric2":"x"},` +
		`{"type":"status","device":"d1","metric":"mode","metric2":"preset","show_trend":true,"target":"y"},` +
		`{"type":"control","device":"d1","control":"mode","label":" Airco woonkamer ","show_device":true,"show_trend":true,"metric":"x"}]}`
	w := do(h, "POST", "/api/dashboard/tiles", lan, body, ct)
	if w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	var tile store.DashboardTile
	if err := decode(w.Body.Bytes(), &tile); err != nil {
		t.Fatal(err)
	}
	var items []TileItem
	if err := decode(tile.Items, &items); err != nil {
		t.Fatal(err)
	}
	if tile.Style != "list" || len(items) != 3 {
		t.Fatalf("tile %+v items %+v", tile, items)
	}
	if v := items[0]; v.Target != "preset" || !v.ShowTrend || v.ShowDevice || v.ShowRange || v.Metric2 != "" {
		t.Errorf("value item %+v", v)
	}
	if s := items[1]; s.Metric2 != "preset" || s.ShowTrend || s.Target != "" {
		t.Errorf("status item %+v", s)
	}
	// a control keeps its own name and the device name, nothing of a value
	if c := items[2]; c.Label != "Airco woonkamer" || !c.ShowDevice || c.ShowTrend || c.Metric != "" || c.Control != "mode" {
		t.Errorf("control item %+v", c)
	}
	// a tile without a form gets the default
	w = do(h, "POST", "/api/dashboard/tiles", lan, `{"name":"Kaal","items":[{"type":"value","device":"d1","metric":"indoor"}]}`, ct)
	if decode(w.Body.Bytes(), &tile) != nil || tile.Style != "value" {
		t.Fatalf("default form: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{
		`{"name":"x","style":"round","items":[]}`,
		`{"name":"x","items":[{"type":"status","device":"d1"}]}`,
	} {
		if w := do(h, "POST", "/api/dashboard/tiles", lan, bad, ct); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, w.Code)
		}
	}
}

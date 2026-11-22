package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Dashboard tiles of the user's own: a name, a size and a list of items
// that show something of a plugin device. There are none until the user
// adds one.
const (
	maxTiles       = 20
	maxTileItems   = 12
	maxTileSeries  = 4
	maxTileNameLen = 60
)

// TileItem is one thing on a tile:
//   - value: the current value of a metric (Device, Metric), by default
//     nothing else; on request also the device's name (ShowDevice), the
//     change over the last hour (ShowTrend), today's lowest and highest
//     (ShowRange) and a second metric of the device as what it is set to
//     (Target, e.g. "18,2 °C → 20,0 °C")
//   - status: a device's state as a label (Metric, a text or on/off, plus
//     optionally a number Metric2: "Verwarmen · 22 °C")
//   - chart: one or more numeric metrics (Series) over the last Hours
//   - control: one control of the device (Device, Control), optionally
//     with the device's name (ShowDevice: "Modus · Airco"); or a manual
//     action (Rule, the id of an automation that starts by hand), shown as
//     a switch or a button
//
// Label optionally replaces the metric's or control's own label.
type TileItem struct {
	Type       string       `json:"type"`
	Device     string       `json:"device,omitempty"`
	Metric     string       `json:"metric,omitempty"`
	Metric2    string       `json:"metric2,omitempty"`
	Target     string       `json:"target,omitempty"`
	Control    string       `json:"control,omitempty"`
	Rule       int64        `json:"rule,omitempty"`
	Series     []TileSeries `json:"series,omitempty"`
	Hours      int          `json:"hours,omitempty"`
	Label      string       `json:"label,omitempty"`
	ShowDevice bool         `json:"show_device,omitempty"`
	ShowTrend  bool         `json:"show_trend,omitempty"`
	ShowRange  bool         `json:"show_range,omitempty"`
}

// TileSeries is one line in a chart item.
type TileSeries struct {
	Device string `json:"device"`
	Metric string `json:"metric"`
}

// tileHours are the chart periods offered.
var tileHours = map[int]bool{6: true, 24: true, 168: true}

// tileStyles are the forms of a tile: each value large under the other
// (value, the default), a list of names and values, a grid of two columns,
// one big value, or values with a small line of their course.
var tileStyles = map[string]bool{"value": true, "list": true, "grid": true, "big": true, "spark": true}

type tileBody struct {
	Name  string     `json:"name"`
	Size  string     `json:"size"`
	Style string     `json:"style"`
	Items []TileItem `json:"items"`
}

// check normalizes the body and returns it as a tile to store, or an error
// in Dutch for the user. known reports whether a plugin device exists,
// manual whether a rule is a manual action.
func (b tileBody) check(known func(string) bool, manual func(int64) bool) (store.DashboardTile, error) {
	var t store.DashboardTile
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return t, fmt.Errorf("geef de tegel een naam")
	}
	if utf8.RuneCountInString(name) > maxTileNameLen {
		return t, fmt.Errorf("de naam mag hoogstens %d tekens zijn", maxTileNameLen)
	}
	size := b.Size
	if size == "" {
		size = "small"
	}
	if size != "small" && size != "wide" {
		return t, fmt.Errorf("onbekende grootte %q (small, wide)", b.Size)
	}
	style := b.Style
	if style == "" {
		style = "value"
	}
	if !tileStyles[style] {
		return t, fmt.Errorf("onbekende vorm %q (value, list, grid, big, spark)", b.Style)
	}
	if len(b.Items) > maxTileItems {
		return t, fmt.Errorf("een tegel heeft hoogstens %d onderdelen", maxTileItems)
	}
	items := make([]TileItem, 0, len(b.Items))
	for i, it := range b.Items {
		n := i + 1
		it.Label = strings.TrimSpace(it.Label)
		if utf8.RuneCountInString(it.Label) > maxTileNameLen {
			return t, fmt.Errorf("onderdeel %d: het label mag hoogstens %d tekens zijn", n, maxTileNameLen)
		}
		device := func(id string) error {
			if id == "" {
				return fmt.Errorf("onderdeel %d: kies een apparaat", n)
			}
			if !known(id) {
				return fmt.Errorf("onderdeel %d: onbekend apparaat %q", n, id)
			}
			return nil
		}
		if it.Type != "control" {
			it.Rule = 0
		}
		switch it.Type {
		case "value":
			if err := device(it.Device); err != nil {
				return t, err
			}
			if it.Metric == "" {
				return t, fmt.Errorf("onderdeel %d: kies een meetwaarde", n)
			}
			it.Control, it.Series, it.Hours, it.Metric2 = "", nil, 0, ""
		case "status":
			if err := device(it.Device); err != nil {
				return t, err
			}
			if it.Metric == "" {
				return t, fmt.Errorf("onderdeel %d: kies wat de status laat zien", n)
			}
			it.Control, it.Series, it.Hours, it.Target = "", nil, 0, ""
			it.ShowTrend, it.ShowRange = false, false
		case "control":
			if it.Rule != 0 {
				if !manual(it.Rule) {
					return t, fmt.Errorf("onderdeel %d: onbekende actie", n)
				}
				it.Device, it.Control, it.Metric, it.Series, it.Hours, it.Metric2, it.Target = "", "", "", nil, 0, "", ""
				it.ShowTrend, it.ShowRange = false, false
				break
			}
			if err := device(it.Device); err != nil {
				return t, err
			}
			if it.Control == "" {
				return t, fmt.Errorf("onderdeel %d: kies een bediening", n)
			}
			it.Metric, it.Series, it.Hours, it.Metric2, it.Target = "", nil, 0, "", ""
			it.ShowTrend, it.ShowRange = false, false
		case "chart":
			if len(it.Series) == 0 || len(it.Series) > maxTileSeries {
				return t, fmt.Errorf("onderdeel %d: een grafiek heeft 1 tot %d lijnen", n, maxTileSeries)
			}
			for _, s := range it.Series {
				if err := device(s.Device); err != nil {
					return t, err
				}
				if s.Metric == "" {
					return t, fmt.Errorf("onderdeel %d: kies een meetwaarde voor elke lijn", n)
				}
			}
			if it.Hours == 0 {
				it.Hours = 24
			}
			if !tileHours[it.Hours] {
				return t, fmt.Errorf("onderdeel %d: een periode van %d uur kan niet (6, 24 of 168)", n, it.Hours)
			}
			it.Device, it.Metric, it.Control, it.Metric2, it.Target = "", "", "", "", ""
			it.ShowDevice, it.ShowTrend, it.ShowRange = false, false, false
		default:
			return t, fmt.Errorf("onderdeel %d: onbekende soort %q (value, status, chart, control)", n, it.Type)
		}
		items = append(items, it)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return t, err
	}
	return store.DashboardTile{Name: name, Size: size, Style: style, Items: raw}, nil
}

// routeTiles registers the dashboard tile endpoints. Tiles show plugin
// devices, so without plugins there are none.
func (s *Server) routeTiles(mux *http.ServeMux) {
	if s.Tiles == nil || s.Plugins == nil {
		return
	}
	mux.HandleFunc("GET /api/dashboard/tiles", s.listTiles)
	mux.HandleFunc("POST /api/dashboard/tiles", s.addTile)
	mux.HandleFunc("PUT /api/dashboard/tiles/order", s.orderTiles)
	mux.HandleFunc("PUT /api/dashboard/tiles/{id}", s.updateTile)
	mux.HandleFunc("DELETE /api/dashboard/tiles/{id}", s.deleteTile)
}

// knownPluginDevice reports whether a plugin device exists.
func (s *Server) knownPluginDevice() func(string) bool {
	ids := map[string]bool{}
	for _, d := range s.Plugins.DeviceViews() {
		ids[d.ID] = true
	}
	return func(id string) bool { return ids[id] }
}

func (s *Server) listTiles(w http.ResponseWriter, r *http.Request) {
	tiles, err := s.Tiles.DashboardTiles(r.Context())
	if err != nil {
		s.Log.Error("tiles", "err", err)
		writeError(w, http.StatusInternalServerError, "tegels niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiles": tiles, "max": maxTiles})
}

func (s *Server) addTile(w http.ResponseWriter, r *http.Request) {
	var body tileBody
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := body.check(s.knownPluginDevice(), s.knownManualAction)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tiles, err := s.Tiles.DashboardTiles(r.Context())
	if err != nil {
		s.Log.Error("tiles", "err", err)
		writeError(w, http.StatusInternalServerError, "tegels niet beschikbaar")
		return
	}
	if len(tiles) >= maxTiles {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("je kunt hoogstens %d tegels maken", maxTiles))
		return
	}
	if t, err = s.Tiles.AddDashboardTile(r.Context(), t); err != nil {
		s.Log.Error("tiles", "err", err)
		writeError(w, http.StatusInternalServerError, "tegel niet opgeslagen")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) updateTile(w http.ResponseWriter, r *http.Request) {
	id, ok := tabID(w, r)
	if !ok {
		return
	}
	var body tileBody
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := body.check(s.knownPluginDevice(), s.knownManualAction)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t.ID = id
	if err := s.Tiles.UpdateDashboardTile(r.Context(), t); err != nil {
		pluginError(w, err)
		return
	}
	if t, err = s.Tiles.DashboardTile(r.Context(), id); err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTile(w http.ResponseWriter, r *http.Request) {
	id, ok := tabID(w, r)
	if !ok {
		return
	}
	if err := s.Tiles.DeleteDashboardTile(r.Context(), id); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// orderTiles puts the tiles in a new order: {"ids": [3, 1, 2]}.
func (s *Server) orderTiles(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.IDs) > maxTiles {
		writeError(w, http.StatusBadRequest, "te veel tegels")
		return
	}
	if err := s.Tiles.OrderDashboardTiles(r.Context(), body.IDs); err != nil {
		s.Log.Error("tiles", "err", err)
		writeError(w, http.StatusInternalServerError, "volgorde niet opgeslagen")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// knownManualAction reports whether rule id is a manual action.
func (s *Server) knownManualAction(id int64) bool {
	if s.Rules == nil {
		return false
	}
	for _, v := range s.Rules.List() {
		if v.ID == id {
			return v.Manual
		}
	}
	return false
}

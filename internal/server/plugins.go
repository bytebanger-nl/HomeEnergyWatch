package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/control"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// routePlugins registers the plugin endpoints. No-op when plugins are off.
func (s *Server) routePlugins(mux *http.ServeMux) {
	if s.Plugins == nil {
		return
	}
	mux.HandleFunc("GET /api/plugins", s.listPlugins)
	mux.HandleFunc("POST /api/plugins", s.addPlugin)
	mux.HandleFunc("PATCH /api/plugins/{id}", s.updatePlugin)
	mux.HandleFunc("DELETE /api/plugins/{id}", s.deletePlugin)
	mux.HandleFunc("POST /api/plugins/{id}/scan", s.scanPlugin)
	mux.HandleFunc("POST /api/plugins/{id}/auth", s.startPluginAuth)
	mux.HandleFunc("GET /api/plugins/{id}/auth", s.pluginAuthStatus)
	mux.HandleFunc("POST /api/plugins/{id}/devices", s.addPluginDevice)
	mux.HandleFunc("PATCH /api/plugins/devices/{id}", s.updatePluginDevice)
	mux.HandleFunc("DELETE /api/plugins/devices/{id}", s.deletePluginDevice)
	mux.HandleFunc("PUT /api/plugins/devices/{id}/meter", s.setPluginMeter)
	mux.HandleFunc("GET /api/plugins/devices/{id}/history", s.pluginHistory)
	mux.HandleFunc("POST /api/plugins/devices/{id}/control", s.pluginControl)
	mux.HandleFunc("PUT /api/plugins/devices/{id}/tab", s.setPluginDeviceTab)
	mux.HandleFunc("GET /api/plugins/tabs", s.listPluginTabs)
	mux.HandleFunc("POST /api/plugins/tabs", s.addPluginTab)
	mux.HandleFunc("PATCH /api/plugins/tabs/{id}", s.renamePluginTab)
	mux.HandleFunc("DELETE /api/plugins/tabs/{id}", s.deletePluginTab)
}

func (s *Server) listPluginTabs(w http.ResponseWriter, r *http.Request) {
	tabs, err := s.Plugins.Tabs(r.Context())
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tabs": tabs, "max": plugin.MaxTabs})
}

func (s *Server) addPluginTab(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := s.Plugins.AddTab(r.Context(), body.Name)
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func tabID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "niet gevonden")
		return 0, false
	}
	return id, true
}

func (s *Server) renamePluginTab(w http.ResponseWriter, r *http.Request) {
	id, ok := tabID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := s.Plugins.RenameTab(r.Context(), id, body.Name); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePluginTab(w http.ResponseWriter, r *http.Request) {
	id, ok := tabID(w, r)
	if !ok {
		return
	}
	if err := s.Plugins.DeleteTab(r.Context(), id); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setPluginDeviceTab puts a plugin device on a tab: {"tab_id": n}, 0 =
// Klimaat.
func (s *Server) setPluginDeviceTab(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TabID int64 `json:"tab_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := s.Plugins.SetDeviceTab(r.Context(), r.PathValue("id"), body.TabID); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pluginControl changes one control of a plugin device: {"key", "value"}.
// Only when control is allowed for the instance (off by default).
func (s *Server) pluginControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	st, err := s.control().Start(control.Command{Device: control.PluginPrefix + r.PathValue("id"), Key: body.Key, Value: body.Value, Source: "app"})
	if err != nil {
		pluginError(w, err)
		return
	}
	// sent in the background: checked and tried again, progress as
	// "control" events
	writeJSON(w, http.StatusAccepted, st)
}

// setPluginMeter links a socket or kWh meter to a plugin device:
// {"meter": "<device id>"}; an empty meter removes the link. One meter per
// device; the same meter may measure several devices.
func (s *Server) setPluginMeter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Meter string `json:"meter"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if id := body.Meter; id != "" {
		v, ok := s.Manager.View(id)
		if !ok {
			writeError(w, http.StatusBadRequest, "meter niet gevonden: "+id)
			return
		}
		if v.Kind != homewizard.KindSocket && v.Kind != homewizard.KindKWH {
			writeError(w, http.StatusBadRequest, "alleen een tussenstekker of kWh-meter kan aan een plugin-apparaat gekoppeld worden")
			return
		}
	}
	if err := s.Plugins.SetMeter(r.Context(), r.PathValue("id"), body.Meter); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pluginHistory serves one numeric metric of a plugin device over time:
// ?metric&from&to (RFC 3339, default the last 24 hours)&points.
func (s *Server) pluginHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := q.Get("metric")
	if metric == "" {
		writeError(w, http.StatusBadRequest, "'metric' ontbreekt")
		return
	}
	var err error
	to := time.Now().UTC()
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'to' (RFC 3339 verwacht)")
			return
		}
	}
	from := to.Add(-24 * time.Hour)
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'from' (RFC 3339 verwacht)")
			return
		}
	}
	if !to.After(from) || to.Sub(from) > 400*24*time.Hour {
		writeError(w, http.StatusBadRequest, "ongeldige periode")
		return
	}
	points, _ := strconv.Atoi(q.Get("points"))
	if points <= 0 || points > 5000 {
		points = 720
	}
	series, step, err := s.Plugins.Series(r.Context(), r.PathValue("id"), metric, from.UTC(), to.UTC(), points)
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.UTC(), "to": to.UTC(), "step_s": int(step.Seconds()), "points": series})
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"types":     s.Plugins.Types(),
		"instances": s.Plugins.Views(),
	})
}

func (s *Server) addPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type   string            `json:"type"`
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	iv, err := s.Plugins.AddInstance(r.Context(), body.Type, body.Name, body.Config)
	if err != nil {
		pluginError(w, err)
		return
	}
	s.wakeIsta()
	writeJSON(w, http.StatusCreated, iv)
}

func (s *Server) updatePlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    *string           `json:"name"`
		Enabled *bool             `json:"enabled"`
		Control *bool             `json:"control"`
		Config  map[string]string `json:"config"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	iv, err := s.Plugins.UpdateInstance(r.Context(), r.PathValue("id"), body.Name, body.Enabled, body.Control, body.Config)
	if err != nil {
		pluginError(w, err)
		return
	}
	s.wakeIsta()
	writeJSON(w, http.StatusOK, iv)
}

func (s *Server) deletePlugin(w http.ResponseWriter, r *http.Request) {
	if err := s.Plugins.DeleteInstance(r.Context(), r.PathValue("id")); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scanPlugin(w http.ResponseWriter, r *http.Request) {
	cands, err := s.Plugins.Scan(r.Context(), r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": cands})
}

func (s *Server) startPluginAuth(w http.ResponseWriter, r *http.Request) {
	ch, err := s.Plugins.StartAuth(r.Context(), r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) pluginAuthStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Plugins.AuthStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) addPluginDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string            `json:"name"`
		Address string            `json:"address"`
		Config  map[string]string `json:"config"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	dv, err := s.Plugins.AddDevice(r.Context(), r.PathValue("id"),
		plugin.Candidate{Address: body.Address, Name: body.Name, Config: body.Config}, body.Name)
	if err != nil {
		pluginError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dv)
}

func (s *Server) updatePluginDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		Control *bool   `json:"control"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := s.Plugins.UpdateDevice(r.Context(), r.PathValue("id"), body.Name, body.Enabled, body.Control); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePluginDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.Plugins.DeleteDevice(r.Context(), r.PathValue("id")); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeBody reads a JSON request body, writing a 400 on failure.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := decodeOne(json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)), v); err != nil {
		writeError(w, http.StatusBadRequest, "ongeldige aanvraag: "+err.Error())
		return false
	}
	return true
}

func pluginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "niet gevonden")
	case errors.Is(err, plugin.ErrUnknownType):
		writeError(w, http.StatusBadRequest, "onbekend plugin-type")
	case errors.Is(err, plugin.ErrDuplicate):
		writeError(w, http.StatusConflict, "dit apparaat is al toegevoegd")
	case errors.Is(err, plugin.ErrControlOff):
		writeError(w, http.StatusForbidden, "bediening staat uit voor deze plugin of dit apparaat (Instellingen → Plugins)")
	case errors.Is(err, plugin.ErrNoControl):
		writeError(w, http.StatusBadRequest, "dit apparaat heeft die bediening niet")
	case errors.Is(err, plugin.ErrNoMeter):
		writeError(w, http.StatusBadRequest, "aan dit soort apparaat kun je geen meter koppelen")
	case errors.Is(err, plugin.ErrTabs):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, plugin.ErrDisabled), errors.Is(err, plugin.ErrValue), errors.Is(err, control.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, plugin.ErrNoAuth):
		writeError(w, http.StatusBadRequest, "deze plugin heeft geen inlogstap")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

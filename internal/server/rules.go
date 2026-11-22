package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/rules"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	list := []rules.View{}
	regulated := map[int64]time.Time{}
	if s.Rules != nil {
		list = s.Rules.List()
		if m, err := s.Rules.LastRegulated(r.Context()); err != nil {
			s.Log.Warn("rules: last regulated", "err", err)
		} else {
			regulated = m
		}
	}
	// regulated: per automation id, when it last regulated (for its chart)
	writeJSON(w, http.StatusOK, map[string]any{"rules": list, "regulated": regulated})
}

// rulePeriods returns when an automation regulated between from and to
// (RFC 3339; default the last 24 hours).
func (s *Server) rulePeriods(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Rules == nil {
		writeError(w, http.StatusNotFound, "regel niet gevonden")
		return
	}
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	for name, t := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := r.URL.Query().Get(name); v != "" {
			p, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "ongeldige tijd: "+name)
				return
			}
			*t = p
		}
	}
	if !from.Before(to) {
		writeError(w, http.StatusBadRequest, "from moet voor to liggen")
		return
	}
	periods, err := s.Rules.RegulatePeriods(r.Context(), id, from, to)
	if err != nil {
		s.Log.Error("rule periods", "err", err)
		writeError(w, http.StatusInternalServerError, "regelperiodes niet geladen")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"periods": periods})
}

// rulePrices returns the current values of the price metrics conditions
// can use (EUR/kWh, EUR/m³, hours).
func (s *Server) rulePrices(w http.ResponseWriter, r *http.Request) {
	out := map[string]float64{}
	if s.Rules != nil {
		out = s.Rules.PricesNow(r.Context())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	if s.Rules == nil {
		writeError(w, http.StatusServiceUnavailable, "regels niet beschikbaar")
		return
	}
	var rule rules.Rule
	if !readJSON(w, r, &rule) {
		return
	}
	rule.ID = 0
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, "regel niet gevonden")
			return
		}
		rule.ID = id
	}
	saved, err := s.Rules.Save(r.Context(), rule)
	switch {
	case errors.Is(err, rules.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "regel niet gevonden")
	case err != nil:
		s.Log.Error("save rule", "err", err)
		writeError(w, http.StatusInternalServerError, "regel niet opgeslagen")
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

// tuneRule changes the target and band of a rule's regulate action without
// saving the rule anew, so a running regulation goes on.
func (s *Server) tuneRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Rules == nil {
		writeError(w, http.StatusNotFound, "regel niet gevonden")
		return
	}
	var body struct {
		Target float64 `json:"target"`
		Band   float64 `json:"band"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	saved, err := s.Rules.Tune(r.Context(), id, body.Target, body.Band)
	switch {
	case errors.Is(err, rules.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "regel niet gevonden")
	case err != nil:
		s.Log.Error("tune rule", "err", err)
		writeError(w, http.StatusInternalServerError, "regel niet opgeslagen")
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

// startRule and stopRule switch a manual action on or off.
func (s *Server) startRule(w http.ResponseWriter, r *http.Request) { s.setRuleActive(w, r, true) }
func (s *Server) stopRule(w http.ResponseWriter, r *http.Request)  { s.setRuleActive(w, r, false) }

func (s *Server) setRuleActive(w http.ResponseWriter, r *http.Request, on bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Rules == nil {
		writeError(w, http.StatusNotFound, "regel niet gevonden")
		return
	}
	set := s.Rules.Stop
	if on {
		set = s.Rules.Start
	}
	v, err := set(r.Context(), id)
	switch {
	case errors.Is(err, rules.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "regel niet gevonden")
	case err != nil:
		s.Log.Error("switch manual action", "err", err)
		writeError(w, http.StatusInternalServerError, "actie niet opgeslagen")
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Rules == nil {
		writeError(w, http.StatusNotFound, "regel niet gevonden")
		return
	}
	switch err := s.Rules.Delete(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "regel niet gevonden")
	case err != nil:
		s.Log.Error("delete rule", "err", err)
		writeError(w, http.StatusInternalServerError, "regel niet verwijderd")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// pushInfo returns the VAPID public key the PWA subscribes with and the
// subscribed apps.
func (s *Server) pushInfo(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "subscriptions": []store.PushSubscription{}})
		return
	}
	subs, err := s.Push.Store.PushSubscriptions(r.Context())
	if err != nil {
		s.Log.Error("push subscriptions", "err", err)
		writeError(w, http.StatusInternalServerError, "abonnementen niet beschikbaar")
		return
	}
	if subs == nil {
		subs = []store.PushSubscription{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true, "public_key": s.Push.Sender.PublicKey(), "subscriptions": subs,
	})
}

// pushSubscription is PushSubscription.toJSON() plus a label for the list.
type pushSubscription struct {
	Endpoint       string   `json:"endpoint"`
	ExpirationTime *float64 `json:"expirationTime"`
	Keys           struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Label string `json:"label"`
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "meldingen niet beschikbaar")
		return
	}
	var sub pushSubscription
	if !readJSON(w, r, &sub) {
		return
	}
	if err := rules.CheckEndpoint(sub.Endpoint); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if sub.Keys.P256DH == "" || sub.Keys.Auth == "" || len(sub.Endpoint) > 2048 || len(sub.Keys.P256DH) > 200 || len(sub.Keys.Auth) > 100 {
		writeError(w, http.StatusBadRequest, "onvolledig pushabonnement")
		return
	}
	label := strings.TrimSpace(sub.Label)
	if label == "" {
		label = "App"
	}
	if len(label) > 80 {
		label = label[:80]
	}
	err := s.Push.Store.SavePushSubscription(r.Context(), store.PushSubscription{
		Endpoint: sub.Endpoint, P256DH: sub.Keys.P256DH, Auth: sub.Keys.Auth, Label: label,
	})
	if err != nil {
		s.Log.Error("save push subscription", "err", err)
		writeError(w, http.StatusInternalServerError, "abonnement niet opgeslagen")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.Push != nil {
		if err := s.Push.Store.DeletePushSubscription(r.Context(), body.Endpoint); err != nil {
			s.Log.Error("delete push subscription", "err", err)
			writeError(w, http.StatusInternalServerError, "abonnement niet verwijderd")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// testPush sends a test message to one app (endpoint given) or to all.
func (s *Server) testPush(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "meldingen niet beschikbaar")
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if r.ContentLength != 0 {
		if err := decodeOne(json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)), &body); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige JSON")
			return
		}
	}
	msg := rules.Message{Title: "Testmelding", Body: "Meldingen van hwlocal komen aan.", Tag: "test", URL: "/#/meldingen"}
	var err error
	if body.Endpoint != "" {
		err = s.Push.NotifyOne(r.Context(), body.Endpoint, msg)
	} else {
		err = s.Push.Notify(r.Context(), msg)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

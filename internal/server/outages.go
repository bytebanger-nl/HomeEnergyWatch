package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
)

// outages serves GET /api/outages?[device=<id>][&days=30]: the times a
// device (HomeWizard or plugin; every device without device) did not
// respond in the last days, newest first, with their number, total
// duration and how many are unread. A stop of the gateway is not in it (see
// package outage).
func (s *Server) outages(w http.ResponseWriter, r *http.Request) {
	if s.Outages == nil {
		writeError(w, http.StatusNotFound, "storingen niet beschikbaar")
		return
	}
	id := r.URL.Query().Get("device")
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 3660 {
			writeError(w, http.StatusBadRequest, "days moet tussen 1 en 3660 liggen")
			return
		}
		days = n
	}
	to := time.Now()
	from := to.AddDate(0, 0, -days)
	list, err := s.Outages.Outages(r.Context(), id, from, to)
	if err != nil {
		s.Log.Error("outages", "err", err)
		writeError(w, http.StatusInternalServerError, "storingen niet geladen")
		return
	}
	var total time.Duration
	unread := 0
	for _, o := range list {
		if !o.Read {
			unread++
		}
		start, end := o.Start, to
		if o.End != nil {
			end = *o.End
		}
		if start.Before(from) {
			start = from
		}
		if end.After(start) {
			total += end.Sub(start)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device": id, "from": from, "to": to,
		"count": len(list), "unread": unread, "total_s": int64(total.Seconds()), "outages": list,
	})
}

// readOutages serves POST /api/outages/read {"device", "until"}: the
// outages of that device (every device without one) that started at or
// before until (the moment of the list the user saw; default now) are
// confirmed as read.
func (s *Server) readOutages(w http.ResponseWriter, r *http.Request) {
	if s.Outages == nil {
		writeError(w, http.StatusNotFound, "storingen niet beschikbaar")
		return
	}
	var body struct {
		Device string     `json:"device"`
		Until  *time.Time `json:"until"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	until := time.Now()
	if body.Until != nil && body.Until.Before(until) {
		until = *body.Until
	}
	n, err := s.Outages.ReadOutages(r.Context(), body.Device, until)
	if err != nil {
		s.Log.Error("read outages", "err", err)
		writeError(w, http.StatusInternalServerError, "storingen niet bijgewerkt")
		return
	}
	if n > 0 && s.Hub != nil {
		s.Hub.Publish(gateway.Event{Type: "outages"}) // other clients update their notice
	}
	writeJSON(w, http.StatusOK, map[string]any{"read": n})
}

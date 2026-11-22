package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
)

// notices serves GET /api/notices[?days=30]: the messages about what went
// wrong (package notice) in the last days, newest first.
func (s *Server) notices(w http.ResponseWriter, r *http.Request) {
	if s.Notices == nil {
		writeError(w, http.StatusNotFound, "berichten niet beschikbaar")
		return
	}
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
	list, err := s.Notices.Notices(r.Context(), to.AddDate(0, 0, -days))
	if err != nil {
		s.Log.Error("notices", "err", err)
		writeError(w, http.StatusInternalServerError, "berichten niet geladen")
		return
	}
	unread := 0
	for _, n := range list {
		if !n.Read {
			unread++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"to": to, "unread": unread, "notices": list})
}

// readNotices serves POST /api/notices/read {"until"}: the notices last seen
// at or before until (the moment of the list the user saw; default now) are
// confirmed as read.
func (s *Server) readNotices(w http.ResponseWriter, r *http.Request) {
	if s.Notices == nil {
		writeError(w, http.StatusNotFound, "berichten niet beschikbaar")
		return
	}
	var body struct {
		Until *time.Time `json:"until"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	until := time.Now()
	if body.Until != nil && body.Until.Before(until) {
		until = *body.Until
	}
	n, err := s.Notices.ReadNotices(r.Context(), until)
	if err != nil {
		s.Log.Error("read notices", "err", err)
		writeError(w, http.StatusInternalServerError, "berichten niet bijgewerkt")
		return
	}
	if n > 0 && s.Hub != nil {
		s.Hub.Publish(gateway.Event{Type: "notices"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"read": n})
}

// inbox serves GET /api/inbox: what is unread, for the icon in the PWA's
// header: the unread outages per device and the unread notices.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"unread": 0, "outages": 0, "notices": 0, "devices": []any{}}
	total := 0
	if s.Outages != nil {
		list, err := s.Outages.UnreadOutages(r.Context())
		if err != nil {
			s.Log.Error("inbox outages", "err", err)
			writeError(w, http.StatusInternalServerError, "berichten niet geladen")
			return
		}
		n := 0
		for _, d := range list {
			n += d.Unread
		}
		out["outages"], out["devices"] = n, list
		total += n
	}
	if s.Notices != nil {
		n, err := s.Notices.UnreadNotices(r.Context())
		if err != nil {
			s.Log.Error("inbox notices", "err", err)
			writeError(w, http.StatusInternalServerError, "berichten niet geladen")
			return
		}
		out["notices"] = n
		total += n
	}
	out["unread"] = total
	writeJSON(w, http.StatusOK, out)
}

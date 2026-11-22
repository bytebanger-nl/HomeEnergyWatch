package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Heating from mijn.ista.nl (package ista). Every call takes an optional
// ?cuid= (the address); without it the first address is used.
func (s *Server) routeIsta(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ista", s.istaOverview)
	mux.HandleFunc("POST /api/ista/sync", s.istaSync)
	mux.HandleFunc("GET /api/ista/usage", s.istaUsage)
	mux.HandleFunc("GET /api/ista/weeks", s.istaWeeks)
	mux.HandleFunc("GET /api/ista/costs", s.istaCosts)
	mux.HandleFunc("GET /api/ista/contracts", s.istaContracts)
	mux.HandleFunc("PUT /api/ista/contracts/{y}", s.istaSaveContract)
	mux.HandleFunc("DELETE /api/ista/contracts/{y}", s.istaDeleteContract)
}

func (s *Server) istaReady(w http.ResponseWriter) bool {
	if s.Ista == nil {
		writeError(w, http.StatusNotFound, "ista is niet beschikbaar")
		return false
	}
	return true
}

func (s *Server) istaError(w http.ResponseWriter, err error) {
	var inv ista.ErrInvalid
	switch {
	case errors.Is(err, ista.ErrNoAddress), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "geen ista-gegevens gevonden")
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, inv.Msg)
	default:
		s.Log.Error("ista", "err", err)
		writeError(w, http.StatusInternalServerError, "ista-gegevens konden niet worden gelezen")
	}
}

func (s *Server) istaOverview(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	addrs, err := s.Ista.Addresses(r.Context())
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": s.Ista.Statuses(r.Context()), "addresses": addrs})
}

func (s *Server) istaSync(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	s.Ista.Wake()
	w.WriteHeader(http.StatusAccepted)
}

// wakeIsta makes the ista sync look at the accounts now, after a plugin
// was added or changed (new credentials, enabled), instead of within a
// minute.
func (s *Server) wakeIsta() {
	if s.Ista != nil {
		s.Ista.Wake()
	}
}

// istaDay reads a query parameter as a day ("YYYY-MM-DD").
func istaDay(r *http.Request, key string) (time.Time, bool) {
	d, err := time.Parse(ista.DayLayout, r.URL.Query().Get(key))
	return d, err == nil
}

// istaRange reads ?from=, ?to= and ?bucket= (day when absent).
func istaRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, bucket string, ok bool) {
	from, ok1 := istaDay(r, "from")
	to, ok2 := istaDay(r, "to")
	if !ok1 || !ok2 || to.Before(from) {
		writeError(w, http.StatusBadRequest, "from en to moeten datums zijn (JJJJ-MM-DD), from niet na to")
		return
	}
	if to.Sub(from) > 3*366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "hooguit drie jaar per keer")
		return
	}
	bucket = r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = ista.BucketDay
	}
	switch bucket {
	case ista.BucketDay, ista.BucketWeek, ista.BucketMonth, ista.BucketPeriod:
	default:
		writeError(w, http.StatusBadRequest, "bucket moet day, week, month of period zijn")
		return
	}
	return from, to, bucket, true
}

func (s *Server) istaCostRange(w http.ResponseWriter, r *http.Request) {
	from, to, bucket, ok := istaRange(w, r)
	if !ok {
		return
	}
	c, err := s.Ista.CostRange(r.Context(), r.URL.Query().Get("cuid"), from, to, bucket)
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) istaUsage(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	from, to, bucket, ok := istaRange(w, r)
	if !ok {
		return
	}
	u, err := s.Ista.Usage(r.Context(), r.URL.Query().Get("cuid"), from, to, bucket)
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// istaYear reads ?y= (0 or absent: the running period).
func istaYear(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	y, err := strconv.Atoi(raw)
	if err != nil || y < 0 || y > 9999 {
		writeError(w, http.StatusBadRequest, "ongeldige periode")
		return 0, false
	}
	return y, true
}

func (s *Server) istaWeeks(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	y, ok := istaYear(w, r.URL.Query().Get("y"))
	if !ok {
		return
	}
	wk, err := s.Ista.Weeks(r.Context(), r.URL.Query().Get("cuid"), y)
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// istaCosts answers where a settlement period stands (?y=, 0 or absent:
// the running one), or with ?from=&to=&bucket= the costs per bucket.
func (s *Server) istaCosts(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	if r.URL.Query().Has("from") {
		s.istaCostRange(w, r)
		return
	}
	y, ok := istaYear(w, r.URL.Query().Get("y"))
	if !ok {
		return
	}
	c, err := s.Ista.Costs(r.Context(), r.URL.Query().Get("cuid"), y)
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) istaContracts(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	cuid, list, err := s.Ista.Contracts(r.Context(), r.URL.Query().Get("cuid"))
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cuid": cuid, "contracts": list})
}

func (s *Server) istaSaveContract(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	y, ok := istaYear(w, r.PathValue("y"))
	if !ok || y == 0 {
		if ok {
			writeError(w, http.StatusBadRequest, "ongeldige periode")
		}
		return
	}
	var c ista.Contract
	if !readJSON(w, r, &c) {
		return
	}
	c.Y = y
	saved, err := s.Ista.SaveContract(r.Context(), r.URL.Query().Get("cuid"), c)
	if err != nil {
		s.istaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) istaDeleteContract(w http.ResponseWriter, r *http.Request) {
	if !s.istaReady(w) {
		return
	}
	y, ok := istaYear(w, r.PathValue("y"))
	if !ok {
		return
	}
	if err := s.Ista.DeleteContract(r.Context(), r.URL.Query().Get("cuid"), y); err != nil {
		s.istaError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func (s *Server) listScenarios(w http.ResponseWriter, r *http.Request) {
	if s.Scenarios == nil {
		writeJSON(w, http.StatusOK, map[string]any{"scenarios": []costs.Scenario{}})
		return
	}
	list, err := s.Scenarios.List(r.Context())
	if err != nil {
		s.Log.Error("scenarios", "err", err)
		writeError(w, http.StatusInternalServerError, "scenario's niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenarios": list})
}

func (s *Server) saveScenario(w http.ResponseWriter, r *http.Request) {
	if s.Scenarios == nil {
		writeError(w, http.StatusServiceUnavailable, "scenario's niet beschikbaar")
		return
	}
	var sc costs.Scenario
	if !readJSON(w, r, &sc) {
		return
	}
	sc.ID = 0
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, "scenario niet gevonden")
			return
		}
		sc.ID = id
	}
	saved, err := s.Scenarios.Save(r.Context(), sc)
	switch {
	case errors.Is(err, costs.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "scenario niet gevonden")
	case err != nil:
		s.Log.Error("save scenario", "err", err)
		writeError(w, http.StatusInternalServerError, "scenario niet opgeslagen")
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

func (s *Server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Scenarios == nil {
		writeError(w, http.StatusNotFound, "scenario niet gevonden")
		return
	}
	switch err := s.Scenarios.Delete(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "scenario niet gevonden")
	case err != nil:
		s.Log.Error("delete scenario", "err", err)
		writeError(w, http.StatusInternalServerError, "scenario niet verwijderd")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// compareCosts returns the actual costs and those of every scenario:
// ?device (default the P1 meter)&from&to&bucket=day|month|year (default month).
func (s *Server) compareCosts(w http.ResponseWriter, r *http.Request) {
	if s.Costs == nil || s.Scenarios == nil {
		writeError(w, http.StatusServiceUnavailable, "vergelijken niet beschikbaar")
		return
	}
	q := r.URL.Query()
	id, ok := s.costDevice(w, q.Get("device"), s.energy())
	if !ok {
		return
	}
	from, err := time.Parse(time.RFC3339, q.Get("from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "ongeldige 'from' (RFC 3339 verwacht)")
		return
	}
	to, err := time.Parse(time.RFC3339, q.Get("to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "ongeldige 'to' (RFC 3339 verwacht)")
		return
	}
	if !to.After(from) || to.Sub(from) > 3*366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "ongeldige periode (maximaal drie jaar)")
		return
	}
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = costs.BucketMonth
	}
	list, err := s.Scenarios.List(r.Context())
	if err != nil {
		s.Log.Error("scenarios", "err", err)
		writeError(w, http.StatusInternalServerError, "scenario's niet beschikbaar")
		return
	}
	cmp, err := s.Costs.Compare(r.Context(), id, list, from, to, bucket)
	if err != nil {
		s.Log.Error("compare", "err", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": id, "comparison": cmp})
}

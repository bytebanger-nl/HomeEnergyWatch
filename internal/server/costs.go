package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// timeline is one set of contracts with its calculator and the kind of
// device whose quarter hours it prices: energy (the P1 meter) or water.
type timeline struct {
	contracts *costs.Contracts
	calc      *costs.Calculator
	kind      homewizard.Kind
	missing   string // error when there is no such device
}

func (s *Server) energy() timeline {
	return timeline{s.Contracts, s.Costs, homewizard.KindP1, "geen P1-meter gevonden"}
}

func (s *Server) water() timeline {
	return timeline{s.WaterContracts, s.WaterCosts, homewizard.KindWater, "geen watermeter gevonden"}
}

// routeCosts registers the contract, cost and advance endpoints of one
// timeline under prefix (/api for energy, /api/water for water).
func (s *Server) routeCosts(mux *http.ServeMux, prefix string, tl func() timeline) {
	mux.HandleFunc("GET "+prefix+"/contracts", func(w http.ResponseWriter, r *http.Request) { s.listContracts(w, r, tl()) })
	mux.HandleFunc("POST "+prefix+"/contracts", func(w http.ResponseWriter, r *http.Request) { s.saveContract(w, r, tl()) })
	mux.HandleFunc("PUT "+prefix+"/contracts/{id}", func(w http.ResponseWriter, r *http.Request) { s.saveContract(w, r, tl()) })
	mux.HandleFunc("DELETE "+prefix+"/contracts/{id}", func(w http.ResponseWriter, r *http.Request) { s.deleteContract(w, r, tl()) })
	mux.HandleFunc("GET "+prefix+"/costs", func(w http.ResponseWriter, r *http.Request) { s.costHistory(w, r, tl()) })
	mux.HandleFunc("GET "+prefix+"/costs/gaps", func(w http.ResponseWriter, r *http.Request) { s.costHistory(w, r, tl()) })
	mux.HandleFunc("GET "+prefix+"/advance", func(w http.ResponseWriter, r *http.Request) { s.advanceStatus(w, r, tl()) })
}

func (s *Server) listContracts(w http.ResponseWriter, r *http.Request, tl timeline) {
	if tl.contracts == nil {
		writeJSON(w, http.StatusOK, map[string]any{"contracts": []costs.Contract{}})
		return
	}
	list, err := tl.contracts.List(r.Context())
	if err != nil {
		s.Log.Error("contracts", "err", err)
		writeError(w, http.StatusInternalServerError, "contracten niet beschikbaar")
		return
	}
	if list == nil {
		list = costs.Timeline{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"contracts": list})
}

func (s *Server) saveContract(w http.ResponseWriter, r *http.Request, tl timeline) {
	if tl.contracts == nil {
		writeError(w, http.StatusServiceUnavailable, "contracten niet beschikbaar")
		return
	}
	var c costs.Contract
	if !readJSON(w, r, &c) {
		return
	}
	c.ID = 0
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, "contract niet gevonden")
			return
		}
		c.ID = id
	}
	saved, err := tl.contracts.Save(r.Context(), c)
	switch {
	case errors.Is(err, costs.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "contract niet gevonden")
	case err != nil:
		s.Log.Error("save contract", "err", err)
		writeError(w, http.StatusInternalServerError, "contract niet opgeslagen")
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

func (s *Server) deleteContract(w http.ResponseWriter, r *http.Request, tl timeline) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || tl.contracts == nil {
		writeError(w, http.StatusNotFound, "contract niet gevonden")
		return
	}
	switch err := tl.contracts.Delete(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "contract niet gevonden")
	case err != nil:
		s.Log.Error("delete contract", "err", err)
		writeError(w, http.StatusInternalServerError, "contract niet verwijderd")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// costDevice resolves ?device, by default the first device of the
// timeline's kind (P1 meter or water meter); it writes the error response
// when there is none.
func (s *Server) costDevice(w http.ResponseWriter, id string, tl timeline) (string, bool) {
	if id == "" {
		for _, d := range s.Manager.Views() {
			if d.Kind == tl.kind {
				return d.ID, true
			}
		}
		writeError(w, http.StatusNotFound, tl.missing)
		return "", false
	}
	if _, ok := s.Manager.View(id); !ok {
		writeError(w, http.StatusNotFound, "apparaat niet gevonden")
		return "", false
	}
	return id, true
}

// advanceStatus returns the advance payments of the current contract
// against its costs, with a forecast for the final bill (?device, default
// the P1 meter, or the water meter under /api/water); {"advance": null,
// "advances": []} when the contract has no advance. "advances" holds one
// status per part when the contract splits electricity and gas (else the
// one status), "advance" the first of them.
func (s *Server) advanceStatus(w http.ResponseWriter, r *http.Request, tl timeline) {
	if tl.calc == nil {
		writeError(w, http.StatusServiceUnavailable, "kosten niet beschikbaar")
		return
	}
	id, ok := s.costDevice(w, r.URL.Query().Get("device"), tl)
	if !ok {
		return
	}
	all, err := tl.calc.Advances(r.Context(), id)
	if err != nil {
		s.Log.Error("advance", "err", err)
		writeError(w, http.StatusInternalServerError, "voorschot niet beschikbaar")
		return
	}
	var first *costs.AdvanceStatus
	if len(all) > 0 {
		first = all[0]
	} else {
		all = []*costs.AdvanceStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": id, "advance": first, "advances": all})
}

// costHistory returns costs per day, month or year: ?device (default: the
// first P1 meter, or water meter under /api/water)&from&to&bucket=day|month|year.
// With scope=usage (energy only) ?device must be a socket or kWh meter and
// only the variable cost of its electricity import is returned.
func (s *Server) costHistory(w http.ResponseWriter, r *http.Request, tl timeline) {
	if tl.calc == nil {
		writeError(w, http.StatusServiceUnavailable, "kosten niet beschikbaar")
		return
	}
	q := r.URL.Query()
	usage := false
	switch q.Get("scope") {
	case "":
	case "usage":
		if tl.kind != homewizard.KindP1 {
			writeError(w, http.StatusBadRequest, "verbruikskosten alleen voor stroom")
			return
		}
		usage = true
	default:
		writeError(w, http.StatusBadRequest, "onbekende 'scope' (usage)")
		return
	}
	id, ok := s.costDevice(w, q.Get("device"), tl)
	if !ok {
		return
	}
	var from, to time.Time
	var err error
	if from, err = time.Parse(time.RFC3339, q.Get("from")); err != nil {
		writeError(w, http.StatusBadRequest, "ongeldige 'from' (RFC 3339 verwacht)")
		return
	}
	if to, err = time.Parse(time.RFC3339, q.Get("to")); err != nil {
		writeError(w, http.StatusBadRequest, "ongeldige 'to' (RFC 3339 verwacht)")
		return
	}
	if !to.After(from) || to.Sub(from) > 20*366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "ongeldige periode")
		return
	}
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = costs.BucketDay
	}
	if strings.HasSuffix(r.URL.Path, "/gaps") {
		// where the quarters that the costs report as missing are
		if usage {
			if v, _ := s.Manager.View(id); v.Kind != homewizard.KindSocket && v.Kind != homewizard.KindKWH {
				writeError(w, http.StatusBadRequest, "verbruikskosten alleen voor een tussenstekker of kWh-meter")
				return
			}
		}
		gaps, err := tl.calc.Gaps(r.Context(), id, from, to, usage)
		if err != nil {
			s.Log.Error("cost gaps", "err", err)
			writeError(w, http.StatusInternalServerError, "niet beschikbaar")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"device": id, "result": gaps})
		return
	}
	var res *costs.Result
	if usage {
		if v, _ := s.Manager.View(id); v.Kind != homewizard.KindSocket && v.Kind != homewizard.KindKWH {
			writeError(w, http.StatusBadRequest, "verbruikskosten alleen voor een tussenstekker of kWh-meter")
			return
		}
		res, err = tl.calc.Usage(r.Context(), id, from, to, bucket)
	} else {
		res, err = tl.calc.Calculate(r.Context(), id, from, to, bucket)
	}
	if err != nil {
		s.Log.Error("costs", "err", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if res.Buckets == nil {
		res.Buckets = []costs.Bucket{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": id, "result": res})
}

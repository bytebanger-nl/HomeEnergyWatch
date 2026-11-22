package server

import (
	"context"
	"net/http"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
)

// forecastUse is what the forecast of one timeline uses now: its
// percentages, or why it has none.
type forecastUse struct {
	Percentages []forecast.Used `json:"percentages"`
	Error       string          `json:"error,omitempty"`
}

// getForecastSettings returns the forecast settings (the user's own
// values), the defaults, and per timeline (electricity, water, heating)
// the percentages its running forecast uses, with their automatic values.
func (s *Server) getForecastSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Forecast.Load(r.Context())
	if err != nil {
		s.Log.Error("forecast settings", "err", err)
		writeError(w, http.StatusInternalServerError, "instellingen niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": set, "defaults": forecast.Defaults, "used": s.forecastUses(r.Context())})
}

func (s *Server) putForecastSettings(w http.ResponseWriter, r *http.Request) {
	if s.Forecast == nil {
		writeError(w, http.StatusServiceUnavailable, "niet beschikbaar")
		return
	}
	var set forecast.Settings
	if !readJSON(w, r, &set) {
		return
	}
	if err := s.Forecast.Save(r.Context(), set); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Info("prognose-instellingen gewijzigd")
	s.getForecastSettings(w, r)
}

// forecastUses collects the percentages of the running forecasts: of the
// first P1 meter and water meter with an advance, and of the first ista
// address. A timeline without a running forecast is left out.
func (s *Server) forecastUses(ctx context.Context) map[string]forecastUse {
	out := map[string]forecastUse{}
	for name, tl := range map[string]timeline{"electricity": s.energy(), "water": s.water()} {
		if tl.calc == nil || s.Manager == nil {
			continue
		}
		for _, d := range s.Manager.Views() {
			if d.Kind != tl.kind {
				continue
			}
			all, err := tl.calc.Advances(ctx, d.ID)
			if err != nil || len(all) == 0 {
				continue
			}
			out[name] = useOf(all)
			break
		}
	}
	if s.Ista != nil {
		if addrs, err := s.Ista.Addresses(ctx); err == nil && len(addrs) > 0 {
			if c, err := s.Ista.Costs(ctx, addrs[0].Cuid, 0); err == nil && (len(c.Percentages) > 0 || c.ForecastError != "") {
				out["heating"] = forecastUse{Percentages: c.Percentages, Error: c.ForecastError}
			}
		}
	}
	return out
}

// useOf merges the percentages of the parts of an advance status.
func useOf(all []*costs.AdvanceStatus) forecastUse {
	var u forecastUse
	seen := map[string]bool{}
	for _, st := range all {
		if st.Forecast == nil {
			if u.Error == "" {
				u.Error = st.ForecastError
			}
			continue
		}
		for _, p := range st.Forecast.Method.Percentages {
			if !seen[p.Key] {
				seen[p.Key] = true
				u.Percentages = append(u.Percentages, p)
			}
		}
	}
	if u.Percentages == nil {
		u.Percentages = []forecast.Used{}
	}
	return u
}

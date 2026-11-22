package server

import (
	"context"
	"net/http"
)

// TokenSetting is an API token the PWA may set or clear but never read.
type TokenSetting interface {
	Status() TokenStatus
	// Set stores the token ("" removes it) and starts using it. A warning
	// is returned when the token could not be checked.
	Set(ctx context.Context, token string) (warning string, err error)
}

// TokenStatus tells the PWA about a token without revealing it.
type TokenStatus struct {
	Configured bool `json:"configured"`
	// FromEnv: the token comes from an environment variable and cannot be
	// changed here.
	FromEnv bool `json:"from_env"`
	// Used: the source is in the configured source list.
	Used    bool   `json:"used"`
	Warning string `json:"warning,omitempty"`
}

func (s *Server) getEntsoe(w http.ResponseWriter, r *http.Request) {
	if s.Entsoe == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, s.Entsoe.Status())
}

func (s *Server) putEntsoe(w http.ResponseWriter, r *http.Request) {
	if s.Entsoe == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	warning, err := s.Entsoe.Set(r.Context(), body.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st := s.Entsoe.Status()
	st.Warning = warning
	s.Log.Info("ENTSO-E-token gewijzigd", "configured", st.Configured)
	writeJSON(w, http.StatusOK, st)
}

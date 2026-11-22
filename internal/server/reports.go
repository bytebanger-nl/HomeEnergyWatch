package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
	"github.com/bytebanger-nl/homeenergywatch/internal/report"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func (s *Server) routeReports(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/reports", s.listReports)
	mux.HandleFunc("GET /api/reports/pdf", s.reportPDF)
	mux.HandleFunc("POST /api/reports/schedules", s.saveSchedule)
	mux.HandleFunc("PUT /api/reports/schedules/{id}", s.saveSchedule)
	mux.HandleFunc("DELETE /api/reports/schedules/{id}", s.deleteSchedule)
	mux.HandleFunc("POST /api/reports/schedules/{id}/send", s.sendSchedule)
	mux.HandleFunc("GET /api/settings/mail", s.getMail)
	mux.HandleFunc("PUT /api/settings/mail", s.putMail)
	mux.HandleFunc("POST /api/settings/mail/test", s.testMail)
}

func (s *Server) reportsReady(w http.ResponseWriter) bool {
	if s.Reports == nil || s.Reports.Builder == nil {
		writeError(w, http.StatusServiceUnavailable, "rapporten niet beschikbaar")
		return false
	}
	return true
}

// listReports returns the sections a report can hold, the scheduled reports
// and whether e-mail is set up.
func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	list, err := s.Reports.List(r.Context())
	if err != nil {
		s.Log.Error("reports", "err", err)
		writeError(w, http.StatusInternalServerError, "rapporten niet beschikbaar")
		return
	}
	ms, err := report.MailSettings(r.Context(), s.Reports.Store)
	if err != nil {
		s.Log.Error("mail settings", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sections": report.Sections, "schedules": list, "mail_configured": ms.Configured(),
		"max_recipients": report.MaxRecipients,
	})
}

// reportPDF makes a report: ?from=YYYY-MM-DD&to=YYYY-MM-DD (the last day,
// inclusive)&sections=a,b&title=.
func (s *Server) reportPDF(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	q := r.URL.Query()
	loc := s.Reports.Builder.Loc
	if loc == nil {
		loc = time.Local
	}
	var req report.Request
	if rng := q.Get("range"); rng != "" {
		// up to and including today, from the start of the contract year or 1 January
		if rng != report.RangeContractYear && rng != report.RangeYear {
			writeError(w, http.StatusBadRequest, "onbekende 'range' (contract_year of year)")
			return
		}
		now := time.Now().In(loc)
		req.To = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
		from, err := s.Reports.Builder.RangeStart(r.Context(), rng, req.To, req.To)
		if err != nil {
			s.Log.Error("report range", "err", err)
			writeError(w, http.StatusInternalServerError, "rapport niet gemaakt")
			return
		}
		req.From = from
	} else {
		from, err := time.ParseInLocation("2006-01-02", q.Get("from"), loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'from' (JJJJ-MM-DD)")
			return
		}
		last, err := time.ParseInLocation("2006-01-02", q.Get("to"), loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'to' (JJJJ-MM-DD)")
			return
		}
		req.From, req.To = from, last.AddDate(0, 0, 1)
	}
	req.Title = q.Get("title")
	if v := q.Get("sections"); v != "" {
		req.Sections = strings.Split(v, ",")
	}
	rep, err := s.Reports.Builder.Build(r.Context(), req)
	if errors.Is(err, report.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.Log.Error("report", "err", err)
		writeError(w, http.StatusInternalServerError, "rapport niet gemaakt")
		return
	}
	pdf, err := rep.Bytes()
	if err != nil {
		s.Log.Error("report pdf", "err", err)
		writeError(w, http.StatusInternalServerError, "rapport niet gemaakt")
		return
	}
	disp := "attachment"
	if q.Get("inline") == "1" {
		disp = "inline"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disp, report.FileName(rep.From, rep.To)))
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pdf)
}

func scheduleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "rapport niet gevonden")
		return 0, false
	}
	return id, true
}

func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	var sc report.Schedule
	if !readJSON(w, r, &sc) {
		return
	}
	sc.ID = 0
	if r.PathValue("id") != "" {
		id, ok := scheduleID(w, r)
		if !ok {
			return
		}
		sc.ID = id
	}
	saved, err := s.Reports.Save(r.Context(), sc)
	switch {
	case errors.Is(err, report.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "rapport niet gevonden")
	case err != nil:
		s.Log.Error("save report schedule", "err", err)
		writeError(w, http.StatusInternalServerError, "rapport niet opgeslagen")
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	switch err := s.Reports.Delete(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "rapport niet gevonden")
	case err != nil:
		s.Log.Error("delete report schedule", "err", err)
		writeError(w, http.StatusInternalServerError, "rapport niet verwijderd")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// sendSchedule mails a schedule's latest period now.
func (s *Server) sendSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	err := s.Reports.SendNow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "rapport niet gevonden")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "niet verstuurd: "+err.Error())
		return
	}
	sc, err := s.Reports.Get(r.Context(), id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// mailView is the e-mail settings without the password.
type mailView struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	User        string `json:"user"`
	From        string `json:"from"`
	HasPassword bool   `json:"has_password"`
	Configured  bool   `json:"configured"`
}

func viewOf(m mail.Settings) mailView {
	return mailView{Host: m.Host, Port: m.Port, Security: m.Security, User: m.User, From: m.From,
		HasPassword: m.Password != "", Configured: m.Configured()}
}

func (s *Server) getMail(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	m, err := report.MailSettings(r.Context(), s.Reports.Store)
	if err != nil {
		s.Log.Error("mail settings", "err", err)
		writeError(w, http.StatusInternalServerError, "e-mailinstellingen niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, viewOf(m))
}

// putMail stores the e-mail settings. The password is never sent back: an
// empty password keeps the stored one (as long as the server and user stay
// the same), clear_password removes it.
func (s *Server) putMail(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	var body struct {
		mail.Settings
		ClearPassword bool `json:"clear_password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	old, err := report.MailSettings(r.Context(), s.Reports.Store)
	if err != nil {
		s.Log.Error("mail settings", "err", err)
		writeError(w, http.StatusInternalServerError, "e-mailinstellingen niet beschikbaar")
		return
	}
	m := body.Settings
	m.Normalize()
	if m.Password == "" && !body.ClearPassword && m.Host == old.Host && m.User == old.User {
		m.Password = old.Password
	}
	if body.ClearPassword {
		m.Password = ""
	}
	if err := report.SaveMailSettings(r.Context(), s.Reports.Store, m); err != nil {
		if errors.Is(err, mail.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Log.Error("save mail settings", "err", err)
		writeError(w, http.StatusInternalServerError, "e-mailinstellingen niet opgeslagen")
		return
	}
	m, _ = report.MailSettings(r.Context(), s.Reports.Store)
	s.Log.Info("e-mailinstellingen gewijzigd", "configured", m.Configured(), "host", m.Host)
	writeJSON(w, http.StatusOK, viewOf(m))
}

// testMail sends a test message with the stored settings to {"to": ...}.
func (s *Server) testMail(w http.ResponseWriter, r *http.Request) {
	if !s.reportsReady(w) {
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	a, err := mail.ParseAddress(body.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := report.MailSettings(r.Context(), s.Reports.Store)
	if err != nil {
		s.Log.Error("mail settings", "err", err)
		writeError(w, http.StatusInternalServerError, "e-mailinstellingen niet beschikbaar")
		return
	}
	if err := report.TestMail(r.Context(), s.Reports.Sender, m, a.Address); err != nil {
		if errors.Is(err, report.ErrNoMail) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "niet verstuurd: "+err.Error())
		return
	}
	s.Log.Info("testmail verstuurd")
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

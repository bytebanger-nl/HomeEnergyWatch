package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/report"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func reportServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s := newServer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s.Reports = &report.Scheduler{Store: st, Sender: &mail.Sender{}, Log: s.Log, Builder: &report.Builder{
		Store: st, Costs: &costs.Calculator{Store: st, Contracts: &costs.Contracts{Store: st}, Loc: prices.Amsterdam},
		Loc: prices.Amsterdam, Devices: func() []report.Device { return nil },
	}}
	return s, st
}

func TestReportPDF(t *testing.T) {
	s, _ := reportServer(t)
	h := s.Handler()
	lan := "192.168.1.20:5000"
	w := do(h, "GET", "/api/reports", lan, "", nil)
	var info struct {
		Sections       []report.SectionInfo `json:"sections"`
		Schedules      []report.Schedule    `json:"schedules"`
		MailConfigured bool                 `json:"mail_configured"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || len(info.Sections) != len(report.Sections) || info.Schedules == nil || info.MailConfigured {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	w = do(h, "GET", "/api/reports/pdf?from=2026-10-01&to=2026-10-31&sections=summary,costs", lan, "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("pdf: %d %s %.80q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="rapport-2026-10.pdf"` {
		t.Fatalf("disposition %q", cd)
	}
	for _, q := range []string{"from=2026-10&to=2026-10-31", "from=2026-10-31&to=2026-10-01", "from=2026-10-01&to=2026-10-31&sections=x", "from=2099-01-01&to=2099-01-31"} {
		if w := do(h, "GET", "/api/reports/pdf?"+q, lan, "", nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
}

func TestMailSettingsAPI(t *testing.T) {
	s, _ := reportServer(t)
	h := s.Handler()
	lan := "192.168.1.20:5000"
	hdr := map[string]string{"Content-Type": "application/json"}
	smtp := &mail.SimServer{User: "u", Password: "geheim"}
	addr, err := smtp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer smtp.Close()
	host, port, _ := net.SplitHostPort(addr)

	if w := do(h, "POST", "/api/settings/mail/test", lan, `{"to":"a@example.nl"}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("test without settings: %d", w.Code)
	}
	body := fmt.Sprintf(`{"host":%q,"port":%s,"security":"none","user":"u","password":"geheim","from":"gw@example.nl"}`, host, port)
	w := do(h, "PUT", "/api/settings/mail", lan, body, hdr)
	if w.Code != 200 || strings.Contains(w.Body.String(), "geheim") || !strings.Contains(w.Body.String(), `"has_password":true`) {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	// saved again without a password: the stored one stays
	body = fmt.Sprintf(`{"host":%q,"port":%s,"security":"none","user":"u","from":"Thuis <gw@example.nl>"}`, host, port)
	if w := do(h, "PUT", "/api/settings/mail", lan, body, hdr); w.Code != 200 || !strings.Contains(w.Body.String(), `"has_password":true`) {
		t.Fatalf("keep password: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/settings/mail", lan, "", nil); w.Code != 200 || strings.Contains(w.Body.String(), "geheim") {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/settings/mail/test", lan, `{"to":"a@example.nl"}`, hdr); w.Code != 200 {
		t.Fatalf("test mail: %d %s", w.Code, w.Body)
	}
	if m := smtp.Messages(); len(m) != 1 || m[0].Auth != "u" || m[0].To[0] != "a@example.nl" {
		t.Fatalf("received %+v", m)
	}
	if w := do(h, "PUT", "/api/settings/mail", lan, `{"host":"smtp.example.nl:587","from":"gw@example.nl"}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("bad host: %d", w.Code)
	}
	if w := do(h, "PUT", "/api/settings/mail", lan, `{"host":"","clear_password":true}`, hdr); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
}

func TestScheduleAPI(t *testing.T) {
	s, _ := reportServer(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, prices.Amsterdam)
	s.Reports.Now = func() time.Time { return now }
	h := s.Handler()
	lan := "192.168.1.20:5000"
	hdr := map[string]string{"Content-Type": "application/json"}
	w := do(h, "POST", "/api/reports/schedules", lan, `{"kind":"week","sections":["summary"],"to":["a@example.nl"],"enabled":true}`, hdr)
	var sc report.Schedule
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sc) != nil || sc.ID == 0 || sc.Name != "Weekrapport" || sc.NextAt == nil {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	sc.Name = "Week"
	b, _ := json.Marshal(sc)
	if w := do(h, "PUT", fmt.Sprintf("/api/reports/schedules/%d", sc.ID), lan, string(b), hdr); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Week"`) {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/reports/schedules", lan, `{"kind":"week","sections":["summary"],"to":["nee"]}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("bad address: %d", w.Code)
	}
	if w := do(h, "PUT", "/api/reports/schedules/999", lan, string(b), hdr); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", w.Code)
	}
	// no mail server: sending fails with the reason
	if w := do(h, "POST", fmt.Sprintf("/api/reports/schedules/%d/send", sc.ID), lan, "", nil); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "niet ingesteld") {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	if w := do(h, "DELETE", fmt.Sprintf("/api/reports/schedules/%d", sc.ID), lan, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(h, "DELETE", fmt.Sprintf("/api/reports/schedules/%d", sc.ID), lan, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", w.Code)
	}
}

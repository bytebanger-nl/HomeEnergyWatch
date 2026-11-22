package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/rules"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
	"github.com/bytebanger-nl/homeenergywatch/internal/webpush"
)

func withRules(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s := newServer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	key, err := webpush.LoadOrCreateKey(filepath.Join(t.TempDir(), "vapid.pem"))
	if err != nil {
		t.Fatal(err)
	}
	s.Push = &rules.Push{Sender: &webpush.Sender{Key: key, Subject: "mailto:t@example.com"}, Store: st, Log: log}
	s.Rules = &rules.Engine{Store: st, Devices: s.Manager, Log: log}
	return s, st
}

func TestRulesAPI(t *testing.T) {
	s, _ := withRules(t)
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	lan := "192.168.1.20:5000"

	body := `{"name":"Droger klaar","enabled":true,"for_s":180,
		"conditions":[{"device":"dryer","metric":"power_w","op":"<","value":5}],
		"actions":[{"type":"notify"}]}`
	w := do(h, "POST", "/api/rules", lan, body, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var v rules.View
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.ID == 0 || v.State.Armed {
		t.Fatalf("created %+v (%v)", v, err)
	}
	if w := do(h, "POST", "/api/rules", lan, `{"name":"x","conditions":[],"actions":[]}`, hdr); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "voorwaarde") {
		t.Fatalf("invalid: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/api/rules/999", lan, body, hdr); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
	w = do(h, "GET", "/api/rules", lan, "", nil)
	var list struct{ Rules []rules.View }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Rules) != 1 || list.Rules[0].ForSeconds != 180 {
		t.Fatalf("list %s", w.Body)
	}
	if w := do(h, "DELETE", "/api/rules/"+itoa(v.ID), lan, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(h, "POST", "/api/rules", "8.8.8.8:5000", body, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("from outside the LAN: %d", w.Code)
	}
}

func TestPushSubscriptionAPI(t *testing.T) {
	s, st := withRules(t)
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	lan := "192.168.1.20:5000"

	w := do(h, "GET", "/api/push", lan, "", nil)
	var info struct {
		Available bool   `json:"available"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || !info.Available || len(info.PublicKey) != 87 {
		t.Fatalf("info %s", w.Body)
	}
	sub := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc","expirationTime":null,"keys":{"p256dh":"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4","auth":"BTBZMqHH6r4Tts7J_aSIgg"},"label":"Android · Chrome"}`
	if w := do(h, "POST", "/api/push/subscriptions", lan, sub, hdr); w.Code != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", w.Code, w.Body)
	}
	// the gateway must not be made to post to the LAN
	for _, ep := range []string{"http://fcm.googleapis.com/x", "https://192.168.1.1/x", "https://localhost/x", "https://nas/x", "https://printer.local/x", "https://[fd00::1]/x"} {
		bad := strings.Replace(sub, "https://fcm.googleapis.com/fcm/send/abc", ep, 1)
		if w := do(h, "POST", "/api/push/subscriptions", lan, bad, hdr); w.Code != http.StatusBadRequest {
			t.Errorf("endpoint %s: %d", ep, w.Code)
		}
	}
	subs, err := st.PushSubscriptions(t.Context())
	if err != nil || len(subs) != 1 || subs[0].Label != "Android · Chrome" || subs[0].Auth != "BTBZMqHH6r4Tts7J_aSIgg" {
		t.Fatalf("stored %+v (%v)", subs, err)
	}
	// the keys are never served
	if w := do(h, "GET", "/api/push", lan, "", nil); strings.Contains(w.Body.String(), "BTBZMqHH6r4Tts7J_aSIgg") {
		t.Fatalf("auth secret served: %s", w.Body)
	}
	if w := do(h, "DELETE", "/api/push/subscriptions", lan, `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc"}`, hdr); w.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d", w.Code)
	}
	if subs, _ := st.PushSubscriptions(t.Context()); len(subs) != 0 {
		t.Fatalf("still subscribed: %+v", subs)
	}
	if w := do(h, "POST", "/api/push/test", lan, "", nil); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "geen enkele app") {
		t.Fatalf("test without subscribers: %d %s", w.Code, w.Body)
	}
}

// A manual action is switched on and off over the API and can be put on a
// dashboard tile; an automation cannot.
func TestManualActionAPI(t *testing.T) {
	s, _ := withRules(t)
	h := withTiles(t, s).Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	lan := "192.168.1.20:5000"

	save := func(body string) rules.View {
		t.Helper()
		w := do(h, "POST", "/api/rules", lan, body, hdr)
		var v rules.View
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		return v
	}
	manual := save(`{"name":"Socket aan","kind":"automation","manual":true,"enabled":true,
		"actions":[{"type":"switch","device":"socket1","on":true}],
		"else_actions":[{"type":"switch","device":"socket1","on":false}]}`)
	auto := save(`{"name":"Auto","kind":"automation","enabled":true,
		"conditions":[{"device":"socket1","metric":"power_w","op":"<","value":5}],
		"actions":[{"type":"switch","device":"socket1","on":true}]}`)

	w := do(h, "POST", "/api/rules/"+itoa(manual.ID)+"/start", lan, "", nil)
	var v rules.View
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.State.Active {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/api/rules/"+itoa(manual.ID)+"/stop", lan, "", nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.State.Active {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/rules/"+itoa(auto.ID)+"/start", lan, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("start of an automation: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/rules/999/start", lan, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("start of an unknown rule: %d %s", w.Code, w.Body)
	}

	for _, bad := range []string{
		`{"name":"x","items":[{"type":"control","rule":` + itoa(auto.ID) + `}]}`,
		`{"name":"x","items":[{"type":"control","rule":999}]}`,
	} {
		if w := do(h, "POST", "/api/dashboard/tiles", lan, bad, hdr); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "onbekende actie") {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	w = do(h, "POST", "/api/dashboard/tiles", lan,
		`{"name":"Acties","items":[{"type":"control","rule":`+itoa(manual.ID)+`,"device":"d1","control":"power","label":"Socket"}]}`, hdr)
	var tile store.DashboardTile
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &tile) != nil {
		t.Fatalf("tile: %d %s", w.Code, w.Body)
	}
	if want := `[{"type":"control","rule":` + itoa(manual.ID) + `,"label":"Socket"}]`; string(tile.Items) != want {
		t.Fatalf("items %s, want %s", tile.Items, want)
	}
}

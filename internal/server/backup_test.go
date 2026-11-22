package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/backup"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func TestBackupEndpoints(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}

	// without a backup service: off
	w := do(h, "GET", "/api/settings/backup", "192.168.1.20:5000", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("get off: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/settings/backup", "192.168.1.20:5000", "{}", hdr); w.Code != http.StatusConflict {
		t.Fatalf("make while off: %d %s", w.Code, w.Body)
	}

	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, backup.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s.Backups = &backup.Service{Store: st, DataDir: data, Dir: filepath.Join(data, "backups"), Keep: 3, At: 3 * time.Hour, Loc: time.UTC}
	h = s.Handler()
	w = do(h, "POST", "/api/settings/backup", "192.168.1.20:5000", "{}", hdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hwlocal-backup-") {
		t.Fatalf("make: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/settings/backup", "8.8.8.8:5000", "{}", hdr); w.Code != http.StatusForbidden {
		t.Fatalf("from outside the LAN: %d", w.Code)
	}
}

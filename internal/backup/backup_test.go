package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setup makes a data directory with a database and the files a gateway
// keeps there, and a service that backs it up to dir.
func setup(t *testing.T) (*Service, string) {
	t.Helper()
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for name, content := range map[string]string{
		"secret.key":        "key",
		"auth.json":         "{}",
		"tls/ca.crt":        "cert",
		"gateway.log":       "log",
		"gateway.log.1":     "old log",
		"hwlocal.db.v3.bak": "old db",
		"bin/hwlocal":       "exe",
		"backups/x.zip":     "earlier",
	} {
		write(t, filepath.Join(data, filepath.FromSlash(name)), content)
	}
	now := time.Date(2026, 11, 17, 3, 30, 0, 0, time.UTC)
	s := &Service{Store: st, DataDir: data, Dir: filepath.Join(data, "backups"), Keep: 2,
		At: 3*time.Hour + 30*time.Minute, Loc: time.UTC, Version: "test",
		now: func() time.Time { return now }}
	return s, data
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

func TestMakeContents(t *testing.T) {
	s, _ := setup(t)
	info, err := s.Make(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.File != "hwlocal-backup-20261117-0330.zip" {
		t.Errorf("file = %q", info.File)
	}
	names := zipNames(t, filepath.Join(s.Dir, info.File))
	want := []string{"manifest.json", "hwlocal.db", "auth.json", "secret.key", "tls/ca.crt"}
	if !slices.Equal(names, want) {
		t.Errorf("entries = %v, want %v", names, want)
	}
	m, err := ReadManifest(filepath.Join(s.Dir, info.File))
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != store.SchemaVersion() || m.Version != "test" {
		t.Errorf("manifest = %+v", m)
	}
	// no temporary files are left behind
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	st := s.Status()
	if !st.Enabled || st.Last == nil || st.Last.Error != "" || len(st.Backups) != 1 || st.At != "03:30" {
		t.Errorf("status = %+v", st)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	s, _ := setup(t)
	at := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	for range 4 {
		if _, err := s.Make(context.Background()); err != nil {
			t.Fatal(err)
		}
		at = at.Add(24 * time.Hour)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range list {
		got = append(got, b.File)
	}
	want := []string{"hwlocal-backup-20261117-0330.zip", "hwlocal-backup-20261116-0330.zip"}
	if !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
	// files that are no backups are left alone
	if _, err := os.Stat(filepath.Join(s.Dir, "x.zip")); err != nil {
		t.Errorf("other file removed: %v", err)
	}
}

func TestFailureNotifies(t *testing.T) {
	s, data := setup(t)
	// a file where the directory should be
	s.Dir = filepath.Join(data, "secret.key", "sub")
	var title string
	s.Notify = func(_ context.Context, ti, _ string) { title = ti }
	if _, err := s.Make(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if title != "Back-up mislukt" {
		t.Errorf("notify title = %q", title)
	}
	if st := s.Status(); st.Last == nil || st.Last.Error == "" {
		t.Errorf("status = %+v", st)
	}
}

func TestNext(t *testing.T) {
	s := &Service{At: 3*time.Hour + 30*time.Minute, Loc: time.UTC}
	for _, c := range []struct{ now, want string }{
		{"2026-11-17T01:00:00Z", "2026-11-17T03:30:00Z"},
		{"2026-11-17T03:30:00Z", "2026-11-18T03:30:00Z"},
		{"2026-11-17T22:00:00Z", "2026-11-18T03:30:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := s.next(now).Format(time.RFC3339); got != c.want {
			t.Errorf("next(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func TestParseTime(t *testing.T) {
	if d, err := ParseTime("03:30"); err != nil || d != 3*time.Hour+30*time.Minute {
		t.Errorf("03:30 = %v, %v", d, err)
	}
	for _, v := range []string{"", "25:00", "3u", "03:30:00"} {
		if _, err := ParseTime(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestRestore(t *testing.T) {
	s, _ := setup(t)
	info, err := s.Make(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(s.Dir, info.File)
	ctx := context.Background()

	// into an empty directory
	to := filepath.Join(t.TempDir(), "data")
	res, err := Restore(ctx, zipPath, to, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != "" {
		t.Errorf("moved = %q", res.Moved)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "secret.key")); string(b) != "key" {
		t.Errorf("secret.key = %q", b)
	}
	st, err := store.Open(filepath.Join(to, DBFile))
	if err != nil {
		t.Fatalf("restored database: %v", err)
	}
	st.Close()

	// over an existing one: refused without force
	write(t, filepath.Join(to, "secret.key"), "other key")
	if _, err := Restore(ctx, zipPath, to, false); err == nil {
		t.Fatal("existing database replaced without force")
	}
	res, err = Restore(ctx, zipPath, to, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "secret.key")); string(b) != "key" {
		t.Errorf("secret.key = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(res.Moved, "secret.key")); string(b) != "other key" {
		t.Errorf("kept secret.key = %q", b)
	}
	if _, err := os.Stat(filepath.Join(res.Moved, DBFile)); err != nil {
		t.Errorf("old database not kept: %v", err)
	}
}

// makeZip writes a zip with a manifest and the given entries.
func makeZip(t *testing.T, m Manifest, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "b.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create(manifestFile)
	json.NewEncoder(w).Encode(m)
	for name, content := range entries {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	f.Close()
	return path
}

func TestRestoreRefuses(t *testing.T) {
	ctx := context.Background()
	ok := Manifest{App: "hwlocal", Schema: 1}
	for name, c := range map[string]struct {
		m       Manifest
		entries map[string]string
	}{
		"newer schema": {Manifest{App: "hwlocal", Schema: store.SchemaVersion() + 1}, map[string]string{DBFile: ""}},
		"other app":    {Manifest{App: "x"}, map[string]string{DBFile: ""}},
		"no database":  {ok, map[string]string{"secret.key": "k"}},
		"zip slip":     {ok, map[string]string{DBFile: "", "../evil": "x"}},
		"absolute":     {ok, map[string]string{DBFile: "", "/evil": "x"}},
		"backslash":    {ok, map[string]string{DBFile: "", `..\evil`: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			to := t.TempDir()
			if _, err := Restore(ctx, makeZip(t, c.m, c.entries), to, true); err == nil {
				t.Fatal("restored")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(to), "evil")); err == nil {
				t.Fatal("wrote outside the data directory")
			}
		})
	}
}

func TestSafeName(t *testing.T) {
	for name, want := range map[string]bool{
		"secret.key": true, "tls/ca.crt": true,
		"": false, "../x": false, "a/../../x": false, "/x": false, `a\b`: false, "C:x": false, "./x": false,
	} {
		if got := safeName(name); got != want {
			t.Errorf("safeName(%q) = %v", name, got)
		}
	}
}

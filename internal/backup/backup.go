// Package backup writes the data directory to a zip file every day and puts
// one back (Restore). A backup holds a consistent copy of the database and
// every other file the gateway keeps in its data directory: secret.key
// (without it the plugin and mail settings load empty), the push key, the
// password and the settings files, certificates. Logs, the copies made
// before a migration and the backups themselves are left out.
package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// DBFile is the database's name in the data directory and in a backup.
const DBFile = "hwlocal.db"

// manifestFile describes a backup; it is the zip's first entry.
const manifestFile = "manifest.json"

// prefix and suffix of a backup's file name, e.g.
// hwlocal-backup-20261117-0330.zip.
const (
	prefix = "hwlocal-backup-"
	suffix = ".zip"
	stamp  = "20060102-1504"
)

// Manifest describes a backup.
type Manifest struct {
	App     string    `json:"app"`
	Version string    `json:"version"`
	Schema  int       `json:"schema"`
	Created time.Time `json:"created"`
	Files   []string  `json:"files"`
}

// Info is one backup file.
type Info struct {
	File string    `json:"file"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

// Result is the outcome of the latest attempt.
type Result struct {
	Info
	Error string `json:"error,omitempty"`
}

// Status is what the PWA shows.
type Status struct {
	Enabled bool      `json:"enabled"`
	Dir     string    `json:"dir,omitempty"`
	Keep    int       `json:"keep"`
	At      string    `json:"at"` // time of day, "03:30"
	Next    time.Time `json:"next,omitzero"`
	Last    *Result   `json:"last,omitempty"` // the latest attempt since the start
	Backups []Info    `json:"backups"`        // newest first
	Running bool      `json:"running"`
}

// Service makes the backups.
type Service struct {
	Store   *store.Store
	DataDir string
	// Dir is where the backups go; "" turns them off.
	Dir string
	// Keep is how many backups are kept (at least 1).
	Keep int
	// At is the time of day of the daily backup, as a duration since
	// midnight in Loc.
	At      time.Duration
	Loc     *time.Location
	Version string
	Log     *slog.Logger
	// Notify reports a failed backup (optional).
	Notify func(ctx context.Context, title, body string)

	now func() time.Time // tests

	run    sync.Mutex // one backup at a time
	mu     sync.Mutex
	last   *Result
	active bool
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) keep() int { return max(s.Keep, 1) }

// Enabled reports whether backups are made.
func (s *Service) Enabled() bool { return s != nil && s.Dir != "" }

// ParseTime parses a time of day "HH:MM".
func ParseTime(v string) (time.Duration, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("ongeldige tijd %q (UU:MM)", v)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// next returns the first daily backup time after now.
func (s *Service) next(now time.Time) time.Time {
	now = now.In(s.loc())
	y, m, d := now.Date()
	h, min := int(s.At/time.Hour), int(s.At%time.Hour/time.Minute)
	t := time.Date(y, m, d, h, min, 0, 0, s.loc())
	if !t.After(now) {
		t = time.Date(y, m, d+1, h, min, 0, 0, s.loc())
	}
	return t
}

// startDelay is how long after the start a missed backup is made.
const startDelay = 2 * time.Minute

// Run makes a backup every day at At, and one shortly after the start when
// the newest backup is more than a day old (the gateway was off at At).
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	wait := time.Until(s.next(s.clock()))
	if list, err := s.List(); err == nil && (len(list) == 0 || s.clock().Sub(list[0].At) > 25*time.Hour) {
		wait = min(wait, startDelay)
	}
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.Make(ctx) // a failure is logged and pushed by Make
		wait = time.Until(s.next(s.clock()))
	}
}

// Make writes a backup now and removes the oldest beyond Keep.
func (s *Service) Make(ctx context.Context) (Info, error) {
	if !s.Enabled() {
		return Info{}, errors.New("back-ups staan uit")
	}
	s.run.Lock()
	defer s.run.Unlock()
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
	started := s.clock()
	info, err := s.write(ctx, started)
	if err == nil {
		err = s.prune()
	}
	r := &Result{Info: info}
	if r.At.IsZero() {
		r.At = started
	}
	if err != nil {
		r.Error = err.Error()
	}
	s.mu.Lock()
	s.active, s.last = false, r
	s.mu.Unlock()
	if err != nil {
		s.log().Warn("back-up mislukt", "dir", s.Dir, "err", err)
		if s.Notify != nil {
			s.Notify(ctx, "Back-up mislukt", err.Error())
		}
		return info, err
	}
	s.log().Info("back-up gemaakt", "file", filepath.Join(s.Dir, info.File), "bytes", info.Size, "took", s.clock().Sub(started).Round(time.Millisecond))
	return info, nil
}

// Status returns the settings, the latest attempt and the backups.
func (s *Service) Status() Status {
	st := Status{Enabled: s.Enabled(), Keep: s.keep(), Backups: []Info{}}
	st.At = fmt.Sprintf("%02d:%02d", int(s.At/time.Hour), int(s.At%time.Hour/time.Minute))
	if !st.Enabled {
		return st
	}
	st.Dir, st.Next = s.Dir, s.next(s.clock())
	s.mu.Lock()
	st.Running = s.active
	if s.last != nil {
		r := *s.last
		st.Last = &r
	}
	s.mu.Unlock()
	if list, err := s.List(); err == nil {
		st.Backups = list
	} else if st.Last == nil {
		st.Last = &Result{Error: err.Error()}
	}
	return st
}

// List returns the backups in Dir, newest first.
func (s *Service) List() ([]Info, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Info{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Info{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		at, err := time.ParseInLocation(stamp, strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix), s.loc())
		if err != nil {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{File: name, Size: fi.Size(), At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// prune removes the oldest backups beyond Keep.
func (s *Service) prune() error {
	list, err := s.List()
	if err != nil {
		return err
	}
	for _, b := range list[min(len(list), s.keep()):] {
		if err := os.Remove(filepath.Join(s.Dir, b.File)); err != nil {
			return fmt.Errorf("oude back-up niet verwijderd: %w", err)
		}
	}
	return nil
}

// write makes the zip: first in a temporary file next to it, renamed when
// complete, so a half-written backup never looks like one.
func (s *Service) write(ctx context.Context, now time.Time) (Info, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Info{}, fmt.Errorf("map voor back-ups: %w", err)
	}
	at := now.In(s.loc()).Truncate(time.Minute)
	name := prefix + at.Format(stamp) + suffix
	tmp, err := os.CreateTemp(s.Dir, ".tmp-"+prefix+"*")
	if err != nil {
		return Info{}, fmt.Errorf("map voor back-ups: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // gone after the rename
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		s.log().Debug("back-up: rechten niet gezet", "err", err)
	}

	// the database copy goes to the temporary directory first: it cannot
	// be streamed into the zip
	dbDir, err := os.MkdirTemp(s.Dir, ".tmp-db-*")
	if err != nil {
		tmp.Close()
		return Info{}, err
	}
	defer os.RemoveAll(dbDir)
	dbCopy := filepath.Join(dbDir, DBFile)
	if err := s.Store.Snapshot(ctx, dbCopy); err != nil {
		tmp.Close()
		return Info{}, err
	}

	files, err := s.dataFiles()
	if err != nil {
		tmp.Close()
		return Info{}, err
	}
	m := Manifest{App: "hwlocal", Version: s.Version, Schema: store.SchemaVersion(), Created: now, Files: append([]string{DBFile}, files...)}
	zw := zip.NewWriter(tmp)
	err = writeZip(zw, m, func(rel string) string {
		if rel == DBFile {
			return dbCopy
		}
		return filepath.Join(s.DataDir, filepath.FromSlash(rel))
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Info{}, fmt.Errorf("back-up schrijven: %w", err)
	}
	final := filepath.Join(s.Dir, name)
	if err := os.Rename(tmpName, final); err != nil {
		return Info{}, fmt.Errorf("back-up schrijven: %w", err)
	}
	fi, err := os.Stat(final)
	if err != nil {
		return Info{}, err
	}
	return Info{File: name, Size: fi.Size(), At: at}, nil
}

func writeZip(zw *zip.Writer, m Manifest, source func(rel string) string) error {
	w, err := zw.Create(manifestFile)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return err
	}
	for _, rel := range m.Files {
		if err := addFile(zw, rel, source(rel)); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	return nil
}

func addFile(zw *zip.Writer, rel, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	h, err := zip.FileInfoHeader(fi)
	if err != nil {
		return err
	}
	h.Name, h.Method = rel, zip.Deflate
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// dataFiles lists the files of the data directory that go into a backup
// besides the database, as slash-separated paths relative to it.
func (s *Service) dataFiles() ([]string, error) {
	backupDir, _ := filepath.Abs(s.Dir)
	var out []string
	err := filepath.WalkDir(s.DataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.DataDir, path)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			if abs, _ := filepath.Abs(path); abs == backupDir || skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipFile(d.Name()) {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("datamap lezen: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// skipDir: directories that are no part of the gateway's state: the
// development binaries, earlier restores, the default backup directory.
func skipDir(name string) bool {
	return name == "bin" || name == "backups" || strings.HasPrefix(name, restoredPrefix) || strings.HasPrefix(name, ".tmp-")
}

// skipFile: the live database (copied consistently instead), the copies
// made before a migration, logs and temporary files.
func skipFile(name string) bool {
	switch {
	case name == DBFile || strings.HasPrefix(name, DBFile+"-") || strings.HasPrefix(name, DBFile+"."):
		return true
	case strings.HasSuffix(name, ".log") || strings.Contains(name, ".log."):
		return true
	case strings.HasPrefix(name, ".tmp-"):
		return true
	}
	return false
}

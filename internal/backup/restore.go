package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// restoredPrefix names the directory Restore moves the files it replaces
// to: pre-restore-20261117-1012.
const restoredPrefix = "pre-restore-"

// ReadManifest reads and checks the manifest of the backup at zipPath.
func ReadManifest(zipPath string) (Manifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("geen leesbare back-up: %w", err)
	}
	defer zr.Close()
	return readManifest(&zr.Reader)
}

func readManifest(zr *zip.Reader) (Manifest, error) {
	var m Manifest
	f, err := zr.Open(manifestFile)
	if err != nil {
		return m, errors.New("geen back-up van hwlocal: manifest.json ontbreekt")
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return m, fmt.Errorf("manifest.json: %w", err)
	}
	if m.App != "hwlocal" {
		return m, errors.New("geen back-up van hwlocal")
	}
	if m.Schema > store.SchemaVersion() {
		return m, fmt.Errorf("de back-up heeft schemaversie %d, dit programma kent tot en met %d: gebruik de nieuwere versie van hwlocal", m.Schema, store.SchemaVersion())
	}
	return m, nil
}

// RestoreResult says what Restore did.
type RestoreResult struct {
	Manifest Manifest
	// Moved is the directory the replaced files went to ("" when none
	// were replaced).
	Moved string
}

// Restore puts the backup at zipPath into dataDir. The gateway must be
// stopped. Files the backup replaces, and the database's -wal and -shm
// files, are moved to dataDir/pre-restore-<time> first; without force a
// data directory that already has a database is refused.
func Restore(ctx context.Context, zipPath, dataDir string, force bool) (RestoreResult, error) {
	var res RestoreResult
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return res, fmt.Errorf("geen leesbare back-up: %w", err)
	}
	defer zr.Close()
	m, err := readManifest(&zr.Reader)
	if err != nil {
		return res, err
	}
	res.Manifest = m

	// every entry is checked before anything is written
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		if f.Name == manifestFile || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !safeName(f.Name) {
			return res, fmt.Errorf("ongeldige naam in de back-up: %q", f.Name)
		}
		entries[f.Name] = f
	}
	if entries[DBFile] == nil {
		return res, errors.New("de back-up bevat geen database")
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return res, err
	}
	if _, err := os.Stat(filepath.Join(dataDir, DBFile)); err == nil && !force {
		return res, fmt.Errorf("%s heeft al een database; stop de gateway en gebruik -force om hem te vervangen (de huidige bestanden worden bewaard)", dataDir)
	}

	// unpack into a temporary directory in dataDir (same file system, so
	// the files can be renamed into place)
	tmp, err := os.MkdirTemp(dataDir, ".tmp-restore-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	for name, f := range entries {
		if err := extract(f, filepath.Join(tmp, filepath.FromSlash(name))); err != nil {
			return res, fmt.Errorf("%s: %w", name, err)
		}
	}
	got, err := store.FileSchemaVersion(ctx, filepath.Join(tmp, DBFile))
	if err != nil {
		return res, err
	}
	if got > store.SchemaVersion() {
		return res, fmt.Errorf("de database in de back-up heeft schemaversie %d, dit programma kent tot en met %d", got, store.SchemaVersion())
	}

	// move what would be replaced out of the way
	moved := filepath.Join(dataDir, restoredPrefix+time.Now().Format(stamp))
	replace := []string{DBFile + "-wal", DBFile + "-shm"}
	for name := range entries {
		replace = append(replace, name)
	}
	for _, name := range replace {
		from := filepath.Join(dataDir, filepath.FromSlash(name))
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		to := filepath.Join(moved, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return res, err
		}
		if err := os.Rename(from, to); err != nil {
			return res, fmt.Errorf("%s niet verplaatst (draait de gateway nog?): %w", name, err)
		}
		res.Moved = moved
	}
	for name := range entries {
		to := filepath.Join(dataDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return res, err
		}
		if err := os.Rename(filepath.Join(tmp, filepath.FromSlash(name)), to); err != nil {
			return res, fmt.Errorf("%s: %w", name, err)
		}
	}
	return res, nil
}

// safeName: a relative path inside the data directory.
func safeName(name string) bool {
	if name == "" || strings.Contains(name, `\`) || strings.Contains(name, ":") || path.IsAbs(name) {
		return false
	}
	clean := path.Clean(name)
	return clean == name && clean != ".." && !strings.HasPrefix(clean, "../")
}

func extract(f *zip.File, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

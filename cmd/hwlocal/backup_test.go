package main

import (
	"path/filepath"
	"testing"
)

func TestBackupDir(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	for v, want := range map[string]string{
		"":    filepath.Join(data, "backups"),
		"off": "",
		"OFF": "",
	} {
		got, err := backupDir(v, data)
		if err != nil || got != want {
			t.Errorf("backupDir(%q) = %q, %v; want %q", v, got, err, want)
		}
	}
	if got, _ := backupDir("rel", data); !filepath.IsAbs(got) {
		t.Errorf("relative dir not made absolute: %q", got)
	}
}

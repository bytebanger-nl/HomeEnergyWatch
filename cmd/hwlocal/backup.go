package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/backup"
	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/rules"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// newBackups sets up the daily backup from the configuration. A failure is
// a notice, and pushed to the PWA installs that turned alerts on.
func newBackups(cfg config, dataDir string, st *store.Store, loc *time.Location, log *slog.Logger, push *rules.Push, notices *notice.Log) (*backup.Service, error) {
	at, err := backup.ParseTime(cfg.backupTime)
	if err != nil {
		return nil, fmt.Errorf("HWLOCAL_BACKUP_TIME: %w", err)
	}
	if cfg.backupKeep < 1 {
		return nil, fmt.Errorf("HWLOCAL_BACKUP_KEEP moet minstens 1 zijn, niet %d", cfg.backupKeep)
	}
	dir, err := backupDir(cfg.backupDir, dataDir)
	if err != nil {
		return nil, err
	}
	s := &backup.Service{Store: st, DataDir: dataDir, Dir: dir, Keep: cfg.backupKeep, At: at, Loc: loc, Version: version, Log: log}
	s.Notify = func(ctx context.Context, title, body string) {
		notices.Add(notice.KindBackup, "", title, body)
		if push == nil {
			return
		}
		if err := push.Notify(ctx, rules.Message{Title: title, Body: body, Tag: "backup", URL: "/#/berichten"}); err != nil {
			log.Warn("melding over de back-up niet verstuurd", "err", err)
		}
	}
	if dir == "" {
		log.Info("back-ups staan uit")
	} else {
		log.Info("dagelijkse back-up", "dir", dir, "at", cfg.backupTime, "keep", cfg.backupKeep)
	}
	return s, nil
}

// backupDir resolves HWLOCAL_BACKUP_DIR: "" is <data dir>/backups, "off"
// turns backups off ("" is returned).
func backupDir(v, dataDir string) (string, error) {
	switch v = strings.TrimSpace(v); strings.ToLower(v) {
	case "off":
		return "", nil
	case "":
		return filepath.Join(dataDir, "backups"), nil
	}
	return filepath.Abs(v)
}

// runRestore implements "hwlocal restore": put a backup back into the data
// directory. The gateway (or the service) must be stopped.
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	dataDir := fs.String("data", defaultPasswordDataDir(), "datamap om de back-up in terug te zetten (HWLOCAL_DATA_DIR)")
	force := fs.Bool("force", false, "een bestaande database vervangen (de huidige bestanden worden verplaatst naar pre-restore-<tijd>)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "gebruik: hwlocal restore [-data map] [-force] <back-up.zip>")
		fmt.Fprintln(fs.Output(), "Stop eerst de gateway of de service.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("geef één back-upbestand op")
	}
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	res, err := backup.Restore(context.Background(), fs.Arg(0), dir, *force)
	if err != nil {
		return err
	}
	m := res.Manifest
	// run as root (sudo), the files would be unreadable for the service user
	for _, name := range m.Files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		for d := filepath.Dir(path); d != dir && strings.HasPrefix(d, dir); d = filepath.Dir(d) {
			matchOwner(d, dir)
		}
		matchOwner(path, dir)
	}
	fmt.Printf("teruggezet in %s: back-up van %s (versie %s, %d bestanden)\n", dir, m.Created.Local().Format("2006-01-02 15:04"), m.Version, len(m.Files))
	if res.Moved != "" {
		fmt.Println("de vervangen bestanden staan in", res.Moved)
	}
	if m.Schema < store.SchemaVersion() {
		fmt.Println("de database wordt bij de volgende start bijgewerkt naar de nieuwe schemaversie")
	}
	return nil
}

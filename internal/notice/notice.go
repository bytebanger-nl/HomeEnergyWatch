// Package notice keeps the messages for the user about things that went
// wrong and need attention: a report that could not be mailed, a backup that
// failed, a command that did not take effect, a login that is refused. They
// show up, with the outages of devices (package outage), under an icon in
// the PWA until the user confirms them as read.
//
// Only what the user should know about belongs here: a price that cannot be
// fetched yet or ista data that is not complete yet are expected and are
// retried, and are not notices.
package notice

import (
	"context"
	"log/slog"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Kinds of notice.
const (
	KindReport  = "report"  // a scheduled report was not mailed
	KindBackup  = "backup"  // the backup failed
	KindControl = "control" // a command did not take effect
	KindLogin   = "login"   // an integration's login is refused
	KindPlugin  = "plugin"  // a plugin could not be loaded
)

// Log records notices. A nil *Log records nothing.
type Log struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time

	// OnChange is called after a notice was added (optional), e.g. to tell
	// the PWA.
	OnChange func()
}

// New creates a Log.
func New(st *store.Store, log *slog.Logger) *Log {
	return &Log{store: st, log: log, now: time.Now}
}

// Add records a notice of kind about subject (a schedule, device or plugin;
// while unread the same kind and subject is counted on one notice).
func (l *Log) Add(kind, subject, title, text string) {
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.store.AddNotice(ctx, kind, subject, title, text, l.now()); err != nil {
		l.log.Error("bericht niet opgeslagen", "kind", kind, "err", err)
		return
	}
	if l.OnChange != nil {
		l.OnChange()
	}
}

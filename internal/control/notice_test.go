package control

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// A command of an automation that does not take effect is a notice; one
// from the app is not (the user sees its result there).
func TestFailedRuleCommandIsNotice(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hwlocal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pl := newPlugins()
	pl.drops = 100
	s, _ := service(pl, nil)
	s.Notices = notice.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	s.Apply(ctx, nil, Command{Device: "plugin:ac", Key: "power", Value: true, Source: "app"})
	if n, _ := st.UnreadNotices(ctx); n != 0 {
		t.Fatalf("%d notices for a command from the app", n)
	}
	s.Apply(ctx, nil, Command{Device: "plugin:ac", Key: "power", Value: true, Source: "rule"})
	list, err := st.Notices(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != notice.KindControl || list[0].Subject != "plugin:ac" {
		t.Fatalf("notices %+v", list)
	}
}

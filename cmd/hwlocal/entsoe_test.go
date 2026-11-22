package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
)

func newEntsoeSetting(t *testing.T, check func(context.Context, string) error) (*entsoeSetting, *prices.Service) {
	t.Helper()
	svc := &prices.Service{}
	return &entsoeSetting{
		path:  filepath.Join(t.TempDir(), entsoeTokenFile),
		names: defaultPriceSources,
		svc:   svc,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		check: check,
	}, svc
}

func sourceNames(svc *prices.Service) []string { return svc.Status().Sources }

func TestEntsoeSettingSetAndClear(t *testing.T) {
	e, svc := newEntsoeSetting(t, func(context.Context, string) error { return nil })
	if warn, err := e.Set(context.Background(), "  abc-123 "); err != nil || warn != "" {
		t.Fatalf("set: %q, %v", warn, err)
	}
	if b, _ := os.ReadFile(e.path); string(b) != "abc-123\n" {
		t.Fatalf("file holds %q", b)
	}
	// a pasted assignment is checked and saved as the bare token
	var checked string
	e.check = func(_ context.Context, tok string) error { checked = tok; return nil }
	if _, err := e.Set(context.Background(), "ENTSOE_API_TOKEN=abc-456"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(e.path); string(b) != "abc-456\n" || checked != "abc-456" {
		t.Fatalf("file holds %q, checked %q", b, checked)
	}
	if st := e.Status(); !st.Configured || !st.Used || st.FromEnv {
		t.Fatalf("status %+v", st)
	}
	if got := sourceNames(svc); len(got) != 3 || got[2] != "entsoe" {
		t.Fatalf("sources %v, want entsoe last", got)
	}

	if _, err := e.Set(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file not removed: %v", err)
	}
	if got := sourceNames(svc); len(got) != 2 {
		t.Fatalf("sources %v, want without entsoe", got)
	}
	if e.Status().Configured {
		t.Fatal("still configured")
	}
}

func TestEntsoeSettingRefusals(t *testing.T) {
	e, _ := newEntsoeSetting(t, func(context.Context, string) error { return prices.ErrTokenRejected })
	if _, err := e.Set(context.Background(), "wrong"); err == nil {
		t.Fatal("rejected token accepted")
	}
	if _, err := os.Stat(e.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected token was saved")
	}
	if _, err := e.Set(context.Background(), "two words"); err == nil {
		t.Fatal("token with a space accepted")
	}
	e.fromEnv = true
	if _, err := e.Set(context.Background(), "x"); err == nil {
		t.Fatal("token from the environment overwritten")
	}
}

// When ENTSO-E cannot be reached the token is kept, with a warning.
func TestEntsoeSettingUncheckedIsSaved(t *testing.T) {
	e, _ := newEntsoeSetting(t, func(context.Context, string) error { return errors.New("network down") })
	warn, err := e.Set(context.Background(), "abc")
	if err != nil || warn == "" {
		t.Fatalf("got %q, %v", warn, err)
	}
	if !e.Status().Configured {
		t.Fatal("not saved")
	}
}

package mhi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// dropFirst closes the first connection without answering, as a module
// that times out or resets a handshake; later requests reach the unit.
type dropFirst struct {
	unit    http.Handler
	dropped atomic.Int32
}

func (d *dropFirst) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if d.dropped.Add(1) == 1 {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	d.unit.ServeHTTP(w, r)
}

// A dropped connection is retried once on a new one, so the poll succeeds.
func TestMHIRetriesDroppedConnection(t *testing.T) {
	h := &dropFirst{unit: &fakeUnit{}}
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	in := &instance{cli: newClient(), watch: plugin.NewWatchdog("0")}
	defer in.Close()
	dev := plugin.Device{ID: "d1", Address: strings.TrimPrefix(srv.URL, "https://"), Config: map[string]string{"airconId": "abc"}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := in.Poll(ctx, dev)
	if err != nil || !r.Online {
		t.Fatalf("poll: %+v %v, want a reading after one retry", r, err)
	}
	if n := h.dropped.Load(); n != 2 {
		t.Fatalf("%d requests, want 2 (dropped, retried)", n)
	}
}

// Without time left for a second try, the first error is returned.
func TestMHINoRetryWithoutTime(t *testing.T) {
	h := &dropFirst{unit: &fakeUnit{}}
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	in := &instance{cli: newClient(), watch: plugin.NewWatchdog("0")}
	defer in.Close()
	dev := plugin.Device{ID: "d1", Address: strings.TrimPrefix(srv.URL, "https://"), Config: map[string]string{"airconId": "abc"}}

	ctx, cancel := context.WithTimeout(context.Background(), retryBudget/2)
	defer cancel()
	if _, err := in.Poll(ctx, dev); err == nil {
		t.Fatal("poll succeeded, want the error of the dropped connection")
	}
	if n := h.dropped.Load(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

// A unit's answer (a result code) is not a connection failure: no retry.
func TestMHINoRetryOnResult(t *testing.T) {
	if transient(context.Background(), &resultError{command: "setAirconStat", code: 11}) {
		t.Fatal("a result code counted as transient")
	}
}

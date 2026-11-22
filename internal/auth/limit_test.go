package auth

import (
	"testing"
	"time"
)

// A lockout ends: once it has passed, one attempt at a time may try again;
// a wrong one locks out twice as long, a right one forgets the failures.
func TestBeginAfterLockout(t *testing.T) {
	a, _ := open(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	for i := range freeFails {
		if d := a.Begin("c"); d != 0 {
			t.Fatalf("try %d refused: %v", i, d)
		}
		a.Finish(false, "c")
	}
	if d := a.Begin("c"); d != firstWait {
		t.Fatalf("locked out: wait %v", d)
	}

	now = now.Add(firstWait + time.Second)
	if d := a.Begin("c"); d != 0 {
		t.Fatalf("still refused after the wait: %v", d)
	}
	// a parallel attempt waits for the one being checked
	if d := a.Begin("c"); d == 0 {
		t.Fatal("second attempt allowed while one is being checked")
	}
	a.Finish(false, "c")
	if d := a.Begin("c"); d != 2*firstWait {
		t.Fatalf("after another wrong one: wait %v, want %v", d, 2*firstWait)
	}

	now = now.Add(2*firstWait + time.Second)
	if d := a.Begin("c"); d != 0 {
		t.Fatalf("refused after the second wait: %v", d)
	}
	a.Finish(true, "c")
	for i := range freeFails {
		if d := a.Begin("c"); d != 0 {
			t.Fatalf("after a correct password, try %d refused: %v", i, d)
		}
		a.Finish(false, "c")
	}
}

// The shared key of a proxy ends its lockout the same way.
func TestBeginProxyAfterLockout(t *testing.T) {
	a, _ := open(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	proxy := ProxyPrefix + "127.0.0.1"
	for i := range proxyFreeFails {
		c := string(rune('A'+i/26)) + string(rune('a'+i%26))
		if d := a.Begin(c, proxy); d != 0 {
			t.Fatalf("try %d refused: %v", i, d)
		}
		a.Finish(false, c, proxy)
	}
	if d := a.Begin("fresh", proxy); d == 0 {
		t.Fatal("proxy not locked out")
	}
	now = now.Add(firstWait + time.Second)
	if d := a.Begin("fresh", proxy); d != 0 {
		t.Fatalf("proxy still locked out after the wait: %v", d)
	}
	a.Finish(true, "fresh", proxy)
}

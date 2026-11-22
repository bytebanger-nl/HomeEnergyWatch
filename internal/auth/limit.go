package auth

import (
	"strings"
	"time"
)

// Login attempts are limited per client address: after freeFails wrong
// passwords in a row, the client waits a minute, and twice as long after
// every further wrong one, up to maxWait. A correct password resets it.
const (
	freeFails = 5
	firstWait = time.Minute
	maxWait   = 30 * time.Minute
	// a client without a wrong password for this long is forgotten
	forgetAfter = 24 * time.Hour
	maxClients  = 1000
)

// ProxyPrefix marks the key of a proxy on this machine (tailscale serve, a
// tunnel). Every client behind it also counts against this shared key, with
// proxyFreeFails free attempts: a proxy that passes on an X-Forwarded-For
// the client chose cannot be used to get a fresh budget for every guess.
const (
	ProxyPrefix    = "proxy:"
	proxyFreeFails = 50
)

type failure struct {
	count   int
	pending int // attempts being checked now (Begin without Finish)
	last    time.Time
	until   time.Time
}

func free(key string) int {
	if strings.HasPrefix(key, ProxyPrefix) {
		return proxyFreeFails
	}
	return freeFails
}

// Wait returns how long client must wait before it may try a password.
func (a *Auth) Wait(client string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.waitLocked(client)
}

func (a *Auth) waitLocked(key string) time.Duration {
	f := a.fails[key]
	if f == nil {
		return 0
	}
	if d := f.until.Sub(a.now()); d > 0 {
		return d
	}
	return 0
}

// Begin reserves one password attempt for every key (the client, and its
// proxy if any). It returns how long to wait when one of them may not try
// now: it is locked out, or it already has as many attempts being checked
// as it has free ones left, so parallel requests cannot all slip through
// before the first failure counts. Once a lockout has passed, one attempt
// at a time is allowed: a wrong one locks out again, twice as long. After
// Begin returned 0, call Finish.
func (a *Auth) Begin(keys ...string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		if d := a.waitLocked(k); d > 0 {
			return d
		}
		if f := a.fails[k]; f != nil && min(f.count, free(k)-1)+f.pending >= free(k) {
			return firstWait
		}
	}
	for _, k := range keys {
		if f := a.entryLocked(k); f != nil {
			f.pending++
		}
	}
	return 0
}

// Finish ends the attempts Begin reserved: ok forgets the keys' wrong
// passwords, otherwise each counts one more.
func (a *Auth) Finish(ok bool, keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		if f := a.fails[k]; f != nil && f.pending > 0 {
			f.pending--
		}
		if ok {
			delete(a.fails, k)
		} else {
			a.failedLocked(k)
		}
	}
}

// Failed notes a wrong password of client.
func (a *Auth) Failed(client string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failedLocked(client)
}

// entryLocked returns the record of key, made when needed; nil when too
// many addresses are being tracked (the hash still slows each try down).
func (a *Auth) entryLocked(key string) *failure {
	now := a.now()
	if f := a.fails[key]; f != nil {
		return f
	}
	for c, f := range a.fails {
		if f.pending == 0 && now.Sub(f.last) > forgetAfter {
			delete(a.fails, c)
		}
	}
	if len(a.fails) >= maxClients {
		return nil
	}
	f := &failure{last: now}
	a.fails[key] = f
	return f
}

func (a *Auth) failedLocked(key string) {
	f := a.entryLocked(key)
	if f == nil {
		return
	}
	now := a.now()
	f.count++
	f.last = now
	if n := free(key); f.count >= n {
		wait := firstWait << min(f.count-n, 10)
		f.until = now.Add(min(wait, maxWait))
	}
}

// Succeeded forgets the wrong passwords of client.
func (a *Auth) Succeeded(client string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, client)
}

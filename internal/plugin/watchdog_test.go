package plugin

import (
	"testing"
	"time"
)

func TestWatchdog(t *testing.T) {
	w := NewWatchdog("2")
	now := time.Now()
	if w.Expired("a", true, now.Add(5*time.Hour)) {
		t.Fatal("a device the app never switched on expired")
	}
	w.Note("a", true)
	if w.Expired("a", true, now.Add(time.Hour)) {
		t.Fatal("expired after 1 h of 2")
	}
	if !w.Expired("a", true, now.Add(3*time.Hour)) {
		t.Fatal("not expired after 3 h of 2")
	}
	// switched off elsewhere: forgotten
	w.Expired("a", false, now)
	if w.Expired("a", true, now.Add(3*time.Hour)) {
		t.Fatal("still watched after being seen off")
	}
	if off := NewWatchdog(""); off.max != 0 {
		t.Fatal("empty setting should be off")
	}
}

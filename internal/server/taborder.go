package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
)

// TabOrder holds the order of the PWA's tab row, chosen under Instellingen.
// It is stored as JSON in a file in the data directory and shared by every
// client: a change is published as a "tab_order" stream event. An empty
// order means the default one. Keys are the fixed routes (fixedTabs) and
// "tab:<id>" for the user's own plugin tabs; the PWA places tabs missing
// from the order (a new own tab) at their default spot and skips keys of
// tabs that no longer exist.
type TabOrder struct {
	path string

	mu    sync.RWMutex
	order []string
}

// fixedTabs are the routes of the tab row besides the own plugin tabs.
var fixedTabs = []string{"dashboard", "verbruik", "meldingen", "automatisering", "apparaten", "klimaat", "instellingen"}

// maxTabOrder bounds the order's length (fixed tabs plus own tabs, with room
// for keys of deleted tabs).
const maxTabOrder = 40

type tabOrderFile struct {
	Order []string `json:"order"`
}

// LoadTabOrder reads path; a missing file means the default order. On a
// damaged file the order is the default one and the error is returned, so
// the caller can warn and carry on.
func LoadTabOrder(path string) (*TabOrder, error) {
	t := &TabOrder{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	var f tabOrderFile
	if err := json.Unmarshal(b, &f); err != nil {
		return t, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := checkTabOrder(f.Order); err != nil {
		return t, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	t.order = f.Order
	return t, nil
}

// Order returns the stored order (empty: the default one).
func (t *TabOrder) Order() []string {
	if t == nil {
		return []string{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]string{}, t.order...)
}

func (t *TabOrder) set(order []string) error {
	b, err := json.MarshalIndent(tabOrderFile{Order: order}, "", "  ")
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("volgorde opslaan: %w", err)
	}
	if err := os.Rename(tmp, t.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("volgorde opslaan: %w", err)
	}
	t.order = order
	return nil
}

// checkTabOrder accepts fixed routes and "tab:<id>", each at most once.
func checkTabOrder(order []string) error {
	if len(order) > maxTabOrder {
		return fmt.Errorf("te veel tabbladen (hooguit %d)", maxTabOrder)
	}
	seen := make(map[string]bool, len(order))
	for _, k := range order {
		if seen[k] {
			return fmt.Errorf("tabblad %q staat er twee keer in", k)
		}
		seen[k] = true
		if id, ok := strings.CutPrefix(k, "tab:"); ok {
			if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
				return fmt.Errorf("onbekend tabblad %q", k)
			}
			continue
		}
		known := false
		for _, f := range fixedTabs {
			known = known || f == k
		}
		if !known {
			return fmt.Errorf("onbekend tabblad %q", k)
		}
	}
	return nil
}

func (s *Server) getTabOrder(w http.ResponseWriter, r *http.Request) {
	if s.TabOrder == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": s.TabOrder.Order()})
}

// putTabOrder stores {"order": [...]}; an empty list restores the default.
func (s *Server) putTabOrder(w http.ResponseWriter, r *http.Request) {
	if s.TabOrder == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return
	}
	var body struct {
		Order []string `json:"order"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Order == nil {
		body.Order = []string{}
	}
	if err := checkTabOrder(body.Order); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.TabOrder.set(body.Order); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Hub != nil {
		s.Hub.Publish(gateway.Event{Type: "tab_order", Data: body.Order})
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": body.Order})
}

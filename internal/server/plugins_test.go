package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// A device that is already added answers 409 with a Dutch message.
func TestPluginErrorDuplicate(t *testing.T) {
	w := httptest.NewRecorder()
	pluginError(w, fmt.Errorf("add: %w", plugin.ErrDuplicate))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "al toegevoegd") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

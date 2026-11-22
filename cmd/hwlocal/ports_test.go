package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := l.Addr().String()
	err = checkPort(busy)
	l.Close()
	if err == nil || !strings.Contains(err.Error(), "al in gebruik") {
		t.Fatalf("busy port: got %v", err)
	}
	if err := checkPort(busy); err != nil {
		t.Fatalf("freed port: %v", err)
	}
}

// A program listening on IPv4 only must be noticed for a wildcard address,
// even where the dual-stack listen itself would succeed (Windows).
func TestCheckPortIPv4Only(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	if err := checkPort(":" + port); err == nil || !strings.Contains(err.Error(), "al in gebruik") {
		t.Fatalf("IPv4-only listener on %s: got %v", port, err)
	}
}

func TestCheckPortsSkipsHTTPSWithoutTLS(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := config{httpAddr: "127.0.0.1:0", httpsAddr: l.Addr().String(), tlsMode: "off"}
	if err := checkPorts(cfg); err != nil {
		t.Fatalf("tls off: %v", err)
	}
	cfg.tlsMode = "selfsigned"
	if err := checkPorts(cfg); err == nil {
		t.Fatal("busy HTTPS port not reported")
	}
}

func TestReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	if tok, err := readTokenFile(filepath.Join(dir, "missing.txt")); tok != "" || err != nil {
		t.Fatalf("missing file: %q, %v", tok, err)
	}
	path := filepath.Join(dir, entsoeTokenFile)
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf  abc-123\r\n"), 0o600); err != nil { // with a UTF-8 BOM
		t.Fatal(err)
	}
	if tok, err := readTokenFile(path); tok != "abc-123" || err != nil {
		t.Fatalf("got %q, %v", tok, err)
	}
	// the whole line pasted from an example, as found on a real install
	if err := os.WriteFile(path, []byte("ENTSOE_API_TOKEN=abc-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := readTokenFile(path); tok != "abc-123" || err != nil {
		t.Fatalf("with assignment: got %q, %v", tok, err)
	}
}

func TestCleanToken(t *testing.T) {
	for in, want := range map[string]string{
		"abc-123":                           "abc-123",
		"  abc-123\r\n":                     "abc-123",
		"\xef\xbb\xbfabc-123":               "abc-123",
		"ENTSOE_API_TOKEN=abc-123":          "abc-123",
		"entsoe_api_token = abc-123":        "abc-123",
		`ENTSOE_API_TOKEN="abc-123"`:        "abc-123",
		"export ENTSOE_API_TOKEN='abc-123'": "abc-123",
		"set ENTSOE_API_TOKEN=abc-123":      "abc-123",
		`"abc-123"`:                         "abc-123",
		"OTHER=abc-123":                     "OTHER=abc-123", // only our own name is stripped
		"":                                  "",
	} {
		if got := cleanToken(in); got != want {
			t.Errorf("cleanToken(%q) = %q, want %q", in, got, want)
		}
	}
}

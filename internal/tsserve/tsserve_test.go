package tsserve

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const statusJSON = `{"Version":"1.80.0","BackendState":"Running","Self":{"DNSName":"gateway-pc.tail1234.ts.net.","HostName":"gateway-pc"},"CertDomains":["gateway-pc.tail1234.ts.net"]}`

// what `tailscale serve status --json` prints with two apps: this gateway on
// 8443 and another app on 443 with two paths, a TCP forward and a funnel
const serveJSON = `{
  "TCP": {"443": {"HTTPS": true}, "8443": {"HTTPS": true}, "2222": {"TCPForward": "127.0.0.1:22"}, "10000": {"HTTPS": true}},
  "Web": {
    "gateway-pc.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}, "/files": {"Path": "C:\\share"}}},
    "gateway-pc.tail1234.ts.net:8443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8080"}}},
    "gateway-pc.tail1234.ts.net:10000": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:9000"}}}
  },
  "AllowFunnel": {"gateway-pc.tail1234.ts.net:10000": true},
  "Foreground": {"abc": {"TCP": {"5000": {"HTTPS": true}}, "Web": {"gateway-pc.tail1234.ts.net:5000": {"Handlers": {"/": {"Text": "hi"}}}}}}
}`

func TestStatus(t *testing.T) {
	var calls []string
	c := &CLI{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "status" {
			return []byte(statusJSON), nil
		}
		return []byte(serveJSON), nil
	}}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.DNSName != "gateway-pc.tail1234.ts.net" || !st.HTTPS || st.BackendState != "Running" {
		t.Fatalf("status: %+v", st)
	}
	want := []Port{
		{Port: 443, Proto: "https", Targets: []string{"/ → http://127.0.0.1:3000", `/files → C:\share`}},
		{Port: 2222, Proto: "tcp", Targets: []string{"127.0.0.1:22"}},
		{Port: 5000, Proto: "https", Targets: []string{"/ → tekst"}, Foreground: true},
		{Port: 8443, Proto: "https", Targets: []string{"/ → http://127.0.0.1:8080"}, Proxy: "http://127.0.0.1:8080"},
		{Port: 10000, Proto: "https", Targets: []string{"/ → http://127.0.0.1:9000"}, Proxy: "http://127.0.0.1:9000", Funnel: true},
	}
	if !reflect.DeepEqual(st.Ports, want) {
		t.Fatalf("ports:\n got %+v\nwant %+v", st.Ports, want)
	}
	if p, ok := st.Find(8443); !ok || p.Proxy == "" {
		t.Fatalf("find: %+v %v", p, ok)
	}
	if _, ok := st.Find(8444); ok {
		t.Fatal("8444 found")
	}
	if !reflect.DeepEqual(calls, []string{"status --json --peers=false", "serve status --json"}) {
		t.Fatalf("calls: %q", calls)
	}
}

// Not logged in: the serve configuration is not read.
func TestStatusNotRunning(t *testing.T) {
	c := &CLI{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "status" {
			t.Fatalf("unexpected %q", args)
		}
		return []byte(`{"BackendState":"NeedsLogin","Self":{"DNSName":""},"CertDomains":null}`), nil
	}}
	st, err := c.Status(context.Background())
	if err != nil || st.BackendState != "NeedsLogin" || st.HTTPS || len(st.Ports) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestEmptyServeConfig(t *testing.T) {
	for _, in := range []string{"", "{}", "{}\n"} {
		if ports, err := parseServe([]byte(in)); err != nil || len(ports) != 0 {
			t.Errorf("%q: %v %v", in, ports, err)
		}
	}
}

func TestServeAndOffArgs(t *testing.T) {
	var calls []string
	c := &CLI{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, nil
	}}
	if err := c.Serve(context.Background(), 8443, "http://127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if err := c.Off(context.Background(), 443); err != nil {
		t.Fatal(err)
	}
	want := []string{"serve --bg --https=8443 http://127.0.0.1:8080", "serve --https=443 off"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls: %q", calls)
	}
}

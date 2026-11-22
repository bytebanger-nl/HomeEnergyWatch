package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"
)

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// parts reads a received message and returns its subject and the parts
// (content type -> body) of all nested multiparts.
func parts(t *testing.T, data []byte) (string, map[string][]byte) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	var walk func(ct, cte string, r io.Reader)
	walk = func(ct, cte string, r io.Reader) {
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(mt, "multipart/") {
			if strings.EqualFold(cte, "base64") {
				r = base64.NewDecoder(base64.StdEncoding, r)
			}
			b, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			out[mt] = b
			return
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			walk(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p)
		}
	}
	walk(msg.Header.Get("Content-Type"), "", msg.Body)
	return subject, out
}

func TestSendPlainWithAttachment(t *testing.T) {
	srv := &SimServer{User: "u", Password: "geheim"}
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := hostPort(t, addr)
	s := Settings{Host: host, Port: port, Security: None, User: "u", Password: "geheim", From: "Gateway <gw@example.nl>"}
	pdf := bytes.Repeat([]byte("%PDF-1.4 data "), 100)
	m := &Message{
		To:          []string{"a@example.nl", "Bé <b@example.nl>"},
		Subject:     "Maandrapport oktober – € 12,34",
		Text:        "Hallo, één regel.",
		HTML:        "<p>Hallo</p>",
		Attachments: []Attachment{{Name: "rapport-2026-10.pdf", ContentType: "application/pdf", Data: pdf}},
	}
	if err := (&Sender{}).Send(context.Background(), s, m); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("received %d messages", len(got))
	}
	r := got[0]
	if r.From != "gw@example.nl" || strings.Join(r.To, ",") != "a@example.nl,b@example.nl" || r.Auth != "u" {
		t.Fatalf("envelope %+v", r)
	}
	subject, ps := parts(t, r.Data)
	if subject != m.Subject {
		t.Fatalf("subject %q", subject)
	}
	if string(ps["text/plain"]) != m.Text || string(ps["text/html"]) != m.HTML {
		t.Fatalf("bodies %q / %q", ps["text/plain"], ps["text/html"])
	}
	if !bytes.Equal(ps["application/pdf"], pdf) {
		t.Fatalf("attachment differs: %d bytes", len(ps["application/pdf"]))
	}
}

func TestSendWrongPassword(t *testing.T) {
	srv := &SimServer{User: "u", Password: "goed"}
	addr, _ := srv.Listen("127.0.0.1:0")
	defer srv.Close()
	host, port := hostPort(t, addr)
	s := Settings{Host: host, Port: port, Security: None, User: "u", Password: "fout", From: "gw@example.nl"}
	err := (&Sender{}).Send(context.Background(), s, &Message{To: []string{"a@example.nl"}, Subject: "x", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "inloggen") {
		t.Fatalf("err = %v", err)
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("message delivered")
	}
}

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func TestSendStartTLS(t *testing.T) {
	cert, pool := testCert(t)
	srv := &SimServer{TLS: &tls.Config{Certificates: []tls.Certificate{cert}}}
	addr, _ := srv.Listen("127.0.0.1:0")
	defer srv.Close()
	host, port := hostPort(t, addr)
	s := Settings{Host: host, Port: port, Security: StartTLS, User: "u", Password: "p", From: "gw@example.nl"}
	snd := &Sender{TLSConfig: &tls.Config{RootCAs: pool}}
	if err := snd.Send(context.Background(), s, &Message{To: []string{"a@example.nl"}, Subject: "x", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(srv.Messages()) != 1 {
		t.Fatal("not delivered")
	}
	// an untrusted certificate is refused
	if err := (&Sender{}).Send(context.Background(), s, &Message{To: []string{"a@example.nl"}, Subject: "x", Text: "x"}); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestStartTLSRequired(t *testing.T) {
	srv := &SimServer{}
	addr, _ := srv.Listen("127.0.0.1:0")
	defer srv.Close()
	host, port := hostPort(t, addr)
	s := Settings{Host: host, Port: port, Security: StartTLS, From: "gw@example.nl"}
	err := (&Sender{}).Send(context.Background(), s, &Message{To: []string{"a@example.nl"}, Subject: "x", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		s  Settings
		ok bool
	}{
		{Settings{}, true},
		{Settings{Host: "smtp.example.nl", From: "a@example.nl"}, true},
		{Settings{Host: "smtp.example.nl:587", From: "a@example.nl"}, false},
		{Settings{Host: "https://smtp.example.nl", From: "a@example.nl"}, false},
		{Settings{Host: "smtp.example.nl", From: "geen adres"}, false},
		{Settings{Host: "smtp.example.nl", From: "a@example.nl", Security: "ssl"}, false},
		{Settings{Host: "smtp.example.nl", From: "a@example.nl", Password: "x"}, false},
		{Settings{From: "a@example.nl"}, false},
		// a password is only sent over TLS or to this machine
		{Settings{Host: "192.168.1.10", From: "a@example.nl", Security: None, User: "u", Password: "x"}, false},
		{Settings{Host: "127.0.0.1", From: "a@example.nl", Security: None, User: "u", Password: "x"}, true},
		{Settings{Host: "192.168.1.10", From: "a@example.nl", Security: None}, true},
		{Settings{Host: "192.168.1.10", From: "a@example.nl", Security: StartTLS, User: "u", Password: "x"}, true},
	} {
		s := c.s
		s.Normalize()
		if err := s.Validate(); (err == nil) != c.ok {
			t.Errorf("%+v: err = %v", c.s, err)
		}
	}
	s := Settings{Host: "h", Security: TLS}
	s.Normalize()
	if s.Port != 465 {
		t.Fatalf("port %d", s.Port)
	}
}

func TestParseList(t *testing.T) {
	got, err := ParseList("a@example.nl; Bé <b@example.nl>,\nc@example.nl")
	if err != nil || strings.Join(got, ",") != "a@example.nl,b@example.nl,c@example.nl" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ParseList("a@example.nl, nee"); err == nil {
		t.Fatal("bad address accepted")
	}
}

func TestHeaderInjection(t *testing.T) {
	m := &Message{To: []string{"a@example.nl"}, Subject: "x\r\nBcc: evil@example.nl", Text: "x"}
	b, err := m.Bytes("gw@example.nl", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("\r\nBcc:")) {
		t.Fatal("header injected")
	}
}

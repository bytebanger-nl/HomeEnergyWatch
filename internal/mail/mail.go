// Package mail sends e-mail through the user's own SMTP server (their
// provider, their own domain, Gmail with an app password, a service such as
// Brevo or SMTP2GO), with the standard library only. Mail is never sent
// straight to the recipient's MX: from a home connection port 25 is often
// blocked, and what gets through ends up as spam.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Security modes of the connection to the SMTP server.
const (
	StartTLS = "starttls" // plain connection upgraded with STARTTLS (port 587)
	TLS      = "tls"      // TLS from the start (port 465)
	None     = "none"     // no encryption: only for a server on this machine or the LAN
)

// Settings is the SMTP server to send through. Password is a secret: it is
// stored encrypted and never returned by the API.
type Settings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
}

// Configured reports whether there is enough to send mail.
func (s Settings) Configured() bool { return s.Host != "" && s.From != "" }

// ErrInvalid wraps every validation error.
var ErrInvalid = errors.New("ongeldige e-mailinstellingen")

// Normalize trims the settings and fills the default port of the security
// mode.
func (s *Settings) Normalize() {
	s.Host = strings.TrimSpace(s.Host)
	s.User = strings.TrimSpace(s.User)
	s.From = strings.TrimSpace(s.From)
	s.Security = strings.ToLower(strings.TrimSpace(s.Security))
	if s.Security == "" {
		s.Security = StartTLS
	}
	if s.Port == 0 {
		switch s.Security {
		case TLS:
			s.Port = 465
		case None:
			s.Port = 25
		default:
			s.Port = 587
		}
	}
}

// Validate checks the settings; empty settings (no host) are valid and mean
// "no mail".
func (s *Settings) Validate() error {
	if s.Host == "" {
		if s.From != "" || s.User != "" {
			return fmt.Errorf("%w: server ontbreekt", ErrInvalid)
		}
		return nil
	}
	if strings.ContainsAny(s.Host, " /:@") && net.ParseIP(s.Host) == nil {
		return fmt.Errorf("%w: server %q (alleen de naam, zonder poort of https://)", ErrInvalid, s.Host)
	}
	switch s.Security {
	case StartTLS, TLS, None:
	default:
		return fmt.Errorf("%w: beveiliging %q (starttls, tls of none)", ErrInvalid, s.Security)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("%w: poort %d", ErrInvalid, s.Port)
	}
	if _, err := ParseAddress(s.From); err != nil {
		return fmt.Errorf("%w: afzender: %v", ErrInvalid, err)
	}
	if s.Password != "" && s.User == "" {
		return fmt.Errorf("%w: wachtwoord zonder gebruikersnaam", ErrInvalid)
	}
	// smtp.PlainAuth only sends the password over TLS or to this machine
	if s.User != "" && s.Security == None && !localHost(s.Host) {
		return fmt.Errorf("%w: inloggen kan alleen over een beveiligde verbinding; kies starttls of tls, of laat gebruikersnaam en wachtwoord leeg", ErrInvalid)
	}
	return nil
}

// localHost reports whether host is this machine, the only server
// smtp.PlainAuth logs in to without TLS.
func localHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// ParseAddress checks one e-mail address ("Naam <a@b.nl>" or "a@b.nl").
func ParseAddress(s string) (*mail.Address, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("ongeldig e-mailadres %q", s)
	}
	if strings.ContainsAny(a.Address, "\r\n") {
		return nil, fmt.Errorf("ongeldig e-mailadres %q", s)
	}
	return a, nil
}

// ParseList splits a list of addresses separated by commas, semicolons or
// line breaks.
func ParseList(s string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		a, err := ParseAddress(f)
		if err != nil {
			return nil, err
		}
		out = append(out, a.Address)
	}
	return out, nil
}

// Attachment is a file sent along.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

// Message is one mail: plain text, optionally an HTML version of the same
// text, and attachments.
type Message struct {
	To          []string
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
}

// Bytes builds the message in MIME format (RFC 5322 / 2045).
func (m *Message) Bytes(from string, now time.Time) ([]byte, error) {
	fromAddr, err := ParseAddress(from)
	if err != nil {
		return nil, err
	}
	if len(m.To) == 0 {
		return nil, errors.New("geen ontvangers")
	}
	var to []string
	for _, t := range m.To {
		a, err := ParseAddress(t)
		if err != nil {
			return nil, err
		}
		to = append(to, a.String())
	}
	var buf bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	h("From", fromAddr.String())
	h("To", strings.Join(to, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Subject)))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+randomID()+"@"+domainOf(fromAddr.Address)+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")

	mixed := multipart.NewWriter(&buf)
	h("Content-Type", `multipart/mixed; boundary="`+mixed.Boundary()+`"`)
	buf.WriteString("\r\n")

	if m.HTML != "" {
		boundary := multipart.NewWriter(io.Discard).Boundary()
		part, err := mixed.CreatePart(textproto.MIMEHeader{"Content-Type": {`multipart/alternative; boundary="` + boundary + `"`}})
		if err != nil {
			return nil, err
		}
		alt := multipart.NewWriter(part)
		if err := alt.SetBoundary(boundary); err != nil {
			return nil, err
		}
		if err := writeText(alt, "text/plain", m.Text); err != nil {
			return nil, err
		}
		if err := writeText(alt, "text/html", m.HTML); err != nil {
			return nil, err
		}
		if err := alt.Close(); err != nil {
			return nil, err
		}
	} else if err := writeText(mixed, "text/plain", m.Text); err != nil {
		return nil, err
	}
	for _, a := range m.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		name := mime.QEncoding.Encode("utf-8", a.Name)
		part, err := mixed.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ct + `; name="` + name + `"`},
			"Content-Disposition":       {`attachment; filename="` + name + `"`},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(part, a.Data); err != nil {
			return nil, err
		}
	}
	if err := mixed.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeText(w *multipart.Writer, ct, text string) error {
	part, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {ct + "; charset=utf-8"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return err
	}
	return writeBase64(part, []byte(text))
}

// writeBase64 writes data in base64 lines of 76 characters.
func writeBase64(w io.Writer, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		if _, err := io.WriteString(w, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err := io.WriteString(w, enc+"\r\n")
	return err
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "localhost"
}

// Sender sends messages through an SMTP server.
type Sender struct {
	// Timeout of one send (connect, talk, deliver); default 30 s.
	Timeout time.Duration
	// TLSConfig replaces the default TLS settings (tests: a test CA); its
	// ServerName is always the configured host.
	TLSConfig *tls.Config
	Now       func() time.Time
}

// Send delivers m through the server of s.
func (snd *Sender) Send(ctx context.Context, s Settings, m *Message) error {
	s.Normalize()
	if err := s.Validate(); err != nil {
		return err
	}
	if !s.Configured() {
		return errors.New("e-mail is niet ingesteld")
	}
	now := time.Now
	if snd != nil && snd.Now != nil {
		now = snd.Now
	}
	body, err := m.Bytes(s.From, now())
	if err != nil {
		return err
	}
	timeout := 30 * time.Second
	if snd != nil && snd.Timeout > 0 {
		timeout = snd.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if snd != nil && snd.TLSConfig != nil {
		tlsCfg = snd.TLSConfig.Clone()
		tlsCfg.ServerName = s.Host
	}
	var d net.Dialer
	var conn net.Conn
	if s.Security == TLS {
		td := tls.Dialer{NetDialer: &d, Config: tlsCfg}
		conn, err = td.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("verbinden met %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("SMTP-server %s: %w", addr, err)
	}
	defer c.Close()
	if err := c.Hello(helloName()); err != nil {
		return fmt.Errorf("SMTP EHLO: %w", err)
	}
	if s.Security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("de server biedt geen STARTTLS; kies 'tls' (poort 465) of een andere server")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if s.User != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("de server biedt geen inloggen (AUTH) aan")
		}
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return fmt.Errorf("inloggen bij de SMTP-server: %w", err)
		}
	}
	from, _ := ParseAddress(s.From)
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("afzender geweigerd: %w", err)
	}
	for _, t := range m.To {
		a, _ := ParseAddress(t)
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("ontvanger %s geweigerd: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("versturen: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("versturen: %w", err)
	}
	return c.Quit()
}

// helloName is the name in EHLO: a stable, harmless name rather than the
// machine's host name, which says nothing to the server anyway.
func helloName() string { return "hwlocal.localdomain" }

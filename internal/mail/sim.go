package mail

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SimServer is a minimal SMTP server that keeps what it receives: for tests
// and hwsim, so the whole path (settings, report, attachment) can be tried
// without sending real mail. It speaks enough SMTP for net/smtp: EHLO, AUTH
// PLAIN, STARTTLS (when TLS is set), MAIL, RCPT, DATA, RSET, NOOP, QUIT.
type SimServer struct {
	// User and Password, when set, are required by AUTH; otherwise any
	// login is accepted and AUTH is optional.
	User, Password string
	// TLS enables STARTTLS with this configuration.
	TLS *tls.Config
	// Dir, when set, receives every message as <time>-<n>.eml.
	Dir string
	// Log receives one line per received message (optional).
	Log func(format string, args ...any)

	mu       sync.Mutex
	received []SimMessage
	ln       net.Listener
}

// SimMessage is one received message.
type SimMessage struct {
	From string
	To   []string
	Data []byte
	Auth string // the user that logged in, if any
}

// Listen starts the server on addr ("127.0.0.1:0" for a free port) and
// returns the address it listens on.
func (s *SimServer) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return ln.Addr().String(), nil
}

// Close stops the server.
func (s *SimServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Close()
}

// Messages returns what was received so far.
func (s *SimServer) Messages() []SimMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SimMessage(nil), s.received...)
}

func (s *SimServer) serve(conn net.Conn) {
	defer func() { conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	tp := textproto.NewConn(conn)
	reply := func(code int, msg string) { _ = tp.PrintfLine("%d %s", code, msg) }
	reply(220, "hwsim SMTP ready")
	var from, user string
	var to []string
	tlsOn := false
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"250-hwsim", "250-8BITMIME", "250-AUTH PLAIN"}
			if s.TLS != nil && !tlsOn {
				lines = append(lines, "250-STARTTLS")
			}
			lines = append(lines, "250 SIZE 52428800")
			for _, l := range lines {
				_ = tp.PrintfLine("%s", l)
			}
		case "STARTTLS":
			if s.TLS == nil || tlsOn {
				reply(502, "not supported")
				continue
			}
			reply(220, "go ahead")
			tc := tls.Server(conn, s.TLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, tp, tlsOn = tc, textproto.NewConn(tc), true
			from, user, to = "", "", nil
		case "AUTH":
			mech, initial, _ := strings.Cut(arg, " ")
			if !strings.EqualFold(mech, "PLAIN") {
				reply(504, "only PLAIN")
				continue
			}
			if initial == "" {
				_ = tp.PrintfLine("334 ")
				if initial, err = tp.ReadLine(); err != nil {
					return
				}
			}
			b, err := base64.StdEncoding.DecodeString(initial)
			parts := strings.Split(string(b), "\x00")
			if err != nil || len(parts) != 3 {
				reply(501, "bad AUTH")
				continue
			}
			if s.User != "" && (parts[1] != s.User || parts[2] != s.Password) {
				reply(535, "5.7.8 authentication failed")
				continue
			}
			user = parts[1]
			reply(235, "2.7.0 authenticated")
		case "MAIL":
			if s.User != "" && user == "" {
				reply(530, "5.7.0 authentication required")
				continue
			}
			from = addrArg(arg)
			to = nil
			reply(250, "OK")
		case "RCPT":
			if from == "" {
				reply(503, "MAIL first")
				continue
			}
			to = append(to, addrArg(arg))
			reply(250, "OK")
		case "DATA":
			if len(to) == 0 {
				reply(503, "RCPT first")
				continue
			}
			_ = tp.PrintfLine("354 end with <CRLF>.<CRLF>")
			data, err := io.ReadAll(tp.DotReader())
			if err != nil {
				return
			}
			s.store(SimMessage{From: from, To: to, Data: data, Auth: user})
			from, to = "", nil
			reply(250, "OK queued")
		case "RSET":
			from, to = "", nil
			reply(250, "OK")
		case "NOOP":
			reply(250, "OK")
		case "QUIT":
			reply(221, "bye")
			return
		default:
			reply(502, "command not implemented")
		}
	}
}

// addrArg takes the address out of "FROM:<a@b>" or "TO:<a@b> ...".
func addrArg(arg string) string {
	if i := strings.IndexByte(arg, '<'); i >= 0 {
		if j := strings.IndexByte(arg[i:], '>'); j > 0 {
			return arg[i+1 : i+j]
		}
	}
	_, v, _ := strings.Cut(arg, ":")
	return strings.TrimSpace(v)
}

func (s *SimServer) store(m SimMessage) {
	s.mu.Lock()
	s.received = append(s.received, m)
	n := len(s.received)
	s.mu.Unlock()
	if s.Dir != "" {
		name := filepath.Join(s.Dir, fmt.Sprintf("%s-%d.eml", time.Now().Format("20060102-150405"), n))
		if err := os.MkdirAll(s.Dir, 0o755); err == nil {
			_ = os.WriteFile(name, m.Data, 0o644)
		}
	}
	if s.Log != nil {
		s.Log("SMTP mail from %s to %s, %d bytes", m.From, strings.Join(m.To, ", "), len(m.Data))
	}
}

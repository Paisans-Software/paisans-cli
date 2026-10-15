// Package smtptest is an in-process SMTP server for tests of the mail checks:
// enough of RFC 5321, STARTTLS and AUTH PLAIN and LOGIN to be logged in to and
// sent one message, with a certificate authority of its own. No test reaches
// a real mail server or a real site.
package smtptest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
)

// Message is one message the server accepted.
type Message struct {
	// Site and Address are what the check dialled.
	Site, Address string
	From          string
	To            []string
	// Data is the message as sent, headers and body, CRLF line ends.
	Data string
}

// Dial is one connection a check asked for.
type Dial struct{ Site, Address string }

// Server answers every connection the Env it hands out dials.
type Server struct {
	// Users is every username and the password it logs in with. Nil
	// accepts any.
	Users map[string]string
	// Security is how the server at an address speaks, by host:port:
	// starttls, tls or none. An address it does not name speaks starttls.
	Security map[string]string
	// CertName, when set, is the name every certificate is issued for, in
	// place of the name the client asks for: a certificate for the wrong
	// host.
	CertName string
	// Untrusted serves certificates from an authority Roots does not hold.
	Untrusted bool
	// NoSTARTTLS is a server that does not offer STARTTLS.
	NoSTARTTLS bool
	// RejectFrom refuses a sender at MAIL FROM.
	RejectFrom map[string]bool
	// Unreachable is the sites a dial fails for, as an ssh that cannot
	// reach them fails, with err.
	Unreachable map[string]error

	ca, other *authority

	mu       sync.Mutex
	messages []Message
	dials    []Dial
	logins   []string
}

// New is a server with its own certificate authority.
func New() *Server {
	return &Server{ca: newAuthority("smtptest CA"), other: newAuthority("smtptest untrusted CA")}
}

// Env is a check's Env reaching this server: every dial, whatever the site
// and address, is answered by it, and Roots trusts its authority.
func (s *Server) Env() smtpcheck.Env {
	pool := x509.NewCertPool()
	pool.AddCert(s.ca.cert)
	return smtpcheck.Env{
		Dial:    s.Dial,
		Roots:   pool,
		Timeout: 10 * time.Second,
		Now:     func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) },
	}
}

// Dial answers one connection on a loopback listener of its own.
func (s *Server) Dial(site, address string) (net.Conn, error) {
	s.mu.Lock()
	s.dials = append(s.dials, Dial{Site: site, Address: address})
	err := s.Unreachable[site]
	security := s.Security[address]
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if security == "" {
		security = config.SMTPStartTLS
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		defer l.Close()
		c, err := l.Accept()
		if err != nil {
			return
		}
		s.serve(c, site, address, security)
	}()
	return net.Dial("tcp", l.Addr().String())
}

// Messages is every message accepted, in order.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Dials is every connection asked for, in order.
func (s *Server) Dials() []Dial {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Dial(nil), s.dials...)
}

// Logins is the username of every successful AUTH, in order.
func (s *Server) Logins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.logins...)
}

func (s *Server) tlsConfig() *tls.Config {
	ca := s.ca
	if s.Untrusted {
		ca = s.other
	}
	return &tls.Config{
		MinVersion:             tls.VersionTLS12,
		SessionTicketsDisabled: true,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if s.CertName != "" {
				name = s.CertName
			}
			return ca.leaf(name)
		},
	}
}

func (s *Server) serve(conn net.Conn, site, address, security string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	secure := false
	if security == config.SMTPTLS {
		tc := tls.Server(conn, s.tlsConfig())
		if err := tc.Handshake(); err != nil {
			return
		}
		conn, secure = tc, true
	}
	tp := textproto.NewConn(conn)
	reply := func(format string, args ...any) bool { return tp.PrintfLine(format, args...) == nil }
	if !reply("220 smtptest ESMTP") {
		return
	}
	var msg *Message
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"smtptest"}
			if security == config.SMTPStartTLS && !secure && !s.NoSTARTTLS {
				lines = append(lines, "STARTTLS")
			}
			if secure || security == config.SMTPNone {
				lines = append(lines, "AUTH PLAIN LOGIN")
			}
			lines = append(lines, "8BITMIME")
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				if !reply("250%s%s", sep, l) {
					return
				}
			}
		case "STARTTLS":
			if security != config.SMTPStartTLS || secure || s.NoSTARTTLS {
				reply("502 5.5.1 not offered")
				continue
			}
			if !reply("220 2.0.0 ready") {
				return
			}
			tc := tls.Server(conn, s.tlsConfig())
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, secure = tc, true
			tp = textproto.NewConn(conn)
		case "AUTH":
			user, pass, ok := s.auth(tp, arg)
			if !ok {
				return
			}
			if want, known := s.Users[user]; s.Users != nil && (!known || want != pass) {
				reply("535 5.7.8 Authentication credentials invalid")
				continue
			}
			s.mu.Lock()
			s.logins = append(s.logins, user)
			s.mu.Unlock()
			reply("235 2.7.0 Authentication successful")
		case "MAIL":
			from := addressOf(arg)
			if s.RejectFrom[from] {
				reply("550 5.7.1 Sender address rejected: not owned by user")
				continue
			}
			msg = &Message{Site: site, Address: address, From: from}
			reply("250 2.1.0 Ok")
		case "RCPT":
			if msg == nil {
				reply("503 5.5.1 MAIL first")
				continue
			}
			msg.To = append(msg.To, addressOf(arg))
			reply("250 2.1.5 Ok")
		case "DATA":
			if msg == nil || len(msg.To) == 0 {
				reply("503 5.5.1 RCPT first")
				continue
			}
			if !reply("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			lines, err := tp.ReadDotLines()
			if err != nil {
				return
			}
			msg.Data = strings.Join(lines, "\r\n") + "\r\n"
			s.mu.Lock()
			s.messages = append(s.messages, *msg)
			s.mu.Unlock()
			msg = nil
			reply("250 2.0.0 Ok: queued")
		case "QUIT":
			reply("221 2.0.0 Bye")
			return
		case "RSET", "NOOP":
			reply("250 2.0.0 Ok")
		default:
			reply("502 5.5.2 Error: command not recognized")
		}
	}
}

// auth reads one AUTH exchange, PLAIN or LOGIN, and returns the username and
// password given. ok is false when the connection failed.
func (s *Server) auth(tp *textproto.Conn, arg string) (user, pass string, ok bool) {
	mechanism, initial, _ := strings.Cut(arg, " ")
	read := func(challenge string) (string, bool) {
		if tp.PrintfLine("334 %s", challenge) != nil {
			return "", false
		}
		line, err := tp.ReadLine()
		if err != nil {
			return "", false
		}
		data, _ := base64.StdEncoding.DecodeString(line)
		return string(data), true
	}
	switch strings.ToUpper(mechanism) {
	case "PLAIN":
		var data string
		if initial != "" {
			raw, _ := base64.StdEncoding.DecodeString(initial)
			data = string(raw)
		} else if data, ok = read(""); !ok {
			return "", "", false
		}
		parts := strings.SplitN(data, "\x00", 3)
		if len(parts) == 3 {
			return parts[1], parts[2], true
		}
		return "", "", true
	case "LOGIN":
		if user, ok = read(base64.StdEncoding.EncodeToString([]byte("Username:"))); !ok {
			return "", "", false
		}
		if pass, ok = read(base64.StdEncoding.EncodeToString([]byte("Password:"))); !ok {
			return "", "", false
		}
		return user, pass, true
	}
	return "", "", true
}

func addressOf(arg string) string {
	_, rest, _ := strings.Cut(arg, ":")
	rest = strings.TrimSpace(rest)
	if i := strings.IndexByte(rest, '>'); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimPrefix(rest, "<")
}

// authority issues a certificate for any name, as a test needs.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	mu   sync.Mutex
	made map[string]*tls.Certificate
}

func newAuthority(name string) *authority {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return &authority{cert: cert, key: key, made: map[string]*tls.Certificate{}}
}

func (a *authority) leaf(name string) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.made[name]; ok {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{name}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, fmt.Errorf("smtptest: issuing for %s: %w", name, err)
	}
	c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	a.made[name] = c
	return c, nil
}

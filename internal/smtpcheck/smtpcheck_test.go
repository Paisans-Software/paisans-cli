package smtpcheck_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"testing"

	_ "github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck/smtptest"
)

const password = "not-a-real-smtp-password"

func pinned(blueprint config.Blueprint, site string, smtp *config.SMTP) config.App {
	return config.App{Blueprint: blueprint, Placement: config.Placement{Mode: config.PlacementPinned, Site: site}, SMTP: smtp}
}

// deployment is two sites, three apps that send mail through one server
// with one login, one of them on the other site, and one app with a sender
// of its own.
func deployment() (*config.Config, *config.Secrets) {
	cfg := &config.Config{
		Community: config.Community{Name: "Example Community", Domain: "example.org"},
		Sites:     map[string]config.Site{"home-a": {}, "home-b": {}},
		SMTP: config.SMTP{
			Host: "smtp.example.org", Security: config.SMTPStartTLS,
			Username: "robot@example.org", FromAddress: "hello@example.org", FromName: "Example Community",
		},
		Apps: map[string]config.App{
			"account": pinned(config.BlueprintAccount, "home-a", nil),
			"health":  pinned(config.BlueprintUptime, "home-a", &config.SMTP{FromName: "Example Health"}),
			"id":      pinned(config.BlueprintPocketID, "home-b", &config.SMTP{FromAddress: "id@example.org"}),
		},
		Operators: map[string]config.Operator{
			"jane": {Name: "Jane Doe", Email: "jane@example.org"},
		},
	}
	secrets := &config.Secrets{External: map[string]string{"smtp_password": password}}
	return cfg, secrets
}

func TestLoginsShareOneCheckPerSiteAndLogin(t *testing.T) {
	cfg, secrets := deployment()
	checks := smtpcheck.Logins(cfg, secrets, nil)
	if len(checks) != 2 {
		t.Fatalf("checks = %+v, want one per site", checks)
	}
	if c := checks[0]; c.Site != "home-a" || !slices.Equal(c.Apps, []string{"account", "health"}) || c.PasswordPath != "external.smtp_password" || c.Skip != "" {
		t.Errorf("home-a's check = %+v", c)
	}
	if c := checks[1]; c.Site != "home-b" || !slices.Equal(c.Apps, []string{"id"}) {
		t.Errorf("home-b's check = %+v", c)
	}
	if only := smtpcheck.Logins(cfg, secrets, []string{"home-b"}); len(only) != 1 || only[0].Site != "home-b" {
		t.Errorf("Logins for home-b = %+v", only)
	}
}

func TestLoginsSkipWhatTheHostCannotReach(t *testing.T) {
	cfg, secrets := deployment()
	cfg.Apps["health"] = pinned(config.BlueprintUptime, "home-a", &config.SMTP{Host: "relay", Security: config.SMTPNone, Username: "-"})
	secrets.External = nil
	for _, c := range smtpcheck.Logins(cfg, secrets, []string{"home-a"}) {
		switch {
		case slices.Contains(c.Apps, "health") && !strings.Contains(c.Skip, "only the app's own network"):
			t.Errorf("a compose service name is checked: %+v", c)
		case slices.Contains(c.Apps, "account") && !strings.Contains(c.Skip, "external.smtp_password is not set"):
			t.Errorf("a login with no password is checked: %+v", c)
		}
	}
}

func TestForPasswordIsOneCheckPerSender(t *testing.T) {
	cfg, secrets := deployment()
	checks := smtpcheck.ForPassword(cfg, secrets, "external.smtp_password")
	if len(checks) != 2 {
		t.Fatalf("checks = %+v, want one per sender", checks)
	}
	if c := checks[0]; c.Site != "" || !slices.Equal(c.Apps, []string{"account", "health"}) {
		t.Errorf("first = %+v", c)
	}
	if c := checks[1]; c.Site != "" || c.SMTP.FromAddress != "id@example.org" {
		t.Errorf("second = %+v", c)
	}
	// A password is tried from the machine the command runs on, not a
	// site: no site need be deployed, or even reachable, to set one.
	if title := checks[0].Title(); !strings.HasSuffix(title, ", from this machine") {
		t.Errorf("title = %q", title)
	}
	// An app's own password, once set, is its own path's alone.
	secrets.Apps = map[string]map[string]any{"id": {"smtp_password": "own"}}
	if own := smtpcheck.ForPassword(cfg, secrets, "apps.id.smtp_password"); len(own) != 1 || !slices.Equal(own[0].Apps, []string{"id"}) {
		t.Errorf("apps.id.smtp_password checks = %+v", own)
	}
	if shared := smtpcheck.ForPassword(cfg, secrets, "external.smtp_password"); len(shared) != 1 {
		t.Errorf("external.smtp_password still checks id: %+v", shared)
	}
}

// An app placed on no site still has its password tried, since the try
// runs from this machine.
func TestForPasswordTriesAnAppOnNoSite(t *testing.T) {
	cfg, secrets := deployment()
	for name, app := range cfg.Apps {
		app.Placement = config.Placement{Mode: config.PlacementPinned, Site: "nowhere"}
		cfg.Apps[name] = app
	}
	for _, c := range smtpcheck.ForPassword(cfg, secrets, "external.smtp_password") {
		if c.Skip != "" || c.Site != "" {
			t.Errorf("check = %+v, want one tried from this machine", c)
		}
	}
}

func check(security string) smtpcheck.Check {
	cfg, _ := deployment()
	s := cfg.SMTP
	s.Security = security
	return smtpcheck.Check{Site: "home-a", Apps: []string{"account"}, SMTP: s, PasswordPath: "external.smtp_password"}
}

func TestLoginEachSecurity(t *testing.T) {
	for _, security := range []string{config.SMTPStartTLS, config.SMTPTLS, config.SMTPNone} {
		t.Run(security, func(t *testing.T) {
			server := smtptest.New()
			server.Users = map[string]string{"robot@example.org": password}
			c := check(security)
			server.Security = map[string]string{c.Address(): security}
			if err := smtpcheck.Login(server.Env(), c, password); err != nil {
				t.Fatal(err)
			}
			if got := server.Logins(); !slices.Equal(got, []string{"robot@example.org"}) {
				t.Errorf("logins = %v", got)
			}
			if d := server.Dials(); len(d) != 1 || d[0].Site != "home-a" || d[0].Address != c.Address() {
				t.Errorf("dials = %+v", d)
			}
			if len(server.Messages()) != 0 {
				t.Error("a login sent a message")
			}
		})
	}
}

func TestLoginRefusedSaysTheReplyAndNotThePassword(t *testing.T) {
	server := smtptest.New()
	server.Users = map[string]string{"robot@example.org": "the-right-one"}
	err := smtpcheck.Login(server.Env(), check(config.SMTPStartTLS), password)
	var e *smtpcheck.Error
	if !errors.As(err, &e) || e.Stage != "log in" || !e.Dialled() {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "535 5.7.8") {
		t.Errorf("the server's reply is missing: %v", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("the error carries the password: %v", err)
	}
}

func TestCertificateFailures(t *testing.T) {
	for name, set := range map[string]func(*smtptest.Server){
		"untrusted":  func(s *smtptest.Server) { s.Untrusted = true },
		"wrong name": func(s *smtptest.Server) { s.CertName = "other.example.net" },
	} {
		for _, security := range []string{config.SMTPStartTLS, config.SMTPTLS} {
			t.Run(name+"/"+security, func(t *testing.T) {
				server := smtptest.New()
				set(server)
				c := check(security)
				server.Security = map[string]string{c.Address(): security}
				err := smtpcheck.Login(server.Env(), c, password)
				if err == nil || !strings.Contains(err.Error(), "certificate") {
					t.Fatalf("err = %v, want a certificate failure", err)
				}
				if len(server.Logins()) != 0 {
					t.Error("logged in over a connection whose certificate failed")
				}
			})
		}
	}
}

func TestStartTLSNotOffered(t *testing.T) {
	server := smtptest.New()
	server.NoSTARTTLS = true
	err := smtpcheck.Login(server.Env(), check(config.SMTPStartTLS), password)
	if err == nil || !strings.Contains(err.Error(), "does not offer STARTTLS") {
		t.Fatalf("err = %v", err)
	}
}

func TestSiteNotReached(t *testing.T) {
	server := smtptest.New()
	server.Unreachable = map[string]error{"home-a": errors.New("ssh could not connect")}
	err := smtpcheck.Login(server.Env(), check(config.SMTPStartTLS), password)
	var e *smtpcheck.Error
	if !errors.As(err, &e) || e.Dialled() {
		t.Fatalf("err = %v, want one that never dialled", err)
	}
	// The stage reads the same from a site and from this machine, where
	// there is no site to reach.
	if got := e.Reply(); !strings.HasPrefix(got, "connect: ") {
		t.Errorf("reply %q, want the connect stage", got)
	}
}

// TryPassword goes past a check whose server cannot be reached from where it
// is dialled, and says which ones: nothing was learned there, so it is not a
// failure. Every other check is still made.
func TestTryPasswordGoesPastAServerNotReached(t *testing.T) {
	server := smtptest.New()
	server.Users = map[string]string{"robot@example.org": password}
	server.Unreachable = map[string]error{"": errors.New("dial tcp: connection refused")}
	cfg, s := deployment()
	tried, err := smtpcheck.TryPassword(server.Env(), cfg, s, "external.smtp_password", password, "paisans secrets rotate", false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(tried.Unreached) == 0 || len(tried.Made) != 0 {
		t.Fatalf("tried %+v", tried)
	}
	for _, u := range tried.Unreached {
		if u.Dialled() || u.Check.From() != "this machine" {
			t.Errorf("unreached %v", u)
		}
	}
}

func TestSendTheTestMessage(t *testing.T) {
	server := smtptest.New()
	cfg, _ := deployment()
	c := check(config.SMTPStartTLS)
	c.Apps = []string{"account", "health"}
	m := smtpcheck.TestMail(cfg, c, "external.smtp_password", "paisans secrets set", []string{"jane@example.org", "john@example.org"})
	if err := smtpcheck.Send(server.Env(), c, password, m); err != nil {
		t.Fatal(err)
	}
	got := server.Messages()
	if len(got) != 1 {
		t.Fatalf("messages = %+v", got)
	}
	msg := got[0]
	if msg.From != "hello@example.org" || !slices.Equal(msg.To, []string{"jane@example.org", "john@example.org"}) {
		t.Errorf("envelope from %s to %v", msg.From, msg.To)
	}
	for _, want := range []string{
		`From: "Example Community" <hello@example.org>`,
		"To: <jane@example.org>, <john@example.org>",
		"Subject: Paisans SMTP test: external.smtp_password for example.org",
		"This is a Paisans SMTP test for Example Community (example.org).",
		"account and health send mail through smtp.example.org:587",
	} {
		if !strings.Contains(msg.Data, want) {
			t.Errorf("message lacks %q:\n%s", want, msg.Data)
		}
	}
	if strings.Contains(msg.Data, password) {
		t.Error("the message carries the password")
	}
}

func TestSendRefusedSender(t *testing.T) {
	server := smtptest.New()
	server.RejectFrom = map[string]bool{"hello@example.org": true}
	cfg, _ := deployment()
	c := check(config.SMTPStartTLS)
	err := smtpcheck.Send(server.Env(), c, password, smtpcheck.TestMail(cfg, c, "external.smtp_password", "paisans secrets set", []string{"jane@example.org"}))
	if err == nil || !strings.Contains(err.Error(), "send from hello@example.org") || !strings.Contains(err.Error(), "550") {
		t.Fatalf("err = %v", err)
	}
}

// A connection that closes before the server sends a byte learned nothing
// about the server: ssh's stdio forward opens, then reports at the first
// read that the host could not open the connection. The check is one whose
// connection was not made, not a server that answered and refused.
func TestLoginClosedBeforeAnyReplyIsNotDialled(t *testing.T) {
	why := fmt.Errorf("home-b to 10.44.0.1:587: ssh: Stdio forwarding request failed: Session open refused by peer: %w", io.EOF)
	for _, security := range []string{config.SMTPStartTLS, config.SMTPTLS, config.SMTPNone} {
		for _, readErr := range []error{io.EOF, why} {
			t.Run(security, func(t *testing.T) {
				env := smtpcheck.Env{Dial: func(string, string) (net.Conn, error) {
					local, remote := net.Pipe()
					go func() {
						_, _ = io.Copy(io.Discard, remote)
					}()
					return closedConn{Conn: local, err: readErr}, nil
				}}
				err := smtpcheck.Login(env, check(security), password)
				var failed *smtpcheck.Error
				if !errors.As(err, &failed) || failed.Dialled() || !failed.Unanswered() {
					t.Fatalf("err = %v, want a check whose connection was not made", err)
				}
				if !strings.HasPrefix(failed.Reply(), "connect: ") {
					t.Errorf("reply = %q", failed.Reply())
				}
				if readErr == why && !strings.Contains(failed.Reply(), "Session open refused by peer") {
					t.Errorf("reply %q drops ssh's reason", failed.Reply())
				}
			})
		}
	}
}

// closedConn is a connection whose every read fails with err.
type closedConn struct {
	net.Conn
	err error
}

func (c closedConn) Read([]byte) (int, error) { return 0, c.err }

// Package smtpcheck tries an app's mail settings against its mail server, from
// a site that runs the app: connect, TLS or STARTTLS as declared, and AUTH
// with the username and password, and, for a credential just changed, one
// test message to the operators.
//
// The connection is dialled from the site, through ssh (Env.Dial), so what is
// tried is the host's own route to the server: its egress, its resolver, and
// a relay reachable only on the mesh or a private network. The SMTP
// conversation itself runs here, on the workstation, so nothing is installed
// on a host and the password never reaches one outside the files apply
// renders. See "Mail settings are tried before they are used" in README.md.
package smtpcheck

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Env is how a check reaches a mail server.
type Env struct {
	// Dial opens a TCP connection to address, host:port, from site: the
	// toolkit's own ssh to the site, forwarding to address as the host
	// resolves and routes it. Tests dial an in-process server.
	Dial func(site, address string) (net.Conn, error)
	// Roots verifies the server's certificate. Nil is the workstation's
	// system roots, the trust a mail client on the internet has.
	Roots *x509.CertPool
	// Timeout bounds one check, from the dial to QUIT. Zero is
	// DefaultTimeout.
	Timeout time.Duration
	// Now dates the test message. Nil is time.Now.
	Now func() time.Time
	// NotOpened reports whether an error read from a connection Dial
	// returned is Dial's own report that the connection was never opened,
	// Eg: ssh's stdio forward, which starts before the host tries the
	// address and says at the first read that the host could not open it.
	// Nil is a Dial whose connection is open once it returns, as a direct
	// TCP dial's is: anything it reads after that came from the server.
	NotOpened func(error) bool
}

// DefaultTimeout bounds one check. A server that answers at all greets, takes
// TLS and answers AUTH within seconds; a minute leaves room for a slow
// greeting delay, which some servers add on purpose.
const DefaultTimeout = time.Minute

// Check is one set of mail settings, tried from one site.
type Check struct {
	// Site is where the connection is dialled from: a site running every
	// app in Apps, or "" for the machine the command runs on.
	Site string
	// Apps send mail with these settings, in configuration order.
	Apps []string
	// SMTP is their effective settings (config.SMTPFor).
	SMTP config.SMTP
	// PasswordPath is the secret they log in with, Eg:
	// external.smtp_password, or "" when the settings name no username and
	// the apps send without logging in.
	PasswordPath string
	// Skip is why the check is not made, or "".
	Skip string
}

// Address is the server as host:port.
func (c Check) Address() string {
	return net.JoinHostPort(c.SMTP.Host, strconv.Itoa(c.SMTP.PortOrDefault()))
}

// Security is the declared security, starttls when none is declared, as the
// apps read it.
func (c Check) Security() string {
	if c.SMTP.Security == "" {
		return config.SMTPStartTLS
	}
	return c.SMTP.Security
}

// Title names the check on one line: the server, how it is reached, and from
// where, Eg: "smtp.example.org:587, starttls, as robot@example.org, from
// home-a".
func (c Check) Title() string {
	who := ""
	if c.SMTP.Username != "" {
		who = ", as " + c.SMTP.Username
	}
	return fmt.Sprintf("%s, %s%s, from %s", c.Address(), c.Security(), who, c.From())
}

// From names where the check is dialled from: its site, or this machine.
func (c Check) From() string {
	if c.Site == "" {
		return "this machine"
	}
	return c.Site
}

// Whose is the apps, as a message names them, Eg: "talk's mail" or "talk and
// health's mail".
func (c Check) Whose() string {
	if len(c.Apps) == 0 {
		return "the mail"
	}
	return appList(c.Apps) + "'s mail"
}

// appList is apps as a sentence names them, Eg: "talk, wiki and health".
func appList(apps []string) string {
	if n := len(apps); n > 1 {
		return strings.Join(apps[:n-1], ", ") + " and " + apps[n-1]
	}
	return strings.Join(apps, "")
}

// Setting is where the settings are declared, for a message: smtp, or
// apps.<app>.smtp for an app that overrides any field.
func (c Check) Setting(cfg *config.Config) string {
	var keys []string
	for _, app := range c.Apps {
		key := "smtp"
		if cfg.Apps[app].SMTP != nil {
			key = "apps." + app + ".smtp"
		}
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "smtp"
	}
	return strings.Join(keys, ", ")
}

// PasswordPath is the secret app logs in to its SMTP server with: its own
// apps.<app>.smtp_password when set, else external.smtp_password, the same
// choice render makes; or "" when its settings name no username.
func PasswordPath(cfg *config.Config, secrets *config.Secrets, app string) string {
	if cfg.SMTPFor(app).Username == "" {
		return ""
	}
	if own, _ := secrets.Apps[app]["smtp_password"].(string); own != "" {
		return "apps." + app + ".smtp_password"
	}
	return "external.smtp_password"
}

// Logins is every check apply and validate make: each set of mail settings an
// app sends with, from each of sites that runs such an app, or from every site
// for nil. Apps whose settings and password are the same on one site share a
// check, since they make the same connection; the same settings on two sites
// are two checks, since each host has its own route to the server.
func Logins(cfg *config.Config, secrets *config.Secrets, sites []string) []Check {
	placed := render.AppSites(cfg)
	var out []Check
	for _, site := range cfg.SiteNames() {
		if sites != nil && !slices.Contains(sites, site) {
			continue
		}
		for _, app := range cfg.AppNames() {
			if !blueprints.SendsMail(cfg.Apps[app].Blueprint) || !slices.Contains(placed[app], site) {
				continue
			}
			s := cfg.SMTPFor(app)
			if s.Host == "" {
				continue
			}
			c := Check{Site: site, Apps: []string{app}, SMTP: s, PasswordPath: PasswordPath(cfg, secrets, app)}
			if i := slices.IndexFunc(out, func(o Check) bool { return o.Site == site && sameLogin(o, c) }); i >= 0 {
				out[i].Apps = append(out[i].Apps, app)
				continue
			}
			out = append(out, c)
		}
	}
	for i := range out {
		out[i].Skip = skipReason(out[i], secrets)
	}
	return out
}

// ForPassword is the checks a new value of the secret at path is tried with
// before it is written or handed out: one for each set of settings an app
// logs in with that password under, with its sender, since the test message
// goes from each sender the provider must accept. Each is dialled from the
// machine the command runs on: whether the provider takes the password and
// the sender does not depend on a site, and none need be deployed yet;
// apply tries the login from each site, which is whether the site reaches
// the server. secrets must hold the new value at path, so that an app's own
// password, just set, is the one it uses.
func ForPassword(cfg *config.Config, secrets *config.Secrets, path string) []Check {
	var out []Check
	for _, app := range cfg.AppNames() {
		if !blueprints.SendsMail(cfg.Apps[app].Blueprint) {
			continue
		}
		s := cfg.SMTPFor(app)
		if s.Host == "" || PasswordPath(cfg, secrets, app) != path {
			continue
		}
		c := Check{Apps: []string{app}, SMTP: s, PasswordPath: path}
		if i := slices.IndexFunc(out, func(o Check) bool { return sameLogin(o, c) && o.SMTP.FromAddress == c.SMTP.FromAddress }); i >= 0 {
			out[i].Apps = append(out[i].Apps, app)
			continue
		}
		out = append(out, c)
	}
	for i := range out {
		out[i].Skip = skipReason(out[i], secrets)
	}
	return out
}

func sameLogin(a, b Check) bool {
	return a.SMTP.Host == b.SMTP.Host && a.SMTP.PortOrDefault() == b.SMTP.PortOrDefault() &&
		a.Security() == b.Security() && a.SMTP.Username == b.SMTP.Username && a.PasswordPath == b.PasswordPath
}

// skipReason is why c cannot be made, or "". A host that only the app's own
// container resolves, a compose service name or a loopback address, is not
// one the site's host, or this machine, reaches. A username with no password
// set has nothing to log in with; init and apply already name that secret
// as owed.
func skipReason(c Check, secrets *config.Secrets) string {
	host := strings.TrimSuffix(c.SMTP.Host, ".")
	where := "this machine"
	if c.Site != "" {
		where = "the host of " + c.Site
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() || strings.EqualFold(host, "localhost") {
		return fmt.Sprintf("%s is the app's own container, which %s does not reach", c.SMTP.Host, where)
	}
	if net.ParseIP(host) == nil && !strings.Contains(host, ".") {
		return fmt.Sprintf("%s is a name only the app's own network resolves, which %s does not reach", c.SMTP.Host, where)
	}
	if c.PasswordPath != "" {
		if v, _ := secrets.Get(c.PasswordPath); v == "" {
			return c.PasswordPath + " is not set, so there is no password to log in with"
		}
	}
	return ""
}

// Mail is the test message a changed credential sends.
type Mail struct {
	To      []string
	Subject string
	Body    string
}

// TestMail is the message sent when the password at path is changed by
// command, Eg: `paisans secrets set`: plain text, naming the deployment, the
// apps and the server, and that it is a Paisans SMTP test. The password is
// not in it.
func TestMail(cfg *config.Config, c Check, path, command string, to []string) Mail {
	deployment := cfg.Community.Domain
	if name := strings.TrimSpace(cfg.Community.Name); name != "" {
		deployment = fmt.Sprintf("%s (%s)", name, cfg.Community.Domain)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This is a Paisans SMTP test for %s.\n\n", deployment)
	fmt.Fprintf(&b, "`%s` is changing %s, and tries it before any app is handed it.\n", command, path)
	verb := "sends"
	if len(c.Apps) > 1 {
		verb = "send"
	}
	fmt.Fprintf(&b, "%s %s mail through %s with %s, as %s, from %s.\n", appList(c.Apps), verb, c.Address(), c.Security(), c.SMTP.Username, c.SMTP.FromAddress)
	fmt.Fprintf(&b, "This message was sent from %s, so the server took the password and the sender.\n\n", c.From())
	b.WriteString("Nothing needs doing. The password is not in this message.\n")
	return Mail{
		To:      to,
		Subject: fmt.Sprintf("Paisans SMTP test: %s for %s", path, cfg.Community.Domain),
		Body:    b.String(),
	}
}

// Login connects as c declares and logs in with password, sending nothing.
func Login(e Env, c Check, password string) error {
	return converse(e, c, password, nil)
}

// Send is Login, then m sent from c's sender.
func Send(e Env, c Check, password string, m Mail) error {
	if len(m.To) == 0 {
		return errors.New("the test message has no recipient")
	}
	return converse(e, c, password, &m)
}

// Error is a check that failed: the stage it failed at, and the server's or
// the connection's reply. It never carries the password.
type Error struct {
	Check Check
	// Stage is what failed, Eg: "log in".
	Stage string
	Err   error
	// closed is a dial that reached the site, which could not open the
	// connection to the address (Unanswered).
	closed bool
}

func (e *Error) Error() string {
	return e.Check.Title() + ": " + e.Reply()
}

func (e *Error) Unwrap() error { return e.Err }

// Reply is the stage and the server's or the connection's answer, as the
// operator is told it, Eg: "log in: 535 5.7.8 Authentication credentials
// invalid". A server's reply is quoted as it sent it.
func (e *Error) Reply() string {
	text := e.Err.Error()
	var server *textproto.Error
	if errors.As(e.Err, &server) {
		text = fmt.Sprintf("%03d %s", server.Code, server.Msg)
	}
	return e.Stage + ": " + ui.Redact(strings.TrimSpace(text))
}

// Dialled reports whether the connection to the mail server was made:
// false when it could not be opened from where the check is dialled, a site
// or this machine, so nothing about the mail server was learned.
func (e *Error) Dialled() bool { return e.Stage != stageConnect }

// Unanswered reports whether the dial reached the site and the site could
// not open the connection to the address (Env.NotOpened): nothing answered
// there. It is one of the failures Dialled reports false for.
func (e *Error) Unanswered() bool { return e.closed }

// stageConnect is opening the connection, from a site or from this machine.
const stageConnect = "connect"

// converse is the conversation: dial from the site or this machine, TLS for tls, EHLO,
// STARTTLS for starttls, AUTH when a username is declared, and the message
// when there is one, then QUIT. Every step must succeed.
func converse(e Env, c Check, password string, m *Mail) error {
	if c.SMTP.Username != "" {
		ui.Secret(password)
	}
	fail := func(stage string, err error) error { return &Error{Check: c, Stage: stage, Err: err} }
	if e.Dial == nil {
		return fail(stageConnect, errors.New("no way to dial the mail server"))
	}
	timeout := e.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	conn, err := e.Dial(c.Site, c.Address())
	if err != nil {
		return fail(stageConnect, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	host := strings.TrimSuffix(c.SMTP.Host, ".")
	tlsConfig := &tls.Config{ServerName: host, RootCAs: e.Roots, MinVersion: tls.VersionTLS12}
	security := c.Security()
	if security == config.SMTPTLS {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.Handshake(); err != nil {
			if unanswered(e, err) {
				return &Error{Check: c, Stage: stageConnect, Err: explainConnect(err, security), closed: true}
			}
			return fail("TLS", explainTLS(explainConnect(err, security)))
		}
		conn = tc
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		if unanswered(e, err) {
			return &Error{Check: c, Stage: stageConnect, Err: explainConnect(err, security), closed: true}
		}
		return fail("greeting", explainConnect(err, security))
	}
	defer client.Close()
	if err := client.Hello("localhost"); err != nil {
		return fail("EHLO", err)
	}
	if security == config.SMTPStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fail("STARTTLS", errors.New("the server does not offer STARTTLS, and security: starttls sends nothing without it"))
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fail("STARTTLS", explainTLS(err))
		}
	}
	if c.SMTP.Username != "" {
		ok, mechanisms := client.Extension("AUTH")
		if !ok {
			return fail("log in", errors.New("the server offers no AUTH, so the username cannot log in"))
		}
		auth, err := pickAuth(mechanisms, c.SMTP.Username, password)
		if err != nil {
			return fail("log in", err)
		}
		if err := client.Auth(auth); err != nil {
			return fail("log in", err)
		}
	}
	if m != nil {
		if err := client.Mail(c.SMTP.FromAddress); err != nil {
			return fail("send from "+c.SMTP.FromAddress, err)
		}
		for _, to := range m.To {
			if err := client.Rcpt(to); err != nil {
				return fail("send to "+to, err)
			}
		}
		w, err := client.Data()
		if err != nil {
			return fail("send", err)
		}
		if _, err := io.WriteString(w, message(c, *m, now())); err != nil {
			return fail("send", err)
		}
		if err := w.Close(); err != nil {
			return fail("send", err)
		}
	}
	// Everything asked of the server has been answered, and the message,
	// when there is one, was accepted at the end of DATA: a QUIT answered
	// badly takes nothing back.
	_ = client.Quit()
	return nil
}

// message is m as sent: headers and the body, lines ended CRLF.
func message(c Check, m Mail, now time.Time) string {
	from := (&mail.Address{Name: oneLine(c.SMTP.FromName), Address: c.SMTP.FromAddress}).String()
	var to []string
	for _, addr := range m.To {
		to = append(to, (&mail.Address{Address: addr}).String())
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := c.SMTP.FromAddress[strings.LastIndexByte(c.SMTP.FromAddress, '@')+1:]
	var b strings.Builder
	for _, h := range [][2]string{
		{"From", from},
		{"To", strings.Join(to, ", ")},
		{"Subject", oneLine(m.Subject)},
		{"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", "<paisans-smtp-test." + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=UTF-8"},
		{"Content-Transfer-Encoding", "8bit"},
	} {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.TrimRight(m.Body, "\n"), "\n", "\r\n") + "\r\n")
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// explainTLS says what a TLS failure means for the operator: a certificate
// the workstation does not trust for that name is the common one.
func explainTLS(err error) error {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &unknown):
		return fmt.Errorf("the server's certificate is not signed by an authority this machine trusts (%v)", err)
	case errors.As(err, &hostname):
		return fmt.Errorf("the server's certificate is not for this name (%v); give the name the certificate carries as the host", err)
	case errors.As(err, &invalid):
		return fmt.Errorf("the server's certificate is not valid (%v)", err)
	}
	return err
}

// unanswered reports whether err is the dial's own report that the
// connection to the address was never opened (Env.NotOpened), so nothing
// about the mail server was learned. A connection that opened and then
// closed reached something at the address, which is the server's answer.
func unanswered(e Env, err error) bool {
	return e.NotOpened != nil && e.NotOpened(err)
}

// explainConnect says what a connection that closed before the greeting
// means, when nothing said why. When ssh said why, Eg: that the host could
// not connect to the address, its reason is the error's own.
func explainConnect(err error, security string) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		why := "the server took the connection and closed it before it greeted, Eg: a connection limit or an address filter"
		if security == config.SMTPTLS {
			why = "the server took the connection and closed it during the TLS handshake, Eg: a connection limit, an address filter, or a port that does not speak TLS from the first byte"
		} else {
			why += ", or the port expects TLS from the first byte (security: tls)"
		}
		return errors.New(why)
	}
	return err
}

// pickAuth is PLAIN when the server offers it, else LOGIN. Both send the
// password as it is, so they run only after TLS for tls and starttls; for
// none, the password crosses the private network the settings name, as the
// apps' own logins do.
func pickAuth(mechanisms, username, password string) (smtp.Auth, error) {
	offered := strings.Fields(strings.ToUpper(mechanisms))
	switch {
	case slices.Contains(offered, "PLAIN"):
		return plainAuth{username: username, password: password}, nil
	case slices.Contains(offered, "LOGIN"):
		return &loginAuth{username: username, password: password}, nil
	}
	return nil, fmt.Errorf("the server offers AUTH %s, and the check speaks PLAIN and LOGIN", mechanisms)
}

// plainAuth is RFC 4616's PLAIN. net/smtp's PlainAuth refuses a connection
// without TLS to anything but localhost; the declared security decides that
// here, and validate has already judged a plaintext host.
type plainAuth struct{ username, password string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge after PLAIN")
	}
	return nil, nil
}

// loginAuth is the LOGIN mechanism: the username, then the password, each
// in answer to a challenge.
type loginAuth struct {
	username, password string
	step               int
}

func (a *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	a.step = 0
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.username), nil
	case 2:
		return []byte(a.password), nil
	}
	return nil, errors.New("unexpected server challenge after LOGIN")
}

// Recipients is who a test message goes to: every operator the configuration
// declares, by their email, in the order of their names. A changed mail
// credential is every operator's business, and each of them can say whether
// the message arrived. None is an error, since a message with no recipient
// proves nothing.
func Recipients(cfg *config.Config) ([]string, error) {
	var names []string
	for name := range cfg.Operators {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []string
	for _, name := range names {
		if addr := strings.TrimSpace(cfg.Operators[name].Email); addr != "" && !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the configuration declares no operator with an email, and the test message goes to the operators. Declare them under operators, each with an email")
	}
	return out, nil
}

// Tried is the outcome of trying a password: the checks made, those
// skipped, with why, and those whose server could not be reached from where
// they were dialled, with the error.
type Tried struct {
	Made, Skipped []Check
	Unreached     []*Error
}

// TryPassword tries value as the password at path, as `secrets set` and
// `secrets rotate` do before it is written or handed out: every check
// ForPassword finds is made, logging in and, with send, sending the test
// message to the operators from each sender. secrets is copied, not changed.
// A check whose server cannot be reached from where it is dialled learned
// nothing, so it is listed in Unreached and the rest are still made. The
// first other failure is returned.
func TryPassword(e Env, cfg *config.Config, secrets *config.Secrets, path, value, command string, send bool) (Tried, error) {
	ui.Secret(value)
	probe := withValue(secrets, path, value)
	var tried Tried
	var to []string
	for _, c := range ForPassword(cfg, probe, path) {
		if c.Skip != "" {
			tried.Skipped = append(tried.Skipped, c)
			continue
		}
		var err error
		if send {
			if to == nil {
				if to, err = Recipients(cfg); err != nil {
					return tried, err
				}
			}
			err = Send(e, c, value, TestMail(cfg, c, path, command, to))
		} else {
			err = Login(e, c, value)
		}
		var failed *Error
		if errors.As(err, &failed) && !failed.Dialled() {
			tried.Unreached = append(tried.Unreached, failed)
			continue
		}
		if err != nil {
			return tried, err
		}
		tried.Made = append(tried.Made, c)
	}
	return tried, nil
}

// withValue is a copy of secrets holding value at path, deep enough that the
// original is left alone.
func withValue(s *config.Secrets, path, value string) *config.Secrets {
	c := *s
	c.External = map[string]string{}
	for k, v := range s.External {
		c.External[k] = v
	}
	c.Apps = map[string]map[string]any{}
	for name, entries := range s.Apps {
		m := map[string]any{}
		for k, v := range entries {
			m[k] = v
		}
		c.Apps[name] = m
	}
	_ = c.Set(path, value)
	return &c
}

// IsPasswordPath reports whether path is an SMTP password's place in the
// secrets file: external.smtp_password, or apps.<app>.smtp_password.
func IsPasswordPath(path string) bool {
	parts := strings.Split(path, ".")
	return path == "external.smtp_password" || len(parts) == 3 && parts[0] == "apps" && parts[1] != "" && parts[2] == "smtp_password"
}

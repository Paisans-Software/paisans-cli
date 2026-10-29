package render

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// nullDSN is Symfony Mailer's discard transport (NullTransportFactory at
// symfony/mailer v7.4.14), rendered when an app resolves no SMTP host. An
// image whose baked configuration names a mail host of its own would
// otherwise fail every mail it tried to send instead of dropping it.
const nullDSN = "null://null"

// SMTPDSN is the app's resolved SMTP settings as a Symfony Mailer DSN, or
// the null transport when no host resolves. It is not yet quoted for a .env;
// a template quotes it with envquote, because the password inside it is
// whatever the mail provider issued.
//
// security: tls is smtps://, which Symfony's EsmtpTransportFactory always
// connects with implicit TLS. starttls is smtp:// with require_tls=true:
// plain smtp:// upgrades with STARTTLS only when the server offers it and
// otherwise carries on in plaintext, and require_tls makes it refuse to send
// without TLS instead (EsmtpTransport.php at v7.4.14). none is smtp:// with
// auto_tls=false, which never sends STARTTLS (EsmtpTransportFactory.php at
// v7.4.14). The port is always written, since a DSN without one makes
// Symfony guess from the scheme.
func (v appValues) SMTPDSN() string { return mailerDSN(v.SMTP) }

// SMTPSender is the From address of the app's mail: the resolved
// from_address, else noreply at the app's own hostname, or the community's
// domain for an app with none, an address nothing sends from while no mail
// server resolves.
func (v appValues) SMTPSender() string {
	if v.SMTP.FromAddress != "" {
		return v.SMTP.FromAddress
	}
	if v.Hostname == "" {
		return "noreply@" + v.Domain
	}
	return "noreply@" + v.Hostname
}

// LockedSettings is the settings the app's blueprint locks, those with a
// condition only when it holds, joined by sep.
func (v appValues) LockedSettings(sep string) string {
	return strings.Join(blueprints.LockedSettingsFor(v.decl, v.OIDC.Present), sep)
}

func mailerDSN(s smtpValues) string {
	if s.Host == "" {
		return nullDSN
	}
	scheme, query := "smtp", "require_tls=true"
	switch {
	case s.Secure:
		scheme, query = "smtps", ""
	case s.Security == config.SMTPNone:
		query = "auto_tls=false"
	}
	out := scheme + "://"
	if s.Username != "" {
		out += escapeUserinfo(s.Username)
		if s.Password != "" {
			out += ":" + escapeUserinfo(s.Password)
		}
		out += "@"
	}
	out += net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	if query != "" {
		out += "?" + query
	}
	return out
}

// escapeUserinfo percent encodes every byte of a DSN's username or password
// that is not an RFC 3986 unreserved character. Symfony's Dsn::fromString
// splits the DSN with PHP's parse_url and then rawurldecodes each part, so a
// fully encoded value always comes back byte for byte, whatever the provider
// issued: an `@`, `:` or `/` would otherwise move the split, a `#` or `?`
// would end the authority, and a `%` would be decoded twice.
func escapeUserinfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

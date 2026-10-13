package render

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// mbinNullDSN is Symfony Mailer's discard transport (NullTransportFactory at
// symfony/mailer v7.4.14), rendered when an Mbin app resolves no SMTP host.
// Without a MAILER_DSN of its own the image's baked .env names
// smtp://mailserver, a host that does not exist here, so every mail Mbin
// tried to send would fail instead of being dropped.
const mbinNullDSN = "null://null"

// MailerDSN is the app's MAILER_DSN, built from its resolved SMTP settings,
// or the null transport when no host resolves. It is not yet quoted for the
// .env; the template quotes it with envquote, because the password inside it
// is whatever the mail provider issued.
//
// security: tls is smtps://, which Symfony's EsmtpTransportFactory always
// connects with implicit TLS. starttls is smtp:// with require_tls=true:
// plain smtp:// upgrades with STARTTLS only when the server offers it and
// otherwise carries on in plaintext, and require_tls makes it refuse to send
// without TLS instead (EsmtpTransport.php at v7.4.14). The port is always
// written, since a DSN without one makes Symfony guess from the scheme.
func (v appValues) MailerDSN() string { return mailerDSN(v.SMTP) }

// SMTPDSN is the app's resolved SMTP settings as a Symfony Mailer DSN, the
// helper any blueprint whose software reads one uses: MailerDSN by the name
// of what it is rather than of the app that first needed it.
func (v appValues) SMTPDSN() string { return mailerDSN(v.SMTP) }

// SMTPSender is the From address of the app's mail: the resolved
// from_address, else noreply at the app's own hostname, an address nothing
// sends from while no mail server resolves.
func (v appValues) SMTPSender() string {
	if v.SMTP.FromAddress != "" {
		return v.SMTP.FromAddress
	}
	return mbinFallbackAddress(v.Hostname)
}

// LockedSettings is the settings the app's blueprint locks, those with a
// condition only when it holds, joined by sep.
func (v appValues) LockedSettings(sep string) string {
	return strings.Join(kinds.LockedSettingsFor(v.decl, v.OIDC.Present), sep)
}

func mailerDSN(s smtpValues) string {
	if s.Host == "" {
		return mbinNullDSN
	}
	scheme, query := "smtp", "require_tls=true"
	if s.Secure {
		scheme, query = "smtps", ""
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

// MbinSenderEmail is KBIN_SENDER_EMAIL, the From address of every mail Mbin
// sends: the resolved from_address, else noreply at the app's own hostname,
// which is an address Mbin accepts and that nothing sends from while the
// transport is the null one. Mbin always pairs it with the instance domain as
// the display name, so from_name is not used.
func (v appValues) MbinSenderEmail() string {
	if v.SMTP.FromAddress != "" {
		return v.SMTP.FromAddress
	}
	return mbinFallbackAddress(v.Hostname)
}

// MbinContactEmail is KBIN_CONTACT_EMAIL, where the contact form sends to:
// settings.contact_email, else the sender, so a message reaches whoever
// reads the sending mailbox rather than nobody.
func (v appValues) MbinContactEmail() string {
	if s, ok := v.set[kinds.MbinContactEmailSetting].(string); ok && s != "" {
		return s
	}
	return v.MbinSenderEmail()
}

func mbinFallbackAddress(hostname string) string { return "noreply@" + hostname }

// mbinLockedSettings are the Mbin database settings this .env renders, so
// the configuration is their only source. The paisans fork reads each one
// named in MBIN_LOCKED_SETTINGS from the environment on every read, never
// writes it when an admin saves, shows it disabled in the admin form, and
// refuses it through the settings API. Every release since 1.15.0+paisans
// carries the feature, and a name it cannot lock makes it answer every request with an
// error, so each name here must be one the fork lists as lockable.
//
// Each name is both a key the template renders and a field of the fork's
// SettingsDto (src/DTO/SettingsDto.php), and TestMbinLockedSettingsAreRendered
// checks both directions against the rendered file, and every name against
// the fork's lockable list, so a setting added to the template without being
// locked here fails a test. A setting's environment
// variable is usually its own name; mbinSettingEnv lists the ones that are
// not, and the lock names the setting, never the variable.
func mbinLockedSettings(gated, sso bool) []string {
	out := []string{"KBIN_DOMAIN", "KBIN_SENDER_EMAIL", "KBIN_CONTACT_EMAIL", "MBIN_AUTHORIZED_FETCH"}
	if sso {
		out = append(out, "MBIN_SSO_ONLY_MODE")
	}
	if gated {
		out = append(out, "MBIN_PRIVATE_INSTANCE", "MBIN_USE_FEDERATION_ALLOW_LIST")
	}
	return out
}

// MbinLockedSettings is MBIN_LOCKED_SETTINGS, comma separated.
func (v appValues) MbinLockedSettings() string {
	return strings.Join(mbinLockedSettings(v.Gated, v.OIDC.Present), ",")
}

// mbinSettingEnv maps a Mbin database setting to the environment variable
// that feeds it, where the two names differ (config/services.yaml:135 at
// Paisans-Software/mbin develop: sso_only_mode reads SSO_ONLY_MODE).
var mbinSettingEnv = map[string]string{
	"MBIN_SSO_ONLY_MODE": "SSO_ONLY_MODE",
}

package validate

import (
	"fmt"
	"net/mail"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// beszelSMTP checks the mail settings setup.sh writes into Beszel's hub.
// README.md, "Server and container stats".
//
// PocketBase refuses settings whose meta.senderAddress is not an email
// address (core/settings_model.go:542 at v0.40.5), and setup.sh writes the
// sender with the SMTP settings, so a missing or malformed from_address
// fails that write and setup restarts on every start without converging.
//
// PocketBase's smtp.tls is implicit TLS when true; when false, the mailer
// upgrades with STARTTLS only if the server offers it (core/settings_model.go
// :372-376, tools/mailer/smtp.go:79-86, and mailyak v3.6.2 sender.go:60),
// with no setting to require it. Its PLAIN and LOGIN auth send no
// credentials over an unencrypted connection (tools/mailer/smtp.go:65-72 and
// :185-193), so a username makes the upgrade required in effect: without
// one, a connection whose STARTTLS was stripped delivers the alert in
// plaintext. That is warned, not refused, since it is a risk the relay's
// path decides rather than an incoherent configuration.
func (c *checker) beszelSMTP() {
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint != config.BlueprintBeszel {
			continue
		}
		s := c.cfg.SMTPFor(name)
		if s.Host == "" {
			continue
		}
		key := "smtp.from_address"
		if o := c.cfg.Apps[name].SMTP; o != nil && o.FromAddress != "" {
			key = fmt.Sprintf("apps.%s.smtp.from_address", name)
		}
		if s.FromAddress == "" {
			c.refuse("beszel-smtp-sender", key,
				"beszel smtp has no sender",
				"is not set, and %s's hub needs a sender address with its SMTP settings: PocketBase refuses the settings without one, so its mail would never be configured. Set from_address.", name)
		} else if addr, err := mail.ParseAddress(s.FromAddress); err != nil || addr.Address != s.FromAddress {
			c.refuse("beszel-smtp-sender", key,
				"beszel sender is not an email address",
				"is %q, which %s's hub is given as its sender address, and PocketBase refuses anything but an email address alone, Eg: status@example.org. Put a display name in from_name.", s.FromAddress, name)
		}
		security := s.Security
		if security == "" {
			security = config.SMTPStartTLS
		}
		if security == config.SMTPStartTLS && s.Username == "" {
			c.warn("beszel-starttls-not-enforced", fmt.Sprintf("apps.%s.smtp", name),
				"beszel cannot require starttls without a username",
				"resolves security: starttls with no username. Beszel's hub upgrades to TLS only when the server offers STARTTLS and has no setting to require it, so a connection whose STARTTLS is stripped on the way sends %s's alerts in plaintext. Use security: tls where the relay offers it, or a relay that takes a username.", name)
		}
	}
}

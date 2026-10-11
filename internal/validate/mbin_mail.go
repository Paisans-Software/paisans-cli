package validate

import (
	"fmt"
	"net/mail"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// mbinApps are the Mbin apps, in name order.
func (c *checker) mbinApps() []string {
	var out []string
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Kind == config.KindMbin {
			out = append(out, name)
		}
	}
	return out
}

// isAddressAlone reports whether s is an email address with nothing around
// it: no display name, no angle brackets. Symfony's Address refuses anything
// else (symfony/mime v7.4.13, Address.php), which is what Mbin builds every
// sender and recipient with.
func isAddressAlone(s string) bool {
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s
}

// mbinWithoutSMTP warns that an Mbin app with no mail server discards its
// mail. It renders Symfony's null transport, so nothing errors and the app
// works; what is lost is every contact form message and the few account
// mails Mbin still sends. A warning, not a refusal, for that reason.
func (c *checker) mbinWithoutSMTP() {
	for _, name := range c.mbinApps() {
		if c.cfg.SMTPFor(name).Host != "" {
			continue
		}
		c.warn("mbin-without-smtp", fmt.Sprintf("apps.%s.smtp", name),
			"mbin has no smtp host, so its mail is discarded",
			"resolves no SMTP host, so %s renders MAILER_DSN=null://null and discards every mail it sends: contact form messages, and the few account mails it still has (resending an activation link, a password reset). It works otherwise. Declare a top level smtp block, or one on this app.", name)
	}
}

// mbinSMTPSender checks the sender an Mbin app with a mail server is given.
// KBIN_SENDER_EMAIL is the From of every mail, and Mbin builds it with
// Symfony's Address, which throws on anything but an address alone, so every
// mail would fail: refused. No sender at all is only risky: Mbin sends as
// noreply at its own hostname, which dns init says sends no mail unless that
// hostname is a mail domain, so receivers are likely to reject it.
func (c *checker) mbinSMTPSender() {
	for _, name := range c.mbinApps() {
		smtp := c.cfg.SMTPFor(name)
		if smtp.Host == "" {
			continue
		}
		key := "smtp.from_address"
		if o := c.cfg.Apps[name].SMTP; o != nil && o.FromAddress != "" {
			key = fmt.Sprintf("apps.%s.smtp.from_address", name)
		}
		if smtp.FromAddress == "" {
			c.warn("mbin-smtp-without-sender", key,
				"mbin smtp has no sender",
				"is not set, so %s sends as noreply@%s. dns init publishes that name as sending no mail unless it is a mail domain, so receivers are likely to reject what it sends. Set from_address.",
				name, c.cfg.Apps[name].Hostname)
			continue
		}
		if !isAddressAlone(smtp.FromAddress) {
			c.refuse("mbin-smtp-from-not-an-email", key,
				"mbin sender is not an email address",
				"is %q, which %s renders as KBIN_SENDER_EMAIL, and Mbin fails to send any mail unless that is an email address alone, Eg: hello@example.org.",
				smtp.FromAddress, name)
		}
	}
}

// mbinContactEmail refuses a contact_email that is not an address alone.
// Mbin sends every contact form message to it, publishes it in /api/info,
// and uses it as its Web Push VAPID subject, and none of those can work with
// anything else.
func (c *checker) mbinContactEmail() {
	for _, name := range c.mbinApps() {
		raw, ok := c.cfg.Apps[name].Settings[kinds.MbinContactEmailSetting]
		if !ok {
			continue
		}
		if s, isString := raw.(string); isString && isAddressAlone(s) {
			continue
		}
		c.refuse("mbin-contact-email-not-an-email", fmt.Sprintf("apps.%s.settings.%s", name, kinds.MbinContactEmailSetting),
			"mbin contact address is not an email address",
			"is %v, and Mbin sends contact form messages to it, so it must be an email address alone, Eg: admins@example.org. Leave it out to use the sender address.", raw)
	}
}

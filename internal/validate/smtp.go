package validate

import (
	"fmt"
	"net"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// sharedAddressSpace is RFC 6598's carrier grade NAT range, which
// net.IP.IsPrivate leaves out and which mesh overlays commonly number from.
var sharedAddressSpace = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// plaintextHostIsPrivate reports whether host may receive plaintext SMTP: an
// IP literal in loopback, RFC 1918, RFC 6598 or IPv6 unique local space or
// inside the mesh, or a compose service name. Nothing is resolved. A name
// with a dot could resolve anywhere, and a resolver given a name made only
// of digits, or a hex one such as 0x08080808, may read it as an IPv4
// address, so a service name has no dot and starts with a letter.
func plaintextHostIsPrivate(host string, mesh config.Mesh) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || sharedAddressSpace.Contains(ip) || mesh.Contains(host)
	}
	return isServiceName(host)
}

func isServiceName(host string) bool {
	if host == "" || !isLetter(host[0]) {
		return false
	}
	for i := 1; i < len(host); i++ {
		c := host[i]
		if !isLetter(c) && !('0' <= c && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// smtpPlaintext checks every smtp block that resolves security: none, the
// deployment's and each mail sending app's own where it names the host or
// the security. A private host is accepted with a warning; anything else is
// refused. See "Mail is declared once and overridden per app" in README.md.
func (c *checker) smtpPlaintext() {
	if s := c.cfg.SMTP; s.Security == config.SMTPNone && s.Host != "" {
		c.judgePlaintext("smtp.host", s.Host, "the mail of every app that inherits this block")
	}
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		own := app.SMTP
		if own == nil || !blueprints.SendsMail(app.Blueprint) || (own.Host == "" && own.Security == "") {
			continue
		}
		s := c.cfg.SMTPFor(name)
		if s.Security != config.SMTPNone || s.Host == "" {
			continue
		}
		key := fmt.Sprintf("apps.%s.smtp.host", name)
		if own.Host == "" {
			key = fmt.Sprintf("apps.%s.smtp.security", name)
		}
		c.judgePlaintext(key, s.Host, name+"'s mail")
	}
}

func (c *checker) judgePlaintext(key, host, whose string) {
	if !plaintextHostIsPrivate(host, c.cfg.Mesh) {
		if net.ParseIP(host) != nil {
			c.refuse("smtp-plaintext-public-host", key,
				"plaintext smtp to an address that is not private",
				"sends %s to %s with security: none, and %s is not a loopback, RFC 1918, RFC 6598 or IPv6 unique local address, nor inside mesh.subnet. Plaintext mail and its SMTP credentials would cross a network nobody here controls. Use starttls or tls, or a relay on a private network.",
				whose, host, host)
			return
		}
		c.refuse("smtp-plaintext-public-host", key,
			"plaintext smtp to a name that could resolve anywhere",
			"sends %s to %q with security: none. A name with a dot could resolve to any address, and validate does not resolve names, so plaintext is refused for it even when it would resolve privately. Give the relay's mesh address, an IP inside mesh.subnet, or a compose service name (no dot, starting with a letter), or use starttls or tls.",
			whose, host)
		return
	}
	c.warn("smtp-plaintext", key,
		"smtp is plaintext",
		"sends %s to %s with security: none, so the mail and the SMTP credentials cross that network unencrypted, readable by anything on it. It is accepted because %s is on a private network; use starttls or tls wherever the relay offers it.",
		whose, host, host)
}

// plaintextAuth is the citation for each blueprint that refuses to
// authenticate over plaintext: Pocket ID's mailer uses net/smtp's PlainAuth,
// or a LOGIN auth with the same check (emailer/smtp/smtp.go in
// italypaleale/go-kit v1.2.4, at Pocket ID v2.18.0), and the account service
// uses go-mail's PLAIN auth (smtp/auth_plain.go at go-mail v0.8.1, at the
// service's v0.2.0). Both refuse any host but localhost, which inside a
// container is the container itself.
var plaintextAuth = map[config.Blueprint]string{
	config.BlueprintPocketID: "Pocket ID",
	config.BlueprintAccount:  "The account service",
}

// smtpPlaintextWithUsername refuses security: none with a username for an
// app that will not send credentials without TLS, since every mail it sent
// would fail at AUTH.
func (c *checker) smtpPlaintextWithUsername() {
	for _, name := range c.cfg.AppNames() {
		who, ok := plaintextAuth[c.cfg.Apps[name].Blueprint]
		if !ok {
			continue
		}
		s := c.cfg.SMTPFor(name)
		if s.Security != config.SMTPNone || s.Host == "" || s.Username == "" {
			continue
		}
		c.refuse("smtp-plaintext-with-username", fmt.Sprintf("apps.%s.smtp", name),
			"plaintext smtp with a username this app will not send",
			"resolves security: none with username %q. %s refuses to authenticate over an unencrypted connection to any host but localhost, so every mail it sent would fail. Set apps.%s.smtp.security to starttls or tls, or send through a private relay that needs no username and leave username out of the block it inherits.",
			s.Username, who, name)
	}
}

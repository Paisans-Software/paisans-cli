package webserver

import (
	"fmt"
	"io"
	"net"
	"strings"
)

// certificateMarker is the line in each snippet where the operator's own
// certificate goes. The toolkit obtains no certificate with managed_by:
// external: it cannot know how an unfamiliar web server gets one, and it must
// not edit a configuration it does not own.
const certificateMarker = "YOUR CERTIFICATE"

// Show prints the hand-off sheet for a target: what the web server in front
// of the app must do, and the same filled in for the web server ws found, or
// for Caddy, nginx and Apache when none was. With managed_by: paisans the
// toolkit's own Caddy is in front, and the sheet says there is nothing to
// hand off.
func Show(w io.Writer, t Target, ws WebServer) {
	if !t.External() {
		fmt.Fprintf(w, "%s is served by the toolkit's own Caddy on %s (managed_by: paisans): nothing to hand off.\n", t.App, t.Site)
		fmt.Fprintf(w, "Its certificate for %s is obtained over DNS-01 through acme.provider, and %s points at %s.\n", t.Hostname, t.Hostname, addresses(t))
		fmt.Fprintf(w, "`paisans web-server check --app %s` checks the name and the certificate from this machine.\n", t.App)
		return
	}

	fmt.Fprintf(w, "Web server for %s on %s (managed_by: external)\n\n", t.App, t.Site)
	fmt.Fprintf(w, "  hostname   %s\n", t.Hostname)
	fmt.Fprintf(w, "  DNS        %s must point at %s\n", t.Hostname, addresses(t))
	fmt.Fprintf(w, "  upstream   %s\n", t.Upstream())
	fmt.Fprintf(w, "  health     https://%s%s answers %s\n\n", t.Hostname, t.HealthPath, t.HealthExpect)

	fmt.Fprintf(w, "Your web server must:\n")
	fmt.Fprintf(w, "  * terminate TLS for %s, with a certificate you obtain and renew;\n", t.Hostname)
	fmt.Fprintf(w, "    the monitor checks https://%s%s and warns 14 days before it expires\n", t.Hostname, t.HealthPath)
	fmt.Fprintf(w, "  * proxy every path to %s, but for the four refused below\n", t.Upstream())
	fmt.Fprintf(w, "  * pass the Host header through unchanged\n")
	fmt.Fprintf(w, "  * set X-Forwarded-For to the client's address and X-Forwarded-Proto to https;\n")
	fmt.Fprintf(w, "    the monitor's sign in rate limit is keyed on the client's address\n")
	fmt.Fprintf(w, "  * refuse /status*, /badge/*, /metrics and /api/v1/*: the status page lists\n")
	fmt.Fprintf(w, "    every monitor, mesh addresses included, and /metrics is public whenever\n")
	fmt.Fprintf(w, "    no API token exists, which under this toolkit is always\n\n")

	fmt.Fprintf(w, "%s\n\n", ws.Summary())
	if ws.Certbot != "" {
		fmt.Fprintf(w, "Obtain its certificate as the others were obtained:\n\n    %s\n\n", ws.Certbot)
	}
	caddy := func() {
		fmt.Fprintf(w, "Caddy (it sets X-Forwarded-For and X-Forwarded-Proto and passes Host itself),\nat the top level of the Caddyfile, after the global options block if it has one:\n\n")
		fmt.Fprint(w, indent(caddySnippet(t, ws)))
	}
	nginx := func() {
		fmt.Fprintf(w, "nginx:\n\n")
		fmt.Fprint(w, indent(nginxSnippet(t, ws)))
	}
	apache := func() {
		fmt.Fprintf(w, "Apache (mod_ssl, mod_proxy, mod_proxy_http and mod_headers; mod_proxy sets\nX-Forwarded-For itself):\n\n")
		fmt.Fprint(w, indent(apacheSnippet(t, ws)))
	}
	switch ws.Server {
	case ServerCaddy:
		caddy()
	case ServerNginx:
		nginx()
	case ServerApache:
		apache()
	default:
		caddy()
		fmt.Fprintln(w)
		nginx()
		fmt.Fprintln(w)
		apache()
	}
	fmt.Fprintf(w, "\nThen run `paisans web-server check --app %s` from this machine.\n", t.App)
}

// ListenWarning is the warning for a listen address that is not loopback. It
// is returned rather than written into the sheet, which is the command's
// stdout and holds only what is copied into a web server.
func ListenWarning(t Target) (hint, detail string, ok bool) {
	ip := net.ParseIP(t.ListenHost)
	if ip == nil || ip.IsLoopback() {
		return "", "", false
	}
	return fmt.Sprintf("listen is %s, not loopback: ufw does not protect it", t.Listen),
		fmt.Sprintf("Docker publishes the port with its own iptables rules, in front of ufw, so ufw does not protect it: anything that can reach %s can reach the app around your web server. Restrict it upstream of the host, or in Docker's DOCKER-USER chain.", t.ListenHost), true
}

// ACMEDNSWarning is the warning for a Caddy whose global acme_dns reaches
// the toolkit's site: its issuer of its own does not keep the DNS challenge
// out, so the site's certificate depends on the global DNS provider's
// credentials covering its hostname. Like ListenWarning, it is returned
// rather than written into the sheet.
func ACMEDNSWarning(t Target, ws WebServer) (hint, detail string, ok bool) {
	if ws.Server != ServerCaddy || ws.Certs != CertsAutomatic || ws.DNS != DNSACME || len(ws.ACMEDNS) == 0 {
		return "", "", false
	}
	conf := ws.Config
	if conf == "" {
		conf = ws.Main
	}
	found := directive(ws.ACMEDNS, 1)
	inner := append([]string{"dns" + strings.TrimPrefix(ws.ACMEDNS[0], "acme_dns")}, ws.ACMEDNS[1:]...)
	rewrite := "    cert_issuer acme {\n" + directive(inner, 2) + "    }\n"
	masked := ""
	if strings.Contains(found, maskedValue) {
		masked = fmt.Sprintf(" Each %s stands for a value as it is in %s, not shown here.", maskedValue, conf)
	}
	return "caddy's global acme_dns applies to " + t.Hostname + " too",
		fmt.Sprintf("%s sets, in its global options:\n\n%s\nCaddy gives a global acme_dns to every site's ACME issuer that names no DNS provider of its own, the paisans site included, and then uses the DNS challenge alone. So the certificate for %s is obtained over DNS-01 with that provider's credentials, which must cover %s's zone. To have this site use HTTP-01, replace it with:\n\n%s\nwhich gives the same DNS challenge to every site without an issuer of its own, while the paisans site keeps its own. It drops Caddy's default ZeroSSL fallback issuer for those sites.%s", conf, found, t.Hostname, t.Hostname, rewrite, masked), true
}

// maskedValue stands in for a value from the operator's Caddyfile that is
// not a placeholder, which may be a credential.
const maskedValue = "<value>"

// directive is a Caddyfile directive's lines, as Detect read them, indented
// by level and by its own braces, with every value that is not a
// placeholder masked: what follows the first token of a line, and on its
// first line the provider's name too.
func directive(lines []string, level int) string {
	var b strings.Builder
	depth := level
	for i, line := range lines {
		toks := caddyTokens(line)
		if len(toks) > 0 && toks[0] == "}" {
			depth--
		}
		keep := 1
		if i == 0 {
			keep = 2
		}
		for j := keep; j < len(toks); j++ {
			if tok := toks[j]; tok != "{" && tok != "}" && !(len(tok) > 2 && strings.HasPrefix(tok, "{") && strings.HasSuffix(tok, "}")) {
				toks[j] = maskedValue
			}
		}
		b.WriteString(strings.Repeat("    ", depth) + strings.Join(toks, " ") + "\n")
		// A closing brace that opens the line was counted above.
		for j, tok := range toks {
			switch {
			case tok == "{":
				depth++
			case tok == "}" && j > 0:
				depth--
			}
		}
	}
	return b.String()
}

// The three snippets differ in what they spell out because the servers
// differ in what they do unasked. Caddy's reverse_proxy passes incoming
// headers, Host included, through unchanged and sets X-Forwarded-For and
// X-Forwarded-Proto itself (caddyserver.com/docs/caddyfile/directives/
// reverse_proxy, "Defaults", read 2026-10-08). Apache's mod_proxy_http adds
// X-Forwarded-For in reverse proxy mode but not X-Forwarded-Proto, and
// replaces Host unless ProxyPreserveHost is on (httpd 2.4 mod_proxy,
// "Reverse Proxy Request Headers", read 2026-10-08). nginx sets none of them
// unasked.
//
// Each also refuses what the toolkit's own snippet for the uptime kind
// refuses (templates/uptime/caddy.snippet.tmpl), because behind the
// operator's web server that snippet is not in front of the app. Apache's
// refusal is a 403 from Require rather than a 404.
//
// The app is Express with its default routing, which ignores case and a
// trailing slash, so /STATUS and /metrics/ reach the same handlers as
// /status and /metrics. Caddy's path matcher already ignores case; nginx's
// ~* and the (?i) in Apache's pattern make theirs do the same, and every
// pattern accepts /metrics/.
func caddySnippet(t Target, ws WebServer) string {
	var head, tls string
	if ws.Server == ServerCaddy && ws.Listen != "" && len(ws.Private) > 0 {
		head = fmt.Sprintf("# A site of its own, with no bind: it belongs on %s, not on %s.\n", ws.Listen, strings.Join(ws.Private, " or "))
	}
	generic := fmt.Sprintf("\t# tls /etc/ssl/%s/fullchain.pem /etc/ssl/%s/privkey.pem\n", t.Hostname, t.Hostname)
	switch {
	case ws.Server != ServerCaddy:
		tls = fmt.Sprintf("\t# %s: if this Caddy obtains its certificates itself, leave this\n\t# out; otherwise uncomment it and name your own files.\n", certificateMarker) + generic
	case ws.Certs == CertsAutomatic:
		// An issuer of the site's own replaces a global cert_issuer, and
		// with no DNS provider certmagic uses HTTP-01 and TLS-ALPN-01
		// (certmagic v0.25.6, acmeclient.go, where a DNS solver is
		// exclusive of the others). A global acme_dns is the exception:
		// fillInGlobalACMEDefaults (caddy v2.11.7, caddyconfig/
		// httpcaddyfile/tlsapp.go) gives it to every ACME issuer without a
		// DNS provider, this one included, and ACMEDNSWarning says so. A
		// global email still fills in the issuer's.
		tls = "\ttls {\n\t\tissuer acme\n\t}\n"
	case ws.Certs == CertsFolder:
		tls = fmt.Sprintf("\t# This Caddy loads every certificate in %s: put the one for\n\t# %s there.\n", ws.Folder, t.Hostname)
	case ws.CertFile != "":
		tls = fmt.Sprintf("\ttls %s %s\n", ws.CertFile, ws.KeyFile)
	case ws.Certs == CertsFiles:
		tls = fmt.Sprintf("\t# %s: this Caddy loads its certificates from files, such as\n\t# %s: uncomment this and name the files for %s.\n", certificateMarker, ws.Example, t.Hostname) + generic
	default:
		tls = fmt.Sprintf("\t# %s: if this Caddy obtains its certificates itself, leave this\n\t# out; otherwise uncomment it and name your own files.\n", certificateMarker) + generic
	}
	return head + fmt.Sprintf(`%s {
%s	@refused path /status* /badge/* /metrics /metrics/ /api/v1/*
	respond @refused 404
	reverse_proxy %s
}
`, t.Hostname, tls, t.Listen)
}

// certLines is the certificate's lines in an nginx or Apache snippet, each
// directive ended by end and indented as the snippet's body: the files
// Detect found for the target, or the operator's own to name.
func certLines(t Target, ws WebServer, server Server, cert, key, end string) string {
	if ws.Server == server && ws.CertFile != "" {
		return cert + ws.CertFile + end + "\n    " + key + ws.KeyFile + end
	}
	return fmt.Sprintf("# %s: replace both paths with your own.\n    %s/etc/ssl/%s/fullchain.pem%s\n    %s/etc/ssl/%s/privkey.pem%s", certificateMarker, cert, t.Hostname, end, key, t.Hostname, end)
}

func nginxSnippet(t Target, ws WebServer) string {
	certs := certLines(t, ws, ServerNginx, "ssl_certificate     ", "ssl_certificate_key ", ";")
	return fmt.Sprintf(`server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name %s;

    %s

    location ~* ^/(status|badge/|metrics/?$|api/v1/) {
        return 404;
    }

    location / {
        proxy_pass %s;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
`, t.Hostname, certs, t.Upstream())
}

func apacheSnippet(t Target, ws WebServer) string {
	certs := certLines(t, ws, ServerApache, "SSLCertificateFile    ", "SSLCertificateKeyFile ", "")
	return fmt.Sprintf(`<VirtualHost *:443>
    ServerName %s

    SSLEngine on
    %s

    <LocationMatch "(?i)^/(status|badge/|metrics/?$|api/v1/)">
        Require all denied
    </LocationMatch>

    ProxyPreserveHost On
    RequestHeader set X-Forwarded-Proto "https"
    ProxyPass        / %s/
    ProxyPassReverse / %s/
</VirtualHost>
`, t.Hostname, certs, t.Upstream(), t.Upstream())
}

// addresses is where the hostname must resolve, as a phrase.
func addresses(t Target) string {
	if t.PublicAddress6 == "" {
		return t.PublicAddress
	}
	return t.PublicAddress + " and " + t.PublicAddress6
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "    " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

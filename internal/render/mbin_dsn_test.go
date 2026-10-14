package render

import (
	"net/url"
	"testing"
)

// MAILER_DSN for each security mode, with and without credentials, at the
// declared port or the mode's default (PortOrDefault, which smtpFor applies).
func TestMailerDSN(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   smtpValues
		want string
	}{
		{"no host is the null transport", smtpValues{}, "null://null"},
		{"starttls requires tls", smtpValues{Host: "smtp.example.org", Port: 587, Username: "u", Password: "p"},
			"smtp://u:p@smtp.example.org:587?require_tls=true"},
		{"tls is smtps", smtpValues{Host: "smtp.example.org", Port: 465, Secure: true, Username: "u", Password: "p"},
			"smtps://u:p@smtp.example.org:465"},
		{"none turns off STARTTLS", smtpValues{Host: "relay", Port: 25, Security: "none", Username: "u", Password: "p"},
			"smtp://u:p@relay:25?auto_tls=false"},
		{"no username has no userinfo", smtpValues{Host: "smtp.example.org", Port: 25, Password: "unused"},
			"smtp://smtp.example.org:25?require_tls=true"},
		{"a username without a password", smtpValues{Host: "smtp.example.org", Port: 587, Username: "relay@example.org"},
			"smtp://relay%40example.org@smtp.example.org:587?require_tls=true"},
		{"an IPv6 host is bracketed", smtpValues{Host: "2001:db8::25", Port: 587},
			"smtp://[2001:db8::25]:587?require_tls=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailerDSN(tc.in); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A password holding every character that could move the split between
// userinfo and host, end the authority, or be decoded twice comes back byte
// for byte through a URL parser that rawurldecodes, as Symfony's
// Dsn::fromString does after parse_url.
func TestMailerDSNEncodesAHostilePassword(t *testing.T) {
	password := "a@b:c/d?e#f%g'h\"i$j k+l&m=n\\o"
	dsn := mailerDSN(smtpValues{Host: "smtp.example.org", Port: 587, Username: "me@example.org", Password: password})
	want := "smtp://me%40example.org:a%40b%3Ac%2Fd%3Fe%23f%25g%27h%22i%24j%20k%2Bl%26m%3Dn%5Co@smtp.example.org:587?require_tls=true"
	if dsn != want {
		t.Fatalf("got  %s\nwant %s", dsn, want)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := u.User.Password()
	if got != password || u.User.Username() != "me@example.org" || u.Hostname() != "smtp.example.org" || u.Port() != "587" {
		t.Errorf("parsed back as user %q password %q host %q port %q", u.User.Username(), got, u.Hostname(), u.Port())
	}
	// Nothing compose reads specially is left in the value, so envquote
	// writes it bare and it reaches Symfony as rendered.
	if q, err := envquote(dsn); err != nil || q != dsn {
		t.Errorf("envquote changed the DSN: %q, %v", q, err)
	}
}

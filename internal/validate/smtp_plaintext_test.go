package validate_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// withPlaintextSMTP is valid.yaml with the deployment's smtp block replaced,
// and the health app's own smtp block set to own.
func withPlaintextSMTP(t *testing.T, smtp config.SMTP, own *config.SMTP) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	cfg.SMTP = smtp
	app := cfg.Apps["health"]
	app.SMTP = own
	cfg.Apps["health"] = app
	return cfg
}

// security: none is accepted for a host on a private network, with a
// warning, and refused for anything else, without resolving a name.
func TestPlaintextSMTPHost(t *testing.T) {
	for _, tc := range []struct {
		host   string
		mesh   string
		accept bool
	}{
		{"127.0.0.1", "", true},
		{"::1", "", true},
		{"10.1.2.3", "", true},
		{"172.16.0.25", "", true},
		{"192.168.1.25", "", true},
		{"100.64.0.25", "", true},
		{"100.127.255.254", "", true},
		{"fd00::25", "", true},
		{"198.18.0.25", "198.18.0.0/24", true},
		{"mailrelay", "", true},
		{"postfix-relay", "", true},
		{"localhost", "", true},

		{"203.0.113.25", "", false},
		{"8.8.8.8", "", false},
		{"100.128.0.1", "", false},
		{"198.18.1.25", "198.18.0.0/24", false},
		{"2001:db8::25", "", false},
		{"fe80::1", "", false},
		{"0.0.0.0", "", false},
		{"smtp.example.org", "", false},
		{"relay.internal", "", false},
		{"relay.", "", false},
		{"010.0.0.1", "", false},
		{"0x08080808", "", false},
		{"134744072", "", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			cfg := withPlaintextSMTP(t, config.SMTP{Host: tc.host, Security: config.SMTPNone, FromAddress: "health@example.org"}, nil)
			if tc.mesh != "" {
				cfg.Mesh.Subnet = tc.mesh
			}
			r := validate.Check(cfg)
			refused, warned := findings(r, "smtp-plaintext-public-host"), findings(r, "smtp-plaintext")
			if tc.accept {
				if len(refused) != 0 || !slices.Equal(warned, []string{"WARN smtp.host"}) {
					t.Errorf("refused %v, warned %v; want one warning at smtp.host", refused, warned)
				}
				return
			}
			if !slices.Equal(refused, []string{"REFUSE smtp.host"}) || len(warned) != 0 {
				t.Errorf("refused %v, warned %v; want one refusal at smtp.host", refused, warned)
			}
		})
	}
}

// A dotted name is refused even when it would resolve privately, and the
// refusal says to use the mesh address.
func TestPlaintextSMTPDottedHostNamesTheMesh(t *testing.T) {
	cfg := withPlaintextSMTP(t, config.SMTP{Host: "relay.example.org", Security: config.SMTPNone, FromAddress: "health@example.org"}, nil)
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "smtp-plaintext-public-host" {
			if !strings.Contains(f.Message, "mesh") {
				t.Errorf("the refusal does not point at the mesh: %s", f.Message)
			}
			return
		}
	}
	t.Fatal("a dotted name was not refused")
}

// The warning says what crosses the network unencrypted.
func TestPlaintextSMTPWarningNamesWhatIsExposed(t *testing.T) {
	for _, f := range validate.Check(load(t, "smtp-plaintext")).Findings {
		if f.Rule == "smtp-plaintext" {
			if !strings.Contains(f.Message, "credentials") || !strings.Contains(f.Message, "unencrypted") {
				t.Errorf("the warning does not say what is exposed: %s", f.Message)
			}
			return
		}
	}
	t.Fatal("smtp-plaintext did not fire")
}

// An app's own smtp block is judged by its effective settings: its own
// security over the deployment's host, or its own host under the
// deployment's security, each at the key the app declared.
func TestPlaintextSMTPAppOverride(t *testing.T) {
	public := config.SMTP{Host: "smtp.example.org", Security: config.SMTPStartTLS, Username: "health@example.org", FromAddress: "health@example.org"}
	private := config.SMTP{Host: "10.44.0.25", Security: config.SMTPNone, FromAddress: "health@example.org"}
	for _, tc := range []struct {
		name          string
		smtp          config.SMTP
		own           *config.SMTP
		refused, warn []string
	}{
		{"own none over a public host", public, &config.SMTP{Security: config.SMTPNone},
			[]string{"REFUSE apps.health.smtp.security"}, nil},
		{"own none with its own mesh host", public, &config.SMTP{Host: "10.44.0.9", Security: config.SMTPNone},
			nil, []string{"WARN apps.health.smtp.host"}},
		{"own none with a dotless host", public, &config.SMTP{Host: "relay", Security: config.SMTPNone},
			nil, []string{"WARN apps.health.smtp.host"}},
		{"own public host under the deployment's none", private, &config.SMTP{Host: "smtp.example.org"},
			[]string{"REFUSE apps.health.smtp.host"}, []string{"WARN smtp.host"}},
		{"own starttls under the deployment's none", private, &config.SMTP{Host: "smtp.example.org", Security: config.SMTPStartTLS},
			nil, []string{"WARN smtp.host"}},
		{"own sender only under the deployment's none", private, &config.SMTP{FromAddress: "monitor@example.org"},
			nil, []string{"WARN smtp.host"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validate.Check(withPlaintextSMTP(t, tc.smtp, tc.own))
			if got := findings(r, "smtp-plaintext-public-host"); !slices.Equal(got, tc.refused) {
				t.Errorf("refused %v, want %v", got, tc.refused)
			}
			if got := findings(r, "smtp-plaintext"); !slices.Equal(got, tc.warn) {
				t.Errorf("warned %v, want %v", got, tc.warn)
			}
		})
	}
}

// Pocket ID and the account service will not send a username and password
// over an unencrypted connection to anything but localhost, so a username
// with security: none is refused for them alone. The monitor and Mbin send
// it.
func TestPlaintextSMTPWithAUsername(t *testing.T) {
	r := validate.Check(load(t, "smtp-plaintext-with-username"))
	got := findings(r, "smtp-plaintext-with-username")
	want := []string{"REFUSE apps.account.smtp", "REFUSE apps.auth.smtp"}
	if !slices.Equal(got, want) {
		t.Errorf("refused %v, want %v", got, want)
	}

	// The deployment authenticates over TLS and the monitor alone sends in
	// plaintext, with the inherited username: nothing is refused.
	cfg := withPlaintextSMTP(t,
		config.SMTP{Host: "smtp.example.org", Security: config.SMTPStartTLS, Username: "health@example.org", FromAddress: "health@example.org"},
		&config.SMTP{Host: "10.44.0.9", Security: config.SMTPNone})
	if got := findings(validate.Check(cfg), "smtp-plaintext-with-username"); len(got) != 0 {
		t.Errorf("the monitor's plaintext with a username was refused: %v", got)
	}
}

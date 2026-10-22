package render_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
)

const gatewayCaddyEnv = "vm/srv/paisans/f2a9/infra/caddy/caddy.env"

// A pasted token renders as one line holding the same value: a real token's
// characters are written bare, and one compose would read specially is
// quoted so that compose delivers it byte for byte.
func TestThePastedDNSTokenRendersAsOneLine(t *testing.T) {
	for token, want := range map[string]string{
		"Ab0_-Cd1Ef2Gh3Ij4Kl5Mn6Op7Qr8St9Uv0Wx1Yz2": "Ab0_-Cd1Ef2Gh3Ij4Kl5Mn6Op7Qr8St9Uv0Wx1Yz2",
		"cfat_Ab0-Cd1.Ef2_Gh3":                      "cfat_Ab0-Cd1.Ef2_Gh3",
		"a$b #c":                                    `"a\$b #c"`,
	} {
		cfg, secrets := fixture(t), fixtureSecrets(t)
		secrets.External["acme_dns_token"] = token
		env := buildFiles(t, cfg, secrets)[gatewayCaddyEnv]
		if !strings.Contains(env, "\nACME_DNS_TOKEN="+want+"\n") {
			t.Errorf("%q: want the line ACME_DNS_TOKEN=%s in:\n%s", token, want, env)
		}
	}
}

// toolkitEnvFiles is each env file a template in this repository writes a
// pasted or captured secret into: Caddy's, and the reserved blueprints'.
// The core catalog's templates are its own.
var toolkitEnvFiles = []string{"/infra/caddy/caddy.env", "/auth/.env", "/account/.env", "/gate/.env", "/health/.env"}

// A line break that reached render past the secrets file's own check still
// cannot add a line to an env file the toolkit's own templates write: each
// pasted value is either quoted onto its own line or refused.
func TestALineBreakInAPastedSecretAddsNoLine(t *testing.T) {
	for name, c := range map[string]string{"a newline": "\n", "a carriage return": "\r", "NUL": "\x00"} {
		t.Run(name, func(t *testing.T) {
			cfg, secrets := withDNS(t, false)
			bad := "tok" + c + "INJECTED=x"
			secrets.External["acme_dns_token"] = bad
			secrets.External["dns_members_token"] = bad
			for name, client := range secrets.OIDCClients {
				client.ClientID, client.ClientSecret = bad, bad
				secrets.OIDCClients[name] = client
			}
			plan, err := render.Build(cfg, secrets)
			if err != nil {
				return
			}
			for path, content := range planFiles(plan) {
				if !slices.ContainsFunc(toolkitEnvFiles, func(suffix string) bool { return strings.HasSuffix(path, suffix) }) {
					continue
				}
				for _, line := range strings.FieldsFunc(content, func(r rune) bool { return r == '\n' || r == '\r' }) {
					if strings.HasPrefix(line, "INJECTED=") {
						t.Errorf("%s gained a line: %q", path, line)
					}
				}
			}
		})
	}
}

package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// lineBreakers is each character that could end an env file's line early, or
// that a reader of one could take as a line break, as YAML double quoted
// escapes and as Go strings.
var lineBreakers = []struct{ name, yaml, raw string }{
	{"a newline", `\n`, "\n"},
	{"a carriage return", `\r`, "\r"},
	{"NUL", `\0`, "\x00"},
	{"a line separator", `\u2028`, "\u2028"},
	{"a paragraph separator", `\u2029`, "\u2029"},
}

// writeSecrets writes a plaintext secrets file and returns its path.
func writeSecrets(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A value an operator pasted is refused when the file is read, by key and
// with the reason, and never with the value: a line break in it would add a
// line to the env file it is rendered into.
func TestPastedSecretWithAControlCharacterIsRefusedOnLoad(t *testing.T) {
	for _, c := range lineBreakers {
		t.Run(c.name, func(t *testing.T) {
			path := writeSecrets(t, "version: 2\nexternal:\n  acme_dns_token: \"tok"+c.yaml+"STATIC_API_KEY=x\"\n")
			_, err := config.LoadSecrets(path)
			var p *ui.Problem
			if !errors.As(err, &p) {
				t.Fatalf("got %v, want a refusal", err)
			}
			text := p.Hint + " " + p.Explain + " " + err.Error()
			if !strings.Contains(p.Hint, "external.acme_dns_token") || !strings.Contains(p.Explain, "env file") {
				t.Errorf("the refusal does not say which key and why: %#v", p)
			}
			if strings.Contains(text, "STATIC_API_KEY") {
				t.Errorf("the refusal shows the value: %q", text)
			}
		})
	}
}

// Every section an operator can paste into is held to it, and so is a
// rotation's next value for one of them.
func TestEveryPastedSectionIsChecked(t *testing.T) {
	for name, body := range map[string]string{
		"an app's SMTP password":  "apps:\n  talk:\n    smtp_password: \"a\\nb\"\n",
		"a backup destination":    "backup:\n  destinations:\n    offsite:\n      secret_access_key: \"a\\nb\"\n",
		"an OIDC client":          "oidc_clients:\n  talk:\n    client_secret: \"a\\nb\"\n",
		"a Pocket ID group":       "pocket_id_groups:\n  members: \"a\\nb\"\n",
		"a DNS provider address":  "operators:\n  ana:\n    dns_provider_email: \"a\\nb\"\n",
		"a rotation's next value": "rotations:\n  external.smtp_password: \"a\\nb\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.LoadSecrets(writeSecrets(t, "version: 2\n"+body)); err == nil {
				t.Error("a line break was accepted")
			}
		})
	}
}

// What the toolkit generates is not held to it: a PEM key is several lines,
// and the file carries it as such.
func TestGeneratedMultilineSecretsLoad(t *testing.T) {
	path := writeSecrets(t, "version: 2\napps:\n  chat:\n    mas_signing_key: \"-----BEGIN PRIVATE KEY-----\\nAAAA\\n-----END PRIVATE KEY-----\\n\"\n")
	if _, err := config.LoadSecrets(path); err != nil {
		t.Fatalf("a PEM key was refused: %v", err)
	}
}

// A file whose one bad value is the key being replaced is read, so `secrets
// set` can repair it; any other bad value still refuses it.
func TestLoadingToReplaceAKeyReadsTheFileItRepairs(t *testing.T) {
	path := writeSecrets(t, "version: 2\nexternal:\n  acme_dns_token: \"a\\nb\"\n")
	if _, err := config.LoadDeploymentSecretsReplacing(path, exampleDeploymentID, "external.acme_dns_token"); err != nil {
		t.Errorf("the key being replaced refused the file: %v", err)
	}
	if _, err := config.LoadDeploymentSecretsReplacing(path, exampleDeploymentID, "external.smtp_password"); err == nil {
		t.Error("another key's bad value was accepted")
	}
}

// CheckPastedSecret is what a command that takes a pasted value asks.
func TestCheckPastedSecret(t *testing.T) {
	for _, c := range lineBreakers {
		if err := config.CheckPastedSecret("external.acme_dns_token", "tok"+c.raw+"x"); err == nil || !strings.Contains(err.Error(), "external.acme_dns_token") {
			t.Errorf("%s: got %v, want a refusal naming the key", c.name, err)
		}
	}
	// Real provider tokens: a Cloudflare user token, a Cloudflare account
	// token and a deSEC token.
	for _, ok := range []string{"Ab0_-Cd1Ef2Gh3Ij4Kl5Mn6Op7Qr8St9Uv0Wx1Yz2", "cfat_Ab0-Cd1.Ef2_Gh3", "a1B2c3D4e5F6g7H8i9J0k1L2m3N4"} {
		if err := config.CheckPastedSecret("external.acme_dns_token", ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	if err := config.CheckPastedSecret("apps.chat.mas_signing_key", "a\nb"); err != nil {
		t.Errorf("a generated key was held to the pasted rule: %v", err)
	}
}

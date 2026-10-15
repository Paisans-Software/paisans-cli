package secretsgen_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// declaredOf is Declare's paths of one source.
func declaredOf(cfg *config.Config, s *config.Secrets, source secretsgen.Source) []string {
	var out []string
	for _, d := range secretsgen.Declare(cfg, s) {
		if d.Source == source {
			out = append(out, d.Path)
		}
	}
	return out
}

// Declare names exactly what Fill generates, without generating it, so
// `secrets list` and `init` cannot disagree about which secrets a deployment
// has. Checked on both fixtures, one with backups and a mail login and one
// without.
func TestDeclareNamesWhatFillGenerates(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "render", "testdata", "deployment.yaml"),
		filepath.Join("..", "..", "examples", "paisans-stack.example.yaml"),
	} {
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		empty := &config.Secrets{}
		want := declaredOf(cfg, empty, secretsgen.Generated)
		filled, err := secretsgen.Fill(cfg, &config.Secrets{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(filled.Generated, want) {
			t.Errorf("%s: Fill generated %v, Declare names %v", path, filled.Generated, want)
		}
		// What Fill reports as owed is declared as pasted.
		pasted := declaredOf(cfg, empty, secretsgen.Pasted)
		for _, o := range filled.Owed {
			if !slices.Contains(pasted, o.Name) {
				t.Errorf("%s: %s is owed but not declared as pasted (%v)", path, o.Name, pasted)
			}
		}
		// What apply records is declared as captured, by its leaves.
		captured := declaredOf(cfg, empty, secretsgen.Captured)
		for _, p := range filled.Pending {
			if !slices.Contains(captured, p.Name+".client_secret") {
				t.Errorf("%s: %s is pending but not declared as captured (%v)", path, p.Name, captured)
			}
		}
	}
}

// A pasted secret stays declared once it is set, where owed forgets it. The
// deployment's SMTP password is declared while an app logging in to send
// mail has none of its own, and an app's own once it is set.
func TestDeclareKeepsPastedSecretsOnceSet(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &config.Secrets{External: map[string]string{"acme_dns_token": "t", "smtp_password": "p"}}
	pasted := declaredOf(cfg, s, secretsgen.Pasted)
	for _, want := range []string{"external.acme_dns_token", "external.smtp_password", "backup.destinations.offsite.access_key_id"} {
		if !slices.Contains(pasted, want) {
			t.Errorf("%s not declared once set: %v", want, pasted)
		}
	}
	if slices.Contains(pasted, "apps.health.smtp_password") {
		t.Errorf("apps.health.smtp_password declared while unset: %v", pasted)
	}

	s.Apps = map[string]map[string]any{}
	for _, name := range cfg.AppNames() {
		s.Apps[name] = map[string]any{"smtp_password": "own"}
	}
	pasted = declaredOf(cfg, s, secretsgen.Pasted)
	if !slices.Contains(pasted, "apps.health.smtp_password") || !slices.Contains(pasted, "apps.talk.smtp_password") {
		t.Errorf("an app's own SMTP password is not declared once set: %v", pasted)
	}
	if slices.Contains(pasted, "apps.chat.smtp_password") {
		t.Errorf("apps.chat.smtp_password declared, though chat sends no mail: %v", pasted)
	}
	if slices.Contains(pasted, "external.smtp_password") {
		t.Errorf("external.smtp_password declared while every app that logs in has its own: %v", pasted)
	}
}

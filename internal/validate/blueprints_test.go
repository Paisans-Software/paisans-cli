package validate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// notesBlueprint is a catalog blueprint that leaves out what a careful one
// says: no health route, a MySQL database with no dump, a gate it leaves
// ActivityPub open past, and checks and settings of its own.
const notesBlueprint = `apiVersion: paisans.blueprint/v1
name: notes
services:
  - {name: app, image: "ghcr.io/example/notes:1"}
  - {name: db, image: "docker.io/library/mysql:8.4"}
database: {engine: mysql}
port: 8400
config: {format: env, file: .env}
gate: {gateable: true, leaves_open: [activitypub]}
settings:
  contact: {type: email}
  mode: {type: enum, values: [quiet, loud]}
checks:
  - id: notes-contact-not-an-email
    level: refuse
    key: settings.contact
    rule: "!has(app.settings.contact) || isBareEmail(app.settings.contact)"
    message: "notes mails its contact, which must be an address alone"
  - id: notes-without-smtp
    level: warn
    rule: "has(app.smtp.host)"
    message: "notes has no mail server"
  - id: notes-mode-on-a-small-deployment
    level: warn
    hint: notes runs loud on a small deployment
    key_expr: "'settings.' + 'mode'"
    rule: "!has(app.settings.mode) || app.settings.mode != 'loud' || size(deployment.sites) > 9"
    message_expr: "app.name + ' is loud on ' + app.sites.join(', ') + ', one of ' + string(size(deployment.sites)) + ' sites, keeping files ' + app.storage.mode"
`

// loadWithCatalog writes a catalog holding notesBlueprint, and the render
// fixture with an app using it, and loads that.
func loadWithCatalog(t *testing.T, settings string) *config.Config {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                        "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/notes/blueprint.yaml":     notesBlueprint,
		"blueprints/notes/compose.yaml.tmpl":  "services: {}\n",
		"blueprints/notes/caddy.snippet.tmpl": "\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "catalog")
	base, err := os.ReadFile(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	app := "\napps:\n  notes:\n    blueprint: ex/notes\n    hostname: notes.example.org\n    placement: { pinned: vm }\n    gated: true\n" + settings
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  ex:\n    url: "+repo+"\n    commit: "+git("rev-parse", "HEAD")+"\n"+app, 1)
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// What a catalog blueprint leaves out, and its own checks and settings,
// are found by name.
func TestABlueprintsOwnRulesRun(t *testing.T) {
	cfg := loadWithCatalog(t, "    settings:\n      contact: \"Notes <notes@example.org>\"\n      colour: red\n      mode: shouty\n")
	result := validate.Check(cfg)
	for rule, key := range map[string]string{
		"notes-contact-not-an-email": "apps.notes.settings.contact",
		"blueprint-without-health":   "apps.notes.blueprint",
		"blueprint-without-backup":   "apps.notes.blueprint",
		"gated-leaves-open":          "apps.notes.gated",
		"blueprint-setting-unknown":  "apps.notes.settings.colour",
		"blueprint-setting-invalid":  "apps.notes.settings.mode",
	} {
		found := false
		for _, f := range result.Findings {
			if f.Rule == rule && f.Key == key {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s at %s in %v", rule, key, result.Findings)
		}
	}
	// A check's key and message may be worked out for the app, and the
	// catalog's check says whose it is.
	cfg = loadWithCatalog(t, "    settings:\n      mode: loud\n")
	var loud *validate.Finding
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "notes-mode-on-a-small-deployment" {
			loud = &f
		}
	}
	if loud == nil {
		t.Fatal("the loud check did not fail")
	}
	if loud.Key != "apps.notes.settings.mode" || loud.Hint != "notes runs loud on a small deployment" || !strings.HasPrefix(loud.Message, "notes is loud on vm, one of ") || !strings.HasSuffix(loud.Message, " sites, keeping files local (a check of the ex/notes blueprint).") {
		t.Errorf("finding %+v", *loud)
	}

	// A blueprint's check that holds says nothing.
	cfg = loadWithCatalog(t, "    settings:\n      contact: notes@example.org\n")
	if validate.Check(cfg).Has("notes-contact-not-an-email") {
		t.Error("a bare address failed the blueprint's check")
	}
}

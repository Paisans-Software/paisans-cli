package grant_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/grant"
)

// A foundation image is reviewed where foundation.yaml is, in the core
// catalog's pull requests, so a change to one is shown and not approved again,
// while an image a blueprint names itself still is.
func TestAChangedFoundationImageNeedsNoApproval(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.6.*", Foundation: true},
		{Stack: "infra", Service: "garage", Reference: "dxflrs/garage:v1.*.*", Foundation: true},
		{Stack: "talk", Service: "app", Reference: "ghcr.io/example/mbin:1.15"},
	}}
	rec := grant.Record{Images: &grant.ImagesPart{Entries: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.5.*"},
		{Stack: "talk", Service: "app", Reference: "ghcr.io/example/mbin:1.14"},
	}}}
	r := grant.Compare("home-a", g, rec, grant.ApplyScope())
	if len(r.Changes) != 3 {
		t.Fatalf("changes: %+v", r.Changes)
	}
	for _, c := range r.Changes {
		foundation := strings.HasPrefix(c.Subject, "image infra/")
		if c.Foundation != foundation || c.NeedsApproval() == foundation {
			t.Errorf("%s: foundation %v, needs approval %v", c.Line(), c.Foundation, c.NeedsApproval())
		}
	}
	if r.Pending() != 1 {
		t.Errorf("%d pending, want only the app's image", r.Pending())
	}
	if u := r.Unapproved(grant.Review{}); len(u) != 1 || u[0].Subject != "image talk/app" {
		t.Errorf("unapproved: %+v", u)
	}
}

// A review whose only additions and changes are foundation images asks
// nothing, and approving it records no approval.
func TestOnlyFoundationChangesAreNothingToApprove(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.6.*", Foundation: true}}}
	rec := grant.Record{Images: &grant.ImagesPart{Entries: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.5.*"},
		{Stack: "old", Service: "app", Reference: "ghcr.io/example/old:1"},
	}}}
	r := grant.Compare("home-a", g, rec, grant.ApplyScope())
	if len(r.Changes) != 2 || r.Pending() != 0 || len(r.Unapproved(grant.Review{})) != 0 {
		t.Errorf("pending %d of %+v", r.Pending(), r.Changes)
	}
	if got := rec.Approve(r.Changes, time.Unix(0, 0), "alice"); got.Approved != nil {
		t.Errorf("a foundation image was recorded as approved: %+v", got.Approved)
	}
}

// The record holds foundation images like any other, so it is complete, and
// one written before the mark reads as the same images: a reference is the
// same image whatever the record says of where it came from.
func TestTheRecordKeepsFoundationImages(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.6.*", Foundation: true}}}
	rec, err := grant.Parse(grant.Encode(grant.Update(grant.Record{}, g, grant.ApplyScope(), time.Unix(0, 0), "")))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Images == nil || len(rec.Images.Entries) != 1 || !rec.Images.Entries[0].Foundation {
		t.Errorf("record: %+v", rec.Images)
	}
	older, err := grant.Parse(`{"version": 3, "images": {"applied_at": "x", "entries": [{"stack": "infra", "service": "etcd", "reference": "quay.io/coreos/etcd:v3.6.*"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if r := grant.Compare("home-a", g, older, grant.ApplyScope()); len(r.Changes) != 0 {
		t.Errorf("an unmarked record entry is a change: %+v", r.Changes)
	}
}

// Every image the render takes from foundation.yaml is marked, by where the
// render took it: the infrastructure stack's, and those of the blueprints
// built into the toolkit. An image a catalog blueprint names is not, and
// neither is one the configuration declares.
func TestARenderedGrantMarksTheFoundationsImages(t *testing.T) {
	cfg := fixture(t)
	plan := rendered(t, cfg)
	marked := map[string]bool{}
	for _, site := range cfg.SiteNames() {
		g, err := grant.For(cfg, plan, site)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range g.Images {
			name := i.Stack + "/" + i.Service
			if prior, seen := marked[name]; seen && prior != i.Foundation {
				t.Errorf("%s is marked differently on different sites", name)
			}
			marked[name] = i.Foundation
		}
	}
	for _, name := range []string{"infra/etcd", "infra/patroni", "infra/haproxy", "infra/garage", "infra/caddy", "infra/heartbeat", "auth/app", "auth/reconciler"} {
		if foundation, ok := marked[name]; !ok || !foundation {
			t.Errorf("%s: rendered %v, marked %v", name, ok, foundation)
		}
	}
	for _, name := range []string{"talk/app", "wiki/app", "wiki/bootstrap"} {
		if foundation, ok := marked[name]; !ok || foundation {
			t.Errorf("%s: rendered %v, marked foundation %v", name, ok, foundation)
		}
	}
	// A pinned app's own database runs the foundation's image for the
	// deployment's Postgres major unless the configuration declares one, as
	// the fixture's chat does.
	if foundation, ok := marked["blog/postgres"]; !ok || !foundation {
		t.Errorf("blog/postgres: rendered %v, marked %v", ok, foundation)
	}
	if foundation, ok := marked["chat/postgres"]; !ok || foundation {
		t.Errorf("chat/postgres, declared: rendered %v, marked %v", ok, foundation)
	}
	var heartbeat bool
	for name, foundation := range marked {
		if strings.HasPrefix(name, "auth/") && strings.Contains(name, "heartbeat") {
			heartbeat = true
			if !foundation {
				t.Errorf("%s, the admins heartbeat, is not marked", name)
			}
		}
	}
	if !heartbeat {
		t.Errorf("no admins heartbeat in the auth stack: %v", marked)
	}

	// A Caddy image acme.image declares is the operator's choice.
	cfg.ACME.Image = "ghcr.io/example-org/caddy-desec:2.10.0"
	g, err := grant.For(cfg, rendered(t, cfg), "vm")
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range g.Images {
		if i.Stack == "infra" && i.Service == "caddy" && i.Foundation {
			t.Errorf("a declared acme.image is marked foundation: %+v", i)
		}
	}
}

// Whether an image is the foundation's is decided by where the render took
// it, not by its name: a catalog blueprint that writes the foundation's own
// reference under image: still has it approved.
func TestACatalogImageNamingAFoundationImageNeedsApproval(t *testing.T) {
	pocketID, err := blueprints.FoundationImage(blueprints.FoundationPocketID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := withCatalogApp(t, `apiVersion: paisans.blueprint/v1
name: journal
services:
  - {name: app, image: "`+pocketID+`", purpose: the application}
database: {engine: none}
port: 8300
config: {format: env, file: .env}
health: {path: /status, status: [200]}
`)
	g, err := grant.For(cfg, rendered(t, cfg), "vm")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, i := range g.Images {
		if i.Stack == "notes" && i.Service == "app" {
			found = true
			if i.Foundation || i.Reference != pocketID {
				t.Errorf("notes/app: %+v", i)
			}
		}
	}
	if !found {
		t.Fatalf("no notes/app: %+v", g.Images)
	}
	r := grant.Compare("vm", g, grant.Record{}, grant.ApplyScope("notes"))
	for _, c := range r.Changes {
		if c.Subject == "image notes/app" && (!c.NeedsApproval() || c.Foundation) {
			t.Errorf("the catalog's image needs no approval: %+v", c)
		}
	}
}

// withCatalogApp is the fixture with an app notes, pinned to vm, whose
// blueprint is example/journal from a catalog made here.
func withCatalogApp(t *testing.T, blueprint string) *config.Config {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "paisans-example")
	files := map[string]string{
		"catalog.yaml":                      "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/journal/blueprint.yaml": blueprint,
		"blueprints/journal/compose.yaml.tmpl": `name: {{ .Project }}
services:
  app:
    image: {{ .Image "app" }}
`,
		"blueprints/journal/.env.secret.tmpl":   "JOURNAL_URL=https://{{ .Hostname }}\n",
		"blueprints/journal/caddy.snippet.tmpl": "reverse_proxy {{ range .Upstreams }}{{ . }} {{ end }}\n",
	}
	for name, content := range files {
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
	commit := git("rev-parse", "HEAD")
	base, err := os.ReadFile(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(base), "\napps:\n", "\ncatalogs:\n  example:\n    url: "+repo+"\n    commit: "+commit+"\n\napps:\n  notes:\n    blueprint: example/journal\n    hostname: notes.example.org\n    placement: { pinned: vm }\n", 1)
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

// A part where only foundation images were added or changed keeps who
// approved the rest of it, since nobody approved the foundation's.
func TestAFoundationOnlyChangeKeepsWhoApproved(t *testing.T) {
	was := grant.Grant{Images: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.5.*", Foundation: true},
		{Stack: "talk", Service: "app", Reference: "ghcr.io/example/mbin:1.15"},
	}}
	rec := grant.Update(grant.Record{}, was, grant.ApplyScope(), time.Unix(0, 0), "alice")
	now := grant.Grant{Images: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.6.*", Foundation: true},
		{Stack: "talk", Service: "app", Reference: "ghcr.io/example/mbin:1.15"},
	}}
	next := grant.Update(rec, now, grant.ApplyScope(), time.Unix(100, 0), "")
	if next.Images.ApprovedBy != "alice" || next.Images.Entries[0].Reference != "quay.io/coreos/etcd:v3.6.*" {
		t.Errorf("images part: %+v", next.Images)
	}
	// A change outside the foundation names who approved it.
	now.Images[1].Reference = "ghcr.io/example/mbin:1.16"
	if next := grant.Update(rec, now, grant.ApplyScope(), time.Unix(100, 0), "bob"); next.Images.ApprovedBy != "bob" {
		t.Errorf("approved by %q, want bob", next.Images.ApprovedBy)
	}
}

// A record written before images were marked differs from the grant only in
// the mark, which is no change: the part is left as it is, applied_at and
// approved_by included.
func TestOnlyTheMarkIsNoChange(t *testing.T) {
	older := grant.Record{Version: grant.Version, Images: &grant.ImagesPart{AppliedAt: "2026-10-01T00:00:00Z", ApprovedBy: "alice", Entries: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.6.*"},
	}}}
	g := grant.Grant{Images: []grant.Image{{Stack: "infra", Service: "etcd", Reference: "quay.io/coreos/etcd:v3.6.*", Foundation: true}}}
	next := grant.Update(older, g, grant.ApplyScope(), time.Unix(100, 0), "")
	if grant.Encode(next) != grant.Encode(older) {
		t.Errorf("rewrote an unchanged part:\n%s", grant.Encode(next))
	}
}

package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Two files the render writes at one path would leave one of them unwritten
// and the other reviewed in its place, so the render refuses rather than
// choose. An app named for the infrastructure stack is the way to get there
// past a configuration that was never loaded.
func TestTwoFilesAtOnePathAreRefused(t *testing.T) {
	cfg := fixture(t)
	// web is pinned to home-b, which runs the infrastructure stack too, and
	// keeps no secrets of its own.
	app := cfg.Apps["web"]
	cfg.Apps["infra"] = app
	_, err := render.Build(cfg, fixtureSecrets(t))
	if err == nil || !strings.Contains(err.Error(), "renders two files at") {
		t.Fatalf("an app named infra rendered: %v", err)
	}
}

// Only the infrastructure template's own services are marked as the
// foundation's in the infrastructure stack: a service it does not write,
// running a foundation image, is not.
func TestOnlyTheInfraTemplatesServicesAreMarked(t *testing.T) {
	cfg := fixture(t)
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	etcd, err := blueprints.FoundationImage(blueprints.FoundationEtcd)
	if err != nil {
		t.Fatal(err)
	}
	var files []render.File
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "/infra/compose.yaml") && strings.HasPrefix(f.Path, "vm/") {
			f.Content = strings.Replace(f.Content, "\nservices:\n", "\nservices:\n  backdoor:\n    image: "+etcd+"\n", 1)
			files = append(files, f)
		}
	}
	if len(files) != 1 || !strings.Contains(files[0].Content, "backdoor:") {
		t.Fatalf("no vm infra compose file to change: %d", len(files))
	}
	images, err := render.ListImages(cfg, fixtureSecrets(t), files)
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range images["vm"] {
		for _, s := range image.FoundationServices {
			if s == "backdoor" {
				t.Errorf("a service the infrastructure template does not write is marked: %+v", image)
			}
		}
	}
}

// infraServices names exactly the services the infrastructure template
// writes, so a service added to the template is not left unmarked, nor a
// name left over that the template no longer writes.
func TestInfraServicesAreTheTemplates(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range plan.Files {
		if !strings.HasSuffix(f.Path, "/infra/compose.yaml") {
			continue
		}
		services, err := blueprints.ComposeMounts(f.Content)
		if err != nil {
			t.Fatal(err)
		}
		for name := range services {
			seen[name] = true
			if !render.InfraServices[name] {
				t.Errorf("the infrastructure template writes %s, which infraServices does not name", name)
			}
		}
	}
	for name := range render.InfraServices {
		if !seen[name] {
			t.Errorf("infraServices names %s, which no site's infrastructure stack runs", name)
		}
	}
}

// Nothing the render writes leaves the deployment's root, except the
// WireGuard file wg-quick reads where it reads it; an app whose name walks
// out of the root is refused rather than written there.
func TestNoFileLeavesTheDeploymentRoot(t *testing.T) {
	for _, name := range []string{"../x", "..", "../../../etc/cron.d", "."} {
		cfg := fixture(t)
		cfg.Apps[name] = cfg.Apps["web"]
		if _, err := render.Build(cfg, fixtureSecrets(t)); err == nil || !strings.Contains(err.Error(), "outside the deployment") {
			t.Errorf("an app named %q rendered: %v", name, err)
		}
	}
}

// A compose file that is not a stack's own, one directory under the
// deployment root, is never skipped: its images would not be reviewed.
func TestAComposeFileOutsideAStackIsAnError(t *testing.T) {
	cfg := fixture(t)
	files := []render.File{{Path: "vm/" + cfg.Deployment().RelPath("a", "b", "compose.yaml"), Content: "services:\n  x:\n    image: ghcr.io/example/x:1\n"}}
	if _, err := render.ListImages(cfg, fixtureSecrets(t), files); err == nil {
		t.Error("a compose file at a/b/compose.yaml was skipped")
	}
	cfg.Apps["a/b"] = cfg.Apps["web"]
	if _, err := render.Build(cfg, fixtureSecrets(t)); err == nil {
		t.Error("an app named a/b rendered")
	}
}

// A setting the toolkit's own templates write into an env file is quoted
// there, so a newline in it cannot add a variable.
func TestSettingsInEnvFilesAreQuoted(t *testing.T) {
	cfg := fixture(t)
	cfg.Apps["auth"].Settings["file_backend"] = "database\nSTATIC_API_KEY=injected"
	cfg.Apps["health"].Settings["admin_group"] = "admins\nOIDC_ISSUER=https://evil.example"
	cfg.Apps["account"].Settings["member_group"] = "members\nACCOUNT_IDP_URL=https://evil.example"
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if !strings.HasSuffix(f.Path, "/.env") {
			continue
		}
		for _, line := range strings.Split(f.Content, "\n") {
			if strings.HasPrefix(line, "STATIC_API_KEY=injected") || strings.HasPrefix(line, "OIDC_ISSUER=https://evil.example") || strings.HasPrefix(line, "ACCOUNT_IDP_URL=https://evil.example") {
				t.Errorf("%s: a setting added the line %q", f.Path, line)
			}
		}
	}
}

// The toolkit's own compose templates quote every image reference, so even a
// reference that slipped past configuration cannot add a key, and pinning
// keeps the quotes.
func TestTheToolkitsImageLinesAreQuoted(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, f := range plan.Files {
		if !strings.HasSuffix(f.Path, "/infra/compose.yaml") && !strings.HasSuffix(f.Path, "/auth/compose.yaml") {
			continue
		}
		for _, line := range strings.Split(f.Content, "\n") {
			if ref, ok := strings.CutPrefix(strings.TrimSpace(line), "image: "); ok {
				checked++
				if !strings.HasPrefix(ref, `"`) {
					t.Errorf("%s: %s is not quoted", f.Path, line)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no image line checked")
	}
	got, err := render.PinImages("    image: \"a:1\"\n", map[string]string{"a:1": "a:1@sha256:00"}, map[string]int{"a:1": 1})
	if err != nil || got != "    image: \"a:1@sha256:00\"\n" {
		t.Errorf("pinned %q, %v", got, err)
	}
}

// jsonquote writes a string as a JSON string, for a catalog template that
// writes a setting into JSON.
func TestJSONQuote(t *testing.T) {
	got, err := render.JSONQuote(`a "b" \ c`)
	if err != nil || got != `"a \"b\" \\ c"` {
		t.Errorf("%s, %v", got, err)
	}
}

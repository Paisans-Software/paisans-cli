package render_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// infraImage is the ComposeImage of the infrastructure stack's compose
// service on site, failing the test when there is none.
func infraImage(t *testing.T, plan *render.Plan, site, service string) render.ComposeImage {
	t.Helper()
	for _, image := range plan.Images[site] {
		if image.Stack == "infra" && slices.Contains(image.Services, service) {
			return image
		}
	}
	t.Fatalf("%s's infrastructure stack runs no %s: %+v", site, service, plan.Images[site])
	return render.ComposeImage{}
}

func TestAnInfraOverrideWinsOverTheFoundation(t *testing.T) {
	cfg := fixture(t)
	const etcd = "gcr.io/etcd-development/etcd:v3.5.99"
	cfg.Infra.Images = map[string]string{"etcd": etcd}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	foundation, _ := blueprints.FoundationImage(blueprints.FoundationEtcd)
	image := infraImage(t, plan, "home-a", "etcd")
	if image.Image != etcd || image.Origin != "infra.images.etcd" || image.Replaces != foundation {
		t.Errorf("etcd is %+v, want %s from infra.images.etcd replacing %s", image, etcd, foundation)
	}
	if got := image.Setting(); got != "infra.images.etcd" {
		t.Errorf("Setting is %q", got)
	}
	haproxy := infraImage(t, plan, "home-a", "haproxy")
	if haproxy.Origin != render.OriginFoundation || haproxy.Replaces != "" {
		t.Errorf("an image nothing overrides is %+v", haproxy)
	}
}

// An override is the operator's choice, which nobody reviewed, so it is
// never marked the foundation's: not even when it names the foundation's own
// reference.
func TestAnOverrideIsNeverTheFoundations(t *testing.T) {
	cfg := fixture(t)
	same, _ := blueprints.FoundationImage(blueprints.FoundationEtcd)
	cfg.Infra.Images = map[string]string{"etcd": same}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	if image := infraImage(t, plan, "home-a", "etcd"); slices.Contains(image.FoundationServices, "etcd") {
		t.Errorf("an overridden etcd is marked the foundation's: %+v", image)
	}
}

// The fixture runs Postgres 18.
func TestASpiloOverrideAppliesOnlyToItsMajor(t *testing.T) {
	cfg := fixture(t)
	cfg.Infra.Postgres = map[string]map[string]string{"17": {"spilo": "example.org/pg-ha:17.4-1"}}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := blueprints.SpiloImage("18")
	if image := infraImage(t, plan, "home-a", "patroni"); image.Image != want || image.Origin != render.OriginFoundation {
		t.Errorf("a 17 override reached Postgres 18: %+v", image)
	}
	cfg.Infra.Postgres = map[string]map[string]string{"18": {"spilo": "example.org/pg-ha:18.0-1"}}
	if plan, err = render.Build(cfg, fixtureSecrets(t)); err != nil {
		t.Fatal(err)
	}
	if image := infraImage(t, plan, "home-a", "patroni"); image.Image != "example.org/pg-ha:18.0-1" || image.Origin != "infra.postgres.18.spilo" {
		t.Errorf("the 18 override was not taken: %+v", image)
	}
}

// One heartbeat image, one key: the override reaches the operators
// heartbeat in every Pocket ID stack too.
func TestTheHeartbeatOverrideReachesPocketID(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	const heartbeat = "docker.io/curlimages/curl:8.99.0"
	cfg.Infra.Images = map[string]string{"heartbeat": heartbeat}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "/auth/compose.yaml") {
			seen = true
			if !strings.Contains(f.Content, "image: \""+heartbeat+"\"\n") {
				t.Errorf("%s does not run the override:\n%s", f.Path, f.Content)
			}
		}
	}
	if !seen {
		t.Fatal("the fixture renders no Pocket ID stack")
	}
	for site, images := range plan.Images {
		for _, image := range images {
			if image.Image == heartbeat && len(image.FoundationServices) > 0 {
				t.Errorf("%s: the overridden heartbeat is marked the foundation's: %+v", site, image)
			}
			if image.Image == heartbeat && image.Setting() != "infra.images.heartbeat" {
				t.Errorf("%s: the heartbeat's Setting is %q", site, image.Setting())
			}
		}
	}
}

func TestAnAppOverrideSaysWhatItReplaces(t *testing.T) {
	cfg := fixture(t)
	app := cfg.Apps["talk"]
	app.Images = map[string]string{"app": "ghcr.io/example-org/mbin:1.0.0-fork"}
	cfg.Apps["talk"] = app
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, images := range plan.Images {
		for _, image := range images {
			if image.Stack == "talk" && image.Image == "ghcr.io/example-org/mbin:1.0.0-fork" {
				if image.Origin != "apps.talk.images.app" || image.Replaces == "" {
					t.Errorf("the talk override is %+v", image)
				}
				return
			}
		}
	}
	t.Fatal("no site runs the talk override")
}

// An origin labels only the reference it chose: an image a compose service
// runs that the app's override did not name (a template's hard-coded one)
// takes neither the override's key nor what it replaces, though the service
// shares the override's name. The fixture's talk declares images.app, which
// its app and messenger services both run; app is made to run another.
func TestAnOriginLabelsOnlyTheReferenceItChose(t *testing.T) {
	cfg := fixture(t)
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	const declared = "ghcr.io/example-org/mbin:v1.10.1-fork"
	const hardCoded = "ghcr.io/example-org/hard-coded:1.0.0"
	var files []render.File
	for _, f := range plan.Files {
		if f.Path == "home-a/srv/paisans/f2a9/talk/compose.yaml" {
			// app's own image line: the first naming declared after app:.
			at := strings.Index(f.Content, "\n  app:\n")
			if at < 0 {
				t.Fatalf("talk's compose file has no app service:\n%s", f.Content)
			}
			f.Content = f.Content[:at] + strings.Replace(f.Content[at:], declared, hardCoded, 1)
			files = append(files, f)
		}
	}
	if len(files) != 1 || !strings.Contains(files[0].Content, hardCoded) || !strings.Contains(files[0].Content, declared) {
		t.Fatalf("no talk compose file on home-a to change: %d", len(files))
	}
	images, err := render.ListImages(cfg, fixtureSecrets(t), files)
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, image := range images["home-a"] {
		switch image.Image {
		case hardCoded:
			seen++
			if image.Origin != "" || image.Replaces != "" || len(image.FoundationServices) > 0 {
				t.Errorf("the hard-coded image took an origin it did not have: %+v", image)
			}
		case declared:
			seen++
			if image.Origin != "apps.talk.images.app" || image.Replaces == "" {
				t.Errorf("the declared image lost its origin: %+v", image)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("home-a lists %d of the two talk images: %+v", seen, images["home-a"])
	}
}

// A reference the operator declared anywhere in the infrastructure stack is
// reviewed wherever it runs there: acme.image naming the foundation's etcd
// unmarks etcd on a site that runs no gateway, where no service runs the
// declaration itself.
func TestADeclaredReferenceIsReviewedWhereverItRunsInItsStack(t *testing.T) {
	cfg := fixture(t)
	etcd, err := blueprints.FoundationImage(blueprints.FoundationEtcd)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ACME.Image = etcd
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	var files []render.File
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "/infra/compose.yaml") {
			files = append(files, f)
		}
	}
	images, err := render.ListImages(cfg, fixtureSecrets(t), files)
	if err != nil {
		t.Fatal(err)
	}
	image := infraImage(t, &render.Plan{Images: images}, "home-a", "etcd")
	if slices.Contains(image.Services, "caddy") {
		t.Fatalf("home-a runs the gateway, so the declaration runs there itself: %+v", image)
	}
	if slices.Contains(image.FoundationServices, "etcd") {
		t.Errorf("etcd runs a reference acme.image declares, and is marked the foundation's: %+v", image)
	}
}

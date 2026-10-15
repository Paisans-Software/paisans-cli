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

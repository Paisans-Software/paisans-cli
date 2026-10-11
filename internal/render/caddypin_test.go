package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/acme"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// Every site running Caddy names the image as the configuration decides it,
// the published major version tag by default, and the plan says so for apply
// to resolve. A site running none has no entry.
func TestTheCaddyImageIsTheConfiguredTag(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	for _, site := range []string{"vm", "watch"} {
		if plan.CaddyImages[site] != acme.Published {
			t.Errorf("%s names %q, want %s", site, plan.CaddyImages[site], acme.Published)
		}
		compose := files[site+"/srv/paisans/f2a9/infra/compose.yaml"]
		if !strings.Contains(compose, "    image: "+acme.Published+"\n") {
			t.Errorf("%s's compose file does not name %s", site, acme.Published)
		}
	}
	if image, ok := plan.CaddyImages["home-a"]; ok {
		t.Errorf("home-a runs no Caddy and names %q", image)
	}
}

func TestPinCaddyImageReplacesTheOneLine(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	compose := planFiles(plan)["vm/srv/paisans/f2a9/infra/compose.yaml"]
	pinned := acme.Published + "@sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"
	got, err := render.PinCaddyImage(compose, acme.Published, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(got, pinned, acme.Published, 1) != compose {
		t.Error("pinning changed more than the image line")
	}

	// A file that does not name the rendered image once is not guessed at.
	if _, err := render.PinCaddyImage(compose, "ghcr.io/example-org/caddy:2", pinned); err == nil {
		t.Error("a file not naming the image was pinned")
	}
	twice := compose + "    image: " + acme.Published + "\n"
	if _, err := render.PinCaddyImage(twice, acme.Published, pinned); err == nil {
		t.Error("a file naming the image twice was pinned")
	}
}

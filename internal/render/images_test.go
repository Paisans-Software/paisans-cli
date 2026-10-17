package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/render"
)

func siteImage(plan *render.Plan, site, stack, image string) (render.ComposeImage, int) {
	var found render.ComposeImage
	n := 0
	for _, img := range plan.Images[site] {
		if img.Stack == stack && img.Image == image {
			found = img
			n++
		}
	}
	return found, n
}

// Every site running Caddy names the image as the configuration decides it,
// the foundation's track by default, and the plan lists it for apply to
// resolve. A site running none has no entry for it.
func TestTheCaddyImageIsListedAsConfigured(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	published, err := blueprints.FoundationImage(blueprints.FoundationCaddy)
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	for _, site := range []string{"vm", "watch"} {
		img, n := siteImage(plan, site, "infra", published)
		if n != 1 || img.Lines != 1 || img.App != "" || strings.Join(img.Services, ",") != "caddy" {
			t.Errorf("%s lists %+v (%d times), want Caddy's %s once on one line", site, img, n, published)
		}
		if img.Path != site+"/srv/paisans/f2a9/infra/compose.yaml" {
			t.Errorf("%s lists Caddy in %s", site, img.Path)
		}
		if !strings.Contains(files[img.Path], "    image: \""+published+"\"\n") {
			t.Errorf("%s's compose file does not name %s", site, published)
		}
	}
	for _, img := range plan.Images["home-a"] {
		if img.Stack == "infra" && img.Setting() == "acme.image" {
			t.Errorf("home-a runs no Caddy and lists %+v", img)
		}
	}
}

// Mbin's application and its messenger consumers run one image: the plan
// lists it once, by the configuration's one key, with the two lines that
// name it, on every site the clustered app runs on.
func TestTheSharedMbinImageIsListedOnceWithItsLines(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	const image = "ghcr.io/example-org/mbin:v1.10.1-fork"
	for _, site := range []string{"home-a", "home-b"} {
		img, n := siteImage(plan, site, "talk", image)
		if n != 1 {
			t.Fatalf("%s lists %s %d times", site, image, n)
		}
		if img.App != "talk" || img.Lines != 2 || strings.Join(img.Services, ",") != "app" {
			t.Errorf("%s lists %+v, want talk's app on two lines", site, img)
		}
		if got := strings.Count(planFiles(plan)[img.Path], "image: "+image+"\n"); got != img.Lines {
			t.Errorf("%s's file names it on %d lines, the plan says %d", site, got, img.Lines)
		}
		if img.Setting() != "apps.talk.images.app" {
			t.Errorf("the setting is %s", img.Setting())
		}
	}
	// The gate, which no file declares, is listed on the gateway like any
	// other app, under its blueprint's default image.
	gateImage, _ := blueprints.FoundationImage(blueprints.FoundationOAuth2Proxy)
	gate, n := siteImage(plan, "vm", "gate", gateImage)
	if n != 1 || gate.Lines != 1 || strings.Join(gate.Services, ",") != "app" {
		t.Errorf("the gate lists %+v (%d times)", gate, n)
	}
	// The infrastructure stack's images are listed too, since each follows
	// a track apply resolves, each under the infra key that decides it.
	infra := 0
	for _, img := range plan.Images["home-a"] {
		if img.Stack == "infra" {
			infra++
			if len(img.Services) != 1 {
				t.Fatalf("home-a lists its infrastructure's %+v", img)
			}
			want := "infra.images." + img.Services[0]
			if img.Services[0] == "patroni" {
				want = "infra.postgres.18.spilo"
			}
			if img.Setting() != want || img.Origin != render.OriginFoundation {
				t.Errorf("home-a lists its infrastructure's %+v, setting %q, want %q", img, img.Setting(), want)
			}
		}
	}
	if infra == 0 {
		t.Error("home-a lists none of its infrastructure's images")
	}
}

func TestPinImagesReplacesEveryLineNamingTheReference(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	const image = "ghcr.io/example-org/mbin:v1.10.1-fork"
	img, _ := siteImage(plan, "home-a", "talk", image)
	compose := planFiles(plan)[img.Path]
	pinned := image + "@sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"
	got, err := render.PinImages(compose, map[string]string{image: pinned}, map[string]int{image: img.Lines})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "    image: "+pinned+"\n") != 2 {
		t.Errorf("both lines were not pinned:\n%s", got)
	}
	if strings.ReplaceAll(got, pinned, image) != compose {
		t.Error("pinning changed more than the image lines")
	}

	// A file that names the reference on a different number of lines than
	// were rendered is not guessed at.
	if _, err := render.PinImages(compose, map[string]string{image: pinned}, map[string]int{image: 1}); err == nil {
		t.Error("a file naming the image twice was pinned as though once")
	}
	if _, err := render.PinImages(compose, map[string]string{"ghcr.io/example-org/mbin:v2": pinned}, map[string]int{"ghcr.io/example-org/mbin:v2": 1}); err == nil {
		t.Error("a file not naming the image was pinned")
	}
	// A longer reference sharing a prefix is a different image.
	prefixed := strings.Replace(compose, "image: "+image+"\n", "image: "+image+"-debug\n", 1)
	if _, err := render.PinImages(prefixed, map[string]string{image: pinned}, map[string]int{image: 2}); err == nil {
		t.Error("a reference with the same prefix was counted as the image")
	}
}

package apply_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// missing is what imagedigest.Registry returns for a tag the registry
// answered does not exist, and denied for one it refused to an anonymous
// pull.
func missing(ref string) error {
	return fmt.Errorf("ghcr.io answered that %s does not exist: %w", ref, imagedigest.ErrNotFound)
}

func denied(ref string) error {
	return fmt.Errorf("asking ghcr.io for %s: the token service answered 403 Forbidden: %w", ref, imagedigest.ErrDenied)
}

// mbinDefault is the fixture with talk on the mbin kind's default image.
func mbinDefault(t *testing.T) (*render.Plan, string) {
	t.Helper()
	def, ok := kinds.DefaultImage(config.Kind("mbin"), "app")
	if !ok || kinds.ParseReference(def).Tag == "" {
		t.Fatalf("the mbin kind's default %q names no tag", def)
	}
	return planWith(t, func(cfg *config.Config) {
		app := cfg.Apps["talk"]
		app.Images = nil
		cfg.Apps["talk"] = app
	}), def
}

// refusal plans home-a, which runs the fixture's own Mbin tag, with r
// resolving the images of p, and returns how the refusal reads.
func refusal(t *testing.T, p *render.Plan, r apply.Resolver) (hint, explain string) {
	t.Helper()
	host := siteAt(t, "home-a", &registry{digest: buildA})
	_, err := apply.Build("home-a", p, acmeModule(t), host, apply.ResolveImages(r))
	if err == nil {
		t.Fatal("planned an image that could not be resolved")
	}
	return ui.Describe(err)
}

func says(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("does not say %q:\n%s", w, text)
		}
	}
}

func saysNot(t *testing.T, text string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(text, w) {
			t.Errorf("says %q:\n%s", w, text)
		}
	}
}

// A tag the operator named that the registry does not have is a tag to
// check.
func TestAMissingOperatorTagSaysToCheckIt(t *testing.T) {
	p := planWith(t, func(cfg *config.Config) {
		app := cfg.Apps["talk"]
		app.Images = map[string]string{"app": "ghcr.io/example-org/mbin:v9.9.9-fork"}
		cfg.Apps["talk"] = app
	})
	ref := "ghcr.io/example-org/mbin:v9.9.9-fork"
	hint, explain := refusal(t, p, &registry{digest: buildA, fail: map[string]error{ref: missing(ref)}})
	if want := "`talk` image `" + ref + "` does not exist (ghcr.io: not found)"; hint != want {
		t.Errorf("hint\n%s\nwant\n%s", hint, want)
	}
	says(t, explain, "`apps.talk.images.app`")
	saysNot(t, explain, "reaches the registry")
}

// A registry that could not be reached is asked again later. The image the
// site runs is named, and why apply does not keep it.
func TestAnUnreachableRegistrySaysToRunAgain(t *testing.T) {
	p, def := mbinDefault(t)
	down := fmt.Errorf("asking ghcr.io for %s: %w", def, errors.New("dial tcp: lookup ghcr.io: no such host"))
	hint, explain := refusal(t, p, &registry{digest: buildA, fail: map[string]error{def: down}})
	if want := "could not reach ghcr.io to resolve `talk` image `" + def + "`"; hint != want {
		t.Errorf("hint\n%s\nwant\n%s", hint, want)
	}
	says(t, explain, "once this workstation reaches ghcr.io", "`apps.talk.images.app`", "a digest", "Apply does not keep the running `"+mbinTag+"`: it can't confirm that build is `"+kinds.ParseReference(def).Tag+"`.")
	saysNot(t, explain, "configuration's", "which this site runs", "is a build of")
}

// An image the registry will not show an anonymous pull is private, or
// named wrong.
func TestADeniedImageSaysItIsPrivateOrMisnamed(t *testing.T) {
	p, def := mbinDefault(t)
	hint, explain := refusal(t, p, &registry{digest: buildA, fail: map[string]error{def: denied(def)}})
	if want := "`talk` image `" + def + "` is not public (ghcr.io: access denied)"; hint != want {
		t.Errorf("hint\n%s\nwant\n%s", hint, want)
	}
	says(t, explain, "private", "name", "`apps.talk.images.app`")
	saysNot(t, explain, "reaches the registry")
}

// The line a refusal is shown on, and the section of a one site apply, are
// named for the site: the refusal does not name it again.
func TestARefusalDoesNotRepeatTheSite(t *testing.T) {
	p, def := mbinDefault(t)
	for name, err := range map[string]error{"missing": missing(def), "denied": denied(def), "down": errors.New("dial tcp: i/o timeout")} {
		hint, _ := refusal(t, p, &registry{digest: buildA, fail: map[string]error{def: err}})
		if strings.Contains(hint, "home-a") {
			t.Errorf("%s: the hint names the site: %s", name, hint)
		}
	}
}

// listed is a registry that also lists its repositories' tags, naming
// newest as the newest version, or failing with err.
type listed struct {
	*registry
	newest string
	err    error
}

func (l listed) NewestVersion(string) (string, error) { return l.newest, l.err }

// The advice to pin a version gives the newest published one as its
// example, read from the registry, so the example is never stale.
func TestTheAdviceNamesTheNewestPublishedVersion(t *testing.T) {
	p, def := mbinDefault(t)
	r := listed{registry: &registry{digest: buildA, fail: map[string]error{def: missing(def)}}, newest: "1.15.0-paisans"}
	_, explain := refusal(t, p, r)
	says(t, explain, "Check `apps.talk.images.app`: set it to a published tag (Eg: `ghcr.io/paisans-software/mbin:1.15.0-paisans`) or a digest.")

	ref := "ghcr.io/example-org/mbin:v9.9.9-fork"
	other := planWith(t, func(cfg *config.Config) {
		app := cfg.Apps["talk"]
		app.Images = map[string]string{"app": ref}
		cfg.Apps["talk"] = app
	})
	r = listed{registry: &registry{digest: buildA, fail: map[string]error{ref: missing(ref)}}, newest: "v1.10.1-fork"}
	_, explain = refusal(t, other, r)
	says(t, explain, "(Eg: `ghcr.io/example-org/mbin:v1.10.1-fork`)")
}

// Without a listing, or with one that failed or held no version, the
// example shows the shape of a version tag and no version.
func TestWithoutAListingTheAdviceShowsTheShape(t *testing.T) {
	p, def := mbinDefault(t)
	for name, r := range map[string]apply.Resolver{
		"no listing":   &registry{digest: buildA, fail: map[string]error{def: missing(def)}},
		"failed":       listed{registry: &registry{digest: buildA, fail: map[string]error{def: missing(def)}}, err: errors.New("the registry answered 500")},
		"none in list": listed{registry: &registry{digest: buildA, fail: map[string]error{def: missing(def)}}},
	} {
		_, explain := refusal(t, p, r)
		if !strings.Contains(explain, "(Eg: `ghcr.io/paisans-software/mbin:<version>`)") {
			t.Errorf("%s: %s", name, explain)
		}
	}
}

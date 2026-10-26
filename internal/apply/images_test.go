package apply_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const (
	buildA = "sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"
	buildB = "sha256:0c5a1ad0b7e4b2c6f0d3d0e5a2b9e8f7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1"

	// caddyTrack is the gateway's Caddy as the foundation names it, and
	// caddyTag the tag every test registry puts it on.
	caddyTrack   = "ghcr.io/paisans-software/caddy:2.*.*"
	caddyTag     = "ghcr.io/paisans-software/caddy:2.0.0"
	mbinTag      = "ghcr.io/example-org/mbin:v1.10.1-fork"
	infraCompose = "/srv/paisans/f2a9/infra/compose.yaml"
	talkCompose  = "/srv/paisans/f2a9/talk/compose.yaml"
	manifestFile = "/srv/paisans/f2a9/.paisans-manifest.json"
)

// registry is a fake resolver: each reference in digests names that build,
// any other names digest, and a reference in fail, or every one when err is
// set, cannot be resolved. versions answers Version, which makes it an
// apply.Versioner.
type registry struct {
	digest   string
	err      error
	digests  map[string]string
	fail     map[string]error
	versions map[string]string
	asked    []string

	platforms   map[string][]string
	platformErr error
	// platformsAsked counts Platforms calls, each a manifest GET on a real
	// registry.
	platformsAsked int
	// mu guards asked and platformsAsked: CheckPlatforms asks about images
	// at once.
	mu sync.Mutex
}

func (r *registry) Digest(ref string) (string, error) {
	r.mu.Lock()
	r.asked = append(r.asked, ref)
	r.mu.Unlock()
	if err := r.fail[ref]; err != nil {
		return "", err
	}
	if r.err != nil {
		return "", r.err
	}
	if d, ok := r.digests[ref]; ok {
		return d, nil
	}
	return r.digest, nil
}

// A plan with no resolver of its own starts a track that runs nowhere yet
// on its first tag, at one build, as `paisans` does through its registry.
func init() {
	apply.FreshImages = func() apply.Resolver {
		return &registry{digest: buildA}
	}
}

// Track puts a track on its first tag, the pattern with each `*` read as 0,
// failing as Digest does for the track itself.
func (r *registry) Track(ref string) (string, error) {
	if err := r.fail[ref]; err != nil {
		return "", err
	}
	if r.err != nil {
		return "", r.err
	}
	return strings.ReplaceAll(ref, "*", "0"), nil
}

func (r *registry) Version(ref string) (string, error) {
	return r.versions[ref], nil
}

// caddyAt is a registry naming caddy for Caddy's tag and buildA for every
// other image, so a test moves the gateway alone.
func caddyAt(caddy string) *registry {
	return &registry{digest: buildA, digests: map[string]string{caddyTag: caddy}}
}

func (r *registry) times(ref string) int {
	n := 0
	for _, asked := range r.asked {
		if asked == ref {
			n++
		}
	}
	return n
}

func manifestPins(t *testing.T, host *fakeHost) []render.ManifestPin {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(host.files[manifestFile]), &m); err != nil {
		t.Fatal(err)
	}
	return m.Pins
}

func manifestPin(t *testing.T, host *fakeHost, stack, service string) *render.ManifestPin {
	t.Helper()
	for _, pin := range manifestPins(t, host) {
		if pin.Stack == stack && pin.Service == service {
			return &pin
		}
	}
	return nil
}

func manifestCaddy(t *testing.T, host *fakeHost) *render.ManifestPin {
	t.Helper()
	return manifestPin(t, host, "infra", "caddy")
}

// gatewayAt applies the fixture's gateway with the registry naming build for
// Caddy and buildA for everything else.
func gatewayAt(t *testing.T, build string) *fakeHost {
	t.Helper()
	return siteAt(t, "vm", caddyAt(build))
}

// siteAt applies one site of the fixture with r resolving its images.
func siteAt(t *testing.T, site string, r apply.Resolver) *fakeHost {
	t.Helper()
	host := newHost()
	p, err := apply.Build(site, plan(t), acmeModule(t), host, apply.ResolveImages(r))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return host
}

func pinOf(p *apply.Plan, stack, image string) *apply.ImagePin {
	for i := range p.Pins {
		if p.Pins[i].Stack == stack && p.Pins[i].Image == image {
			return &p.Pins[i]
		}
	}
	return nil
}

func mbinPin(p *apply.Plan) *apply.ImagePin { return pinOf(p, "talk", mbinTag) }

// The configuration names Caddy by a tag that moves, and the host is handed
// the build that tag named when apply planned, with the record of it.
func TestTheCaddyTagIsPinnedToTheBuildItNames(t *testing.T) {
	defer apply.SetNow(func() time.Time { return time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC) })()
	host := newHost()
	digests := caddyAt(buildA)
	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if digests.times(caddyTag) != 1 {
		t.Fatalf("the registry was asked about %v, want the published tag once", digests.asked)
	}
	want := caddyTag + "@" + buildA
	if c := p.Caddy(); c == nil || c.Reference != want || c.Source != "registry" {
		t.Fatalf("planned %+v, want %s from the registry", c, want)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(host.files[infraCompose], "    image: \""+want+"\"\n") {
		t.Errorf("the compose file does not name the build:\n%s", host.files[infraCompose])
	}
	got := manifestCaddy(t, host)
	if got == nil || *got != (render.ManifestPin{Stack: "infra", Service: "caddy", Image: caddyTrack, Reference: want, ResolvedAt: "2026-10-11T17:00:00Z", Source: "registry"}) {
		t.Errorf("the manifest records %+v", got)
	}

	// The same build again moves nothing, and the record keeps the time it
	// went in.
	defer apply.SetNow(func() time.Time { return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC) })()
	again, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Writes()) != 0 || len(again.Actions) != 0 {
		t.Fatalf("the same build again would write %v and run %v", again.Writes(), again.Actions)
	}
	if err := apply.Execute(again, host); err != nil {
		t.Fatal(err)
	}
	if got := manifestCaddy(t, host); got == nil || got.ResolvedAt != "2026-10-11T17:00:00Z" {
		t.Errorf("the record of an unchanged build moved: %+v", got)
	}
}

// A new build behind the same tag is exactly one changed file, the gateway's
// compose file, so it is replaced through every gate an image change has, and
// the plan says which build replaces which.
func TestANewBuildIsAPlannedGatewayChange(t *testing.T) {
	host := gatewayAt(t, buildA)
	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(caddyAt(buildB)))
	if err != nil {
		t.Fatal(err)
	}
	writes := p.Writes()
	if len(writes) != 1 || writes[0].Path != infraCompose {
		t.Fatalf("a new build should move one file, got %v", writes)
	}
	if !p.GatewayChanging || p.ACMEModule == "" {
		t.Fatal("a new Caddy build is not checked as a gateway change")
	}
	if !p.Caddy().Moved() {
		t.Error("the plan does not call a new digest a move")
	}
	if moved := p.Moved(); len(moved) != 1 || moved[0].Stack != "infra" {
		t.Errorf("the plan lists %+v as moved", moved)
	}
	var said bool
	for _, n := range p.Notes {
		if strings.Contains(n.Text, buildB) && strings.Contains(n.Text, buildA) {
			said = true
		}
	}
	if !said {
		t.Errorf("no note names the old and the new build: %+v", p.Notes)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !host.ran("list-modules") {
		t.Error("the new build was not checked for its DNS module")
	}
	if got := manifestCaddy(t, host); got == nil || got.Reference != caddyTag+"@"+buildB {
		t.Errorf("the manifest records %+v after the move", got)
	}
}

// A registry that cannot be reached is no reason to stop or to move: the
// build the site runs is kept, and the operator told.
func TestAnUnreachableRegistryKeepsTheDeployedBuild(t *testing.T) {
	host := gatewayAt(t, buildA)
	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{err: errors.New("dial tcp: no route to host")}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Writes()) != 0 {
		t.Fatalf("an unreachable registry moved %v", p.Writes())
	}
	if c := p.Caddy(); c == nil || c.Source != "deployed" || c.Reference != caddyTag+"@"+buildA {
		t.Fatalf("planned %+v, want the deployed build kept", c)
	}
	var warned bool
	for _, n := range p.Notes {
		if n.Hint != "" && strings.Contains(n.Text, "no route to host") && strings.Contains(n.Text, caddyTag) {
			warned = true
		}
	}
	if !warned {
		t.Errorf("keeping the deployed build was not a warning: %+v", p.Notes)
	}
}

// With nothing deployed to keep, an unresolved tag would be written as it is
// and never move again, so the plan stops instead.
func TestAnUnreachableRegistryWithNothingDeployedStops(t *testing.T) {
	r := &registry{digest: buildA, fail: map[string]error{caddyTag: errors.New("dial tcp: no route to host")}}
	_, err := apply.Build("vm", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r))
	if err == nil {
		t.Fatal("a gateway was planned with an unresolved Caddy tag")
	}
	for _, want := range []string{caddyTrack, "no route to host", "acme.image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// A command writing the gateway's compose file for some other reason passes
// no resolver, and must not move Caddy to a build nobody planned.
func TestWithoutAResolverTheDeployedBuildIsKept(t *testing.T) {
	host := gatewayAt(t, buildA)
	p, err := apply.Build("vm", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Writes()) != 0 {
		t.Fatalf("a plan without a resolver moved %v", p.Writes())
	}
}

// acme.image naming a digest is that build, and nobody is asked about it.
func TestADeclaredDigestIsNotResolved(t *testing.T) {
	image := "ghcr.io/example-org/caddy-desec@" + buildB
	rendered := planWith(t, func(cfg *config.Config) { cfg.ACME.Image = image })
	digests := &registry{digest: buildA}
	p, err := apply.Build("vm", rendered, acmeModule(t), newHost(), apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if digests.times(image) != 0 || digests.times(caddyTag) != 0 {
		t.Errorf("the registry was asked about %v", digests.asked)
	}
	if c := p.Caddy(); c == nil || c.Reference != image || c.Source != "configured" {
		t.Errorf("planned %+v", c)
	}
}

// A site running no Caddy has no Caddy image to resolve.
func TestASiteWithoutCaddyAsksNothingAboutCaddy(t *testing.T) {
	digests := &registry{digest: buildA}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if digests.times(caddyTag) != 0 || p.Caddy() != nil {
		t.Errorf("a site without Caddy asked %v and planned %+v", digests.asked, p.Caddy())
	}
}

// An app's image named by a tag is pinned on every line naming it: Mbin's
// application and its messenger consumers run one image, and run one build
// of it. Each service the configuration names is recorded.
func TestAnAppTagIsPinnedOnEveryLineNamingIt(t *testing.T) {
	defer apply.SetNow(func() time.Time { return time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC) })()
	host := newHost()
	digests := &registry{digest: buildA}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if digests.times(mbinTag) != 1 {
		t.Errorf("the registry was asked about Mbin's tag %d times, want once", digests.times(mbinTag))
	}
	pin := mbinPin(p)
	want := mbinTag + "@" + buildA
	if pin == nil || pin.Reference != want || pin.Source != "registry" || strings.Join(pin.Services, ",") != "app" {
		t.Fatalf("planned %+v", pin)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	compose := host.files[talkCompose]
	if n := strings.Count(compose, "    image: "+want+"\n"); n != 2 {
		t.Errorf("the build is named on %d lines, want the app's and the messenger's:\n%s", n, compose)
	}
	if strings.Contains(compose, "image: "+mbinTag+"\n") {
		t.Error("a line still names the bare tag")
	}
	if got := manifestPin(t, host, "talk", "app"); got == nil || *got != (render.ManifestPin{Stack: "talk", Service: "app", Image: mbinTag, Reference: want, ResolvedAt: "2026-10-11T17:00:00Z", Source: "registry"}) {
		t.Errorf("the manifest records %+v", got)
	}
}

// An app image declared with a digest is that build, untouched.
func TestADeclaredAppDigestIsLeftAlone(t *testing.T) {
	image := "ghcr.io/example-org/mbin@" + buildB
	rendered := planWith(t, func(cfg *config.Config) {
		app := cfg.Apps["talk"]
		app.Images = map[string]string{"app": image}
		cfg.Apps["talk"] = app
	})
	digests := &registry{digest: buildA}
	host := newHost()
	p, err := apply.Build("home-a", rendered, acmeModule(t), host, apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if digests.times(image) != 0 {
		t.Errorf("a digest was resolved: %v", digests.asked)
	}
	pin := pinOf(p, "talk", image)
	if pin == nil || pin.Reference != image || pin.Source != "configured" {
		t.Errorf("planned %+v", pin)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(host.files[talkCompose], "    image: "+image+"\n"); n != 2 {
		t.Errorf("the declared digest is named on %d lines", n)
	}
}

// An app whose registry cannot be reached keeps the build it runs; a new
// stack, which runs none, is refused.
func TestAnUnreachableRegistryKeepsAnAppsBuildAndRefusesANewOne(t *testing.T) {
	host := siteAt(t, "home-a", &registry{digest: buildA})
	down := &registry{err: errors.New("dial tcp: no route to host")}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(down))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Writes()) != 0 {
		t.Fatalf("an unreachable registry moved %v", p.Writes())
	}
	if pin := mbinPin(p); pin == nil || pin.Source != "deployed" || pin.Reference != mbinTag+"@"+buildA {
		t.Errorf("planned %+v, want the deployed build kept", pin)
	}

	_, err = apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA, fail: map[string]error{mbinTag: errors.New("dial tcp: no route to host")}}))
	if err == nil {
		t.Fatal("a new Mbin stack was planned with an unresolved tag")
	}
	for _, want := range []string{mbinTag, "no route to host", "apps.talk.images.app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// A clustered app runs on home-a and home-b. An apply of home-a alone keeps
// it on the build home-b runs, and says a newer one exists, even on a home-a
// that runs none yet.
func TestASiteApplyHoldsAMultiSiteAppOnItsDeployedBuild(t *testing.T) {
	homeB := siteAt(t, "home-b", &registry{digest: buildA})
	peer := apply.PeerCompose{Site: "home-b", Stack: "talk", Content: homeB.files[talkCompose], Found: true}

	for name, host := range map[string]*fakeHost{"deployed": siteAt(t, "home-a", &registry{digest: buildA}), "new": newHost()} {
		t.Run(name, func(t *testing.T) {
			p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: buildB}), apply.OtherSites(peer))
			if err != nil {
				t.Fatal(err)
			}
			pin := mbinPin(p)
			if pin == nil || pin.Reference != mbinTag+"@"+buildA || pin.Source != "held" {
				t.Fatalf("planned %+v, want the build home-b runs", pin)
			}
			var said bool
			for _, n := range p.Notes {
				if n.Hint != "" && strings.Contains(n.Text, "home-b") && strings.Contains(n.Text, buildB) && strings.Contains(n.Text, "without --site") {
					said = true
				}
			}
			if !said {
				t.Errorf("no warning says the app stays and a newer build exists: %+v", p.Notes)
			}
			// An app with no other site in the plan's word moves as usual.
			wikiImage, _ := blueprints.DefaultImage(config.Blueprint("wikijs"), "app")
			if wiki := pinOf(p, "wiki", wikiImage); wiki == nil || !strings.HasSuffix(wiki.Reference, buildB) {
				t.Errorf("wiki planned %+v", wiki)
			}
		})
	}

	// No site runs it yet: it is resolved like any image.
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildB}), apply.OtherSites(apply.PeerCompose{Site: "home-b", Stack: "talk"}))
	if err != nil {
		t.Fatal(err)
	}
	if pin := mbinPin(p); pin == nil || pin.Reference != mbinTag+"@"+buildB || pin.Source != "registry" {
		t.Errorf("a first deployment planned %+v", pin)
	}
}

// Sites that already run different builds of a clustered app are refused,
// naming each site's build. Every image of the app's stack is held together,
// so the first one the file lists is the one named.
func TestDisagreeingSitesRefuse(t *testing.T) {
	homeB := siteAt(t, "home-b", &registry{digest: buildB})
	homeA := siteAt(t, "home-a", &registry{digest: buildA})
	peer := apply.PeerCompose{Site: "home-b", Stack: "talk", Content: homeB.files[talkCompose], Found: true}
	_, err := apply.Build("home-a", plan(t), acmeModule(t), homeA, apply.ResolveImages(&registry{digest: buildA}), apply.OtherSites(peer))
	if err == nil {
		t.Fatal("sites running two builds of one app were planned")
	}
	for _, want := range []string{"`home-a` runs `", "@" + buildA + "`;", "`home-b` runs `", "@" + buildB + "`", "without --site"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// A peer that cannot be read is left out with a warning when this site runs
// a build to stay on, and refuses a site that runs none.
func TestAnUnreadablePeer(t *testing.T) {
	peer := apply.PeerCompose{Site: "home-b", Stack: "talk", Err: errors.New("ssh: connect: no route to host")}
	host := siteAt(t, "home-a", &registry{digest: buildA})
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: buildB}), apply.OtherSites(peer))
	if err != nil {
		t.Fatal(err)
	}
	if pin := mbinPin(p); pin == nil || pin.Reference != mbinTag+"@"+buildA || pin.Source != "held" {
		t.Errorf("planned %+v, want home-a's own build kept", pin)
	}
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildB}), apply.OtherSites(peer)); err == nil || !strings.Contains(err.Error(), "home-b") {
		t.Errorf("a new site beside an unreadable one was planned: %v", err)
	}
}

// The dry run's line for a moved image names the app, the service, the
// reference as configured and both builds, by version where the build has
// a label.
func TestAMovedImageIsDescribedWithItsVersions(t *testing.T) {
	host := siteAt(t, "home-a", &registry{digest: buildA})
	r := &registry{digest: buildA, digests: map[string]string{mbinTag: buildB}, versions: map[string]string{
		mbinTag + "@" + buildA: "1.15.0-paisans",
		mbinTag + "@" + buildB: "1.16.0-paisans",
	}}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(r))
	if err != nil {
		t.Fatal(err)
	}
	moved := p.Moved()
	if len(moved) != 1 {
		t.Fatalf("moved %+v, want Mbin alone", moved)
	}
	want := "talk  app  " + mbinTag + "  1.15.0-paisans (sha256:b401d1cb1807) -> 1.16.0-paisans (sha256:0c5a1ad0b7e4)"
	if got := moved[0].Describe(); got != want {
		t.Errorf("described as\n%s\nwant\n%s", got, want)
	}
	// Without labels, the digests alone.
	moved[0].Version, moved[0].DeployedVersion = "", ""
	if got := moved[0].Describe(); got != "talk  app  "+mbinTag+"  sha256:b401d1cb1807 -> sha256:0c5a1ad0b7e4" {
		t.Errorf("described as %s", got)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(manifestPins(t, host), func(m render.ManifestPin) bool {
		return m.Stack == "talk" && m.Reference == mbinTag+"@"+buildB
	}) {
		t.Errorf("the manifest does not record the move: %+v", manifestPins(t, host))
	}
}

// ReadPeers reads the app's compose file on each other site, saying which
// have none.
func TestReadPeersReadsEachSitesComposeFile(t *testing.T) {
	homeB := siteAt(t, "home-b", &registry{digest: buildA})
	peers := apply.ReadPeers(plan(t).Deployment, "talk", map[string]apply.Transport{"home-b": homeB, "home-c": newHost()})
	if len(peers) != 2 || peers[0].Site != "home-b" || peers[1].Site != "home-c" {
		t.Fatalf("read %+v", peers)
	}
	if !peers[0].Found || peers[0].Content != homeB.files[talkCompose] || peers[0].Stack != "talk" {
		t.Errorf("home-b read as %+v", peers[0])
	}
	if peers[1].Found || peers[1].Err != nil {
		t.Errorf("home-c, which runs nothing, read as %+v", peers[1])
	}
}

// bareAt applies a site with no resolver, as an older toolkit did: every
// image is the bare configured tag.
func bareAt(t *testing.T, site string) *fakeHost {
	t.Helper()
	host := newHost()
	p, err := apply.Build(site, plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return host
}

// --only and Except leave the other stacks alone, so their images are not
// resolved, listed or refused over.
func TestOnlyAndExceptPinOnlyTheStacksTheyMove(t *testing.T) {
	host := siteAt(t, "home-a", &registry{digest: buildA})
	moving := &registry{digest: buildA, digests: map[string]string{mbinTag: buildB}}
	for name, opt := range map[string]apply.Option{"only": apply.Only("wiki"), "except": apply.Except("talk")} {
		t.Run(name, func(t *testing.T) {
			p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(moving))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Moved()) != 1 {
				t.Fatalf("a whole plan moves %+v, want Mbin", p.Moved())
			}
			p, err = apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(moving), opt)
			if err != nil {
				t.Fatal(err)
			}
			if moved := p.Moved(); len(moved) != 0 {
				t.Errorf("a plan leaving talk alone lists %+v as moved", moved)
			}
			if pin := mbinPin(p); pin != nil {
				t.Errorf("a plan leaving talk alone pins %+v", pin)
			}
			for _, n := range p.Notes {
				if strings.Contains(n.Text, mbinTag) {
					t.Errorf("a plan leaving talk alone says %s", n.Text)
				}
			}
			down := &registry{digest: buildA, fail: map[string]error{mbinTag: errors.New("dial tcp: i/o timeout")}}
			if _, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(down), opt); err != nil {
				t.Errorf("a registry failing for talk refused a plan leaving talk alone: %v", err)
			}
		})
	}
}

// Without a resolver the build the host runs is kept whatever the
// configuration now names, a moved tag or another repository, since a
// narrower command must not move an image.
func TestWithoutAResolverAnyDeployedBuildIsKept(t *testing.T) {
	for _, image := range []string{"ghcr.io/example-org/mbin:v2.0.0-fork", "ghcr.io/other-org/mbin:v1.10.1-fork"} {
		host := siteAt(t, "home-a", &registry{digest: buildA})
		before := host.files[talkCompose]
		rendered := planWith(t, func(cfg *config.Config) {
			app := cfg.Apps["talk"]
			app.Images = map[string]string{"app": image}
			cfg.Apps["talk"] = app
		})
		p, err := apply.Build("home-a", rendered, acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		pin := pinOf(p, "talk", image)
		if pin == nil || pin.Reference != mbinTag+"@"+buildA || pin.Source != "deployed" {
			t.Errorf("%s: planned %+v, want the deployed build", image, pin)
		}
		for _, c := range p.Writes() {
			if c.Path == talkCompose {
				t.Errorf("%s: a plan without a resolver rewrites talk's compose file", image)
			}
		}
		if err := apply.Execute(p, host); err != nil {
			t.Fatal(err)
		}
		if host.files[talkCompose] != before {
			t.Errorf("%s: talk's compose file changed", image)
		}
	}
}

// The first apply after an older toolkit pins a bare tag: that is not a new
// build the plan knows of, and is not reported as one.
func TestPinningABareTagIsNotAMove(t *testing.T) {
	host := bareAt(t, "home-a")
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	if moved := p.Moved(); len(moved) != 0 {
		t.Errorf("pinning a bare tag lists %+v as moved", moved)
	}
	pin := mbinPin(p)
	if pin == nil || !pin.FirstPin() || pin.Moved() {
		t.Fatalf("planned %+v, want a first pin", pin)
	}
	var first bool
	for _, f := range p.FirstPins() {
		if f.Image == mbinTag {
			first = true
			if !strings.Contains(f.Describe(), "pins "+mbinTag) {
				t.Errorf("described as %s", f.Describe())
			}
		}
	}
	if !first {
		t.Errorf("first pins %+v do not hold Mbin", p.FirstPins())
	}
	for _, n := range p.Notes {
		if strings.Contains(n.Text, mbinTag) && strings.Contains(n.Text, "new build") {
			t.Errorf("a first pin is called a new build: %s", n.Text)
		}
	}
	// A different bare tag is another build, and a move.
	other := planWith(t, func(cfg *config.Config) {
		app := cfg.Apps["talk"]
		app.Images = map[string]string{"app": "ghcr.io/example-org/mbin:v2.0.0-fork"}
		cfg.Apps["talk"] = app
	})
	p, err = apply.Build("home-a", other, acmeModule(t), host, apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	if pin := pinOf(p, "talk", "ghcr.io/example-org/mbin:v2.0.0-fork"); pin == nil || !pin.Moved() || pin.FirstPin() {
		t.Errorf("another tag planned %+v, want a move", pin)
	}
}

// A host running the bare tag, with the registry down, keeps what it runs:
// the stack is not refused for running nothing.
func TestAnUnreachableRegistryKeepsADeployedBareTag(t *testing.T) {
	host := bareAt(t, "home-a")
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{err: errors.New("dial tcp: i/o timeout")}))
	if err != nil {
		t.Fatalf("a host running the tag was refused: %v", err)
	}
	pin := mbinPin(p)
	if pin == nil || pin.Reference != mbinTag || pin.Source != "deployed" {
		t.Errorf("planned %+v, want the deployed tag kept", pin)
	}
	if len(p.Writes()) != 0 {
		t.Errorf("wrote %v", p.Writes())
	}
}

// A site still on the bare tag beside one pinned to a build of that tag is
// not a disagreement: the apply holds both on the build that is known.
func TestABareTagBesideItsPinnedBuildIsNotADisagreement(t *testing.T) {
	pinned := siteAt(t, "home-b", &registry{digest: buildA})
	bare := bareAt(t, "home-b")
	cases := map[string]struct {
		self *fakeHost
		peer string
	}{
		"this site bare": {bareAt(t, "home-a"), pinned.files[talkCompose]},
		"the other bare": {siteAt(t, "home-a", &registry{digest: buildA}), bare.files[talkCompose]},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			peer := apply.PeerCompose{Site: "home-b", Stack: "talk", Content: tc.peer, Found: true}
			p, err := apply.Build("home-a", plan(t), acmeModule(t), tc.self, apply.ResolveImages(&registry{digest: buildB}), apply.OtherSites(peer))
			if err != nil {
				t.Fatal(err)
			}
			if pin := mbinPin(p); pin == nil || pin.Reference != mbinTag+"@"+buildA || pin.Source != "held" {
				t.Errorf("planned %+v, want the known build held", pin)
			}
		})
	}
}

// Platforms makes the fake an apply.Platformer: each reference whose name
// (before @) is a key is published for those platforms, and any other for
// amd64 and arm64.
func (r *registry) Platforms(ref string) ([]string, error) {
	r.mu.Lock()
	r.platformsAsked++
	r.mu.Unlock()
	if r.platformErr != nil {
		return nil, r.platformErr
	}
	name, _, _ := strings.Cut(ref, "@")
	if p, ok := r.platforms[name]; ok {
		return p, nil
	}
	return []string{"amd64", "arm64"}, nil
}

func TestAnImageWithoutTheSitesPlatformIsRefusedInThePlan(t *testing.T) {
	host := newHost()
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"amd64"}}}
	_, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(r), apply.Arch("arm64"))
	if err == nil || !strings.Contains(err.Error(), "home-a (arm64): `"+mbinTag+"`") || !strings.Contains(err.Error(), "linux/amd64 only") {
		t.Fatalf("got %v", err)
	}
	for _, c := range host.commands {
		if strings.Contains(c, "docker pull") {
			t.Fatalf("pulled before refusing: %s", c)
		}
	}
}

func TestAVariantCountsAsItsArchitecture(t *testing.T) {
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"amd64", "arm64/v8"}}}
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r), apply.Arch("arm64")); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnreadablePlatformListIsANoteNotARefusal(t *testing.T) {
	r := &registry{digest: buildA, platformErr: errors.New("the registry answered 500")}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r), apply.Arch("arm64"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range p.Notes {
		found = found || strings.Contains(n.Hint, "platforms")
	}
	if !found {
		t.Fatalf("no note: %+v", p.Notes)
	}
}

func TestWithoutAnArchNoPlatformIsAsked(t *testing.T) {
	r := &registry{digest: buildA, platformErr: errors.New("must not be asked")}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Notes {
		if strings.Contains(n.Hint, "platforms") {
			t.Fatal("asked without an arch")
		}
	}
}

func TestANonLinuxPlatformIsNamedAsItIs(t *testing.T) {
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"windows/amd64"}}}
	_, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(r), apply.Arch("amd64"))
	if err == nil || !strings.Contains(err.Error(), "published for windows/amd64 only") {
		t.Fatalf("got %v", err)
	}
}

// CheckPlatforms is Build's check without a host: the images a render names
// for a site, each tag resolved first, so a run can refuse before it
// prepares any host.
func TestCheckPlatformsRefusesFromTheRenderAlone(t *testing.T) {
	r := &registry{digest: buildA, platforms: map[string][]string{mbinTag: {"amd64"}}}
	images := []render.ComposeImage{
		{Stack: "talk", App: "talk", Image: mbinTag, Services: []string{"app"}},
		{Stack: "talk", App: "talk", Image: mbinTag, Services: []string{"messenger"}},
		{Stack: "infra", Image: caddyTrack, Services: []string{"caddy"}},
	}
	notes, refusals := apply.CheckPlatforms("home-a", "arm64", images, r)
	if len(notes) != 0 {
		t.Errorf("notes: %+v", notes)
	}
	if len(refusals) != 1 || !strings.Contains(refusals[0].Error(), "home-a (arm64): `"+mbinTag+"` is published for linux/amd64 only") {
		t.Fatalf("got %v", refusals)
	}
	if r.times(mbinTag) != 1 {
		t.Errorf("resolved %s %d times", mbinTag, r.times(mbinTag))
	}
}

func TestCheckPlatformsAsksAPinnedImageDirectly(t *testing.T) {
	r := &registry{err: errors.New("must not resolve a digest"), platforms: map[string][]string{mbinTag: {"amd64"}}}
	_, refusals := apply.CheckPlatforms("home-a", "arm64", []render.ComposeImage{{Image: mbinTag + "@" + buildA}}, r)
	if len(refusals) != 1 {
		t.Fatalf("got %v", refusals)
	}
	if len(r.asked) != 0 {
		t.Errorf("resolved %v", r.asked)
	}
}

func TestCheckPlatformsNotesATagItCannotResolve(t *testing.T) {
	r := &registry{err: errors.New("the registry answered 500")}
	notes, refusals := apply.CheckPlatforms("home-a", "arm64", []render.ComposeImage{{Image: mbinTag}}, r)
	if len(refusals) != 0 {
		t.Fatalf("refused: %v", refusals)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "500") {
		t.Fatalf("notes: %+v", notes)
	}
}

func TestCheckPlatformsWithoutAnArchAsksNothing(t *testing.T) {
	r := &registry{digest: buildA, platformErr: errors.New("must not be asked")}
	notes, refusals := apply.CheckPlatforms("home-a", "", []render.ComposeImage{{Image: mbinTag}}, r)
	if len(notes)+len(refusals) != 0 || len(r.asked) != 0 {
		t.Fatalf("asked: %v %+v %v", r.asked, notes, refusals)
	}
}

// An override behind the track it replaced is a warning, naming the key
// and the newer release; it never stops the plan.
func TestAnOverrideBehindItsTrackIsAWarning(t *testing.T) {
	const old = "gcr.io/etcd-development/etcd:v3.4.30"
	rendered := planWith(t, func(c *config.Config) { c.Infra.Images = map[string]string{"etcd": old} })
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	var pin *apply.ImagePin
	for i := range p.Pins {
		if p.Pins[i].Origin == "infra.images.etcd" {
			pin = &p.Pins[i]
		}
	}
	if pin == nil {
		t.Fatalf("no pin from infra.images.etcd: %+v", p.Pins)
	}
	// The fake registry puts a track on its pattern with each * as 0.
	if !pin.Behind || pin.Replaced != "gcr.io/etcd-development/etcd:v3.5.0" {
		t.Errorf("the pin is %+v, want behind v3.5.0", pin)
	}
	var warned bool
	for _, n := range p.Notes {
		if n.Hint != "" && strings.Contains(n.Hint, "infra.images.etcd") && strings.Contains(n.Text, "v3.5.0") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning names the key and the newer release: %+v", p.Notes)
	}
}

func TestAnOverrideAheadOfItsTrackIsNotBehind(t *testing.T) {
	rendered := planWith(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "gcr.io/etcd-development/etcd:v3.5.21"}
	})
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range p.Pins {
		if pin.Origin == "infra.images.etcd" && pin.Behind {
			t.Errorf("v3.5.21 is called behind v3.5.0: %+v", pin)
		}
	}
}

// Two repositories' version schemes need not compare, so a fork is shown
// beside what it replaced and never judged.
func TestAnOverrideInAnotherRepositoryIsNotJudged(t *testing.T) {
	rendered := planWith(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "ghcr.io/example-org/etcd:v1.0.0"}
	})
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range p.Pins {
		if pin.Origin != "infra.images.etcd" {
			continue
		}
		if pin.Behind || pin.Replaced == "" {
			t.Errorf("a fork was judged, or what it replaced not shown: %+v", pin)
		}
		return
	}
	t.Fatal("no pin from infra.images.etcd")
}

func TestReferenceLabel(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1a", 32)
	cases := map[string]string{
		"gcr.io/etcd-development/etcd:v3.5.21@" + digest: "v3.5.21 (sha256:1a1a1a1a1a1a)",
		"gcr.io/etcd-development/etcd@" + digest:         "sha256:1a1a1a1a1a1a",
		"gcr.io/etcd-development/etcd:v3.5.21":           "v3.5.21",
	}
	for ref, want := range cases {
		if got := apply.ReferenceLabel(ref); got != want {
			t.Errorf("ReferenceLabel(%q) = %q, want %q", ref, got, want)
		}
	}
}

// A replaced reference pinned by digest is compared by its tag.
func TestAnOverrideBehindADigestPinnedReferenceIsBehind(t *testing.T) {
	const old = "gcr.io/etcd-development/etcd:v3.4.30"
	rendered := planWith(t, func(c *config.Config) { c.Infra.Images = map[string]string{"etcd": old} })
	replaced := "gcr.io/etcd-development/etcd:v3.5.21@" + buildB
	for site, images := range rendered.Images {
		for i := range images {
			if images[i].Origin == "infra.images.etcd" {
				images[i].Replaces = replaced
			}
		}
		rendered.Images[site] = images
	}
	p, err := apply.Build("home-a", rendered, acmeModule(t), newHost(), apply.ResolveImages(&registry{digest: buildA}))
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range p.Pins {
		if pin.Origin == "infra.images.etcd" {
			if !pin.Behind {
				t.Errorf("v3.4.30 is not behind %s: %+v", replaced, pin)
			}
			return
		}
	}
	t.Fatal("no pin from infra.images.etcd")
}

// TolerateUnresolved plans an image the registry cannot resolve as an
// unresolved pin, with the reason, instead of refusing, and leaves its line
// as rendered. Without it the same plan is refused.
func TestTolerateUnresolvedPlansAnImageItCannotResolve(t *testing.T) {
	down := errors.New("dial tcp: lookup ghcr.io: no such host")
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(&registry{err: down})); err == nil {
		t.Fatal("without the option, an image that cannot be resolved was planned")
	}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(&registry{err: down}), apply.TolerateUnresolved())
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(p.Pins) == 0 {
		t.Fatal("no pins")
	}
	for _, pin := range p.Pins {
		if pin.Source != "unresolved" || !strings.Contains(pin.Unresolved, "no such host") {
			t.Errorf("pin %+v", pin)
		}
		if pin.Moved() || pin.FirstPin() || pin.Behind {
			t.Errorf("an unresolved pin moves or lags: %+v", pin)
		}
	}
	for _, change := range p.Changes {
		if strings.Contains(apply.ChangeContent(change), "@sha256:") {
			t.Errorf("%s pins a build nobody resolved:\n%s", change.Path, apply.ChangeContent(change))
		}
	}
}

// A plan built with TolerateUnresolved lists images and is never applied:
// Execute refuses it before anything reaches the host.
func TestATolerantPlanCannotBeExecuted(t *testing.T) {
	host := newHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{err: errors.New("dial tcp: lookup ghcr.io: no such host")}), apply.TolerateUnresolved())
	if err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	err = apply.Execute(p, host)
	if err == nil || !strings.Contains(err.Error(), "this plan was built to list images, not to apply them") {
		t.Fatalf("err = %v", err)
	}
	if len(host.files) != 0 || len(host.commands) != 0 {
		t.Errorf("wrote %d files and ran %v", len(host.files), host.commands)
	}
}

package apply_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const (
	buildA = "sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186"
	buildB = "sha256:0c5a1ad0b7e4b2c6f0d3d0e5a2b9e8f7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1"

	infraCompose = "/srv/paisans/f2a9/infra/compose.yaml"
	manifestFile = "/srv/paisans/f2a9/.paisans-manifest.json"
)

// registry is a resolver that names one build for every tag, or fails.
type registry struct {
	digest string
	err    error
	asked  []string
}

func (r *registry) Digest(ref string) (string, error) {
	r.asked = append(r.asked, ref)
	return r.digest, r.err
}

func manifestCaddy(t *testing.T, host *fakeHost) *render.ManifestCaddy {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(host.files[manifestFile]), &m); err != nil {
		t.Fatal(err)
	}
	return m.Caddy
}

// gatewayAt applies the fixture's gateway with the registry naming build.
func gatewayAt(t *testing.T, build string) *fakeHost {
	t.Helper()
	host := newHost()
	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: build}))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return host
}

// The configuration names Caddy by a tag that moves, and the host is handed
// the build that tag named when apply planned, with the record of it.
func TestTheCaddyTagIsPinnedToTheBuildItNames(t *testing.T) {
	defer apply.SetNow(func() time.Time { return time.Date(2026, 10, 11, 17, 0, 0, 0, time.UTC) })()
	host := newHost()
	digests := &registry{digest: buildA}
	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if len(digests.asked) != 1 || digests.asked[0] != "ghcr.io/paisans-software/caddy:2" {
		t.Fatalf("the registry was asked about %v, want the published tag once", digests.asked)
	}
	want := "ghcr.io/paisans-software/caddy:2@" + buildA
	if p.Caddy == nil || p.Caddy.Reference != want || p.Caddy.Source != "registry" {
		t.Fatalf("planned %+v, want %s from the registry", p.Caddy, want)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(host.files[infraCompose], "    image: "+want+"\n") {
		t.Errorf("the compose file does not name the build:\n%s", host.files[infraCompose])
	}
	got := manifestCaddy(t, host)
	if got == nil || *got != (render.ManifestCaddy{Image: "ghcr.io/paisans-software/caddy:2", Reference: want, ResolvedAt: "2026-10-11T17:00:00Z", Source: "registry"}) {
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
	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: buildB}))
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
	if !p.Caddy.Moved() {
		t.Error("the plan does not call a new digest a move")
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
	if got := manifestCaddy(t, host); got == nil || got.Reference != "ghcr.io/paisans-software/caddy:2@"+buildB {
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
	if p.Caddy == nil || p.Caddy.Source != "deployed" || p.Caddy.Reference != "ghcr.io/paisans-software/caddy:2@"+buildA {
		t.Fatalf("planned %+v, want the deployed build kept", p.Caddy)
	}
	var warned bool
	for _, n := range p.Notes {
		if n.Hint != "" && strings.Contains(n.Text, "no route to host") {
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
	_, err := apply.Build("vm", plan(t), acmeModule(t), newHost(), apply.ResolveImages(&registry{err: errors.New("dial tcp: no route to host")}))
	if err == nil {
		t.Fatal("a gateway was planned with an unresolved Caddy tag")
	}
	for _, want := range []string{"ghcr.io/paisans-software/caddy:2", "no route to host", "acme.image"} {
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
	if len(digests.asked) != 0 {
		t.Errorf("the registry was asked about %v", digests.asked)
	}
	if p.Caddy == nil || p.Caddy.Reference != image || p.Caddy.Source != "configured" {
		t.Errorf("planned %+v", p.Caddy)
	}
}

// A site running no Caddy has no image to resolve.
func TestASiteWithoutCaddyAsksNothing(t *testing.T) {
	digests := &registry{digest: buildA}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), newHost(), apply.ResolveImages(digests))
	if err != nil {
		t.Fatal(err)
	}
	if len(digests.asked) != 0 || p.Caddy != nil {
		t.Errorf("a site without Caddy asked %v and planned %+v", digests.asked, p.Caddy)
	}
}

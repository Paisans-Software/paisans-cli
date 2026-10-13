package apply_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// appliedOn applies home-a for arch, as the first apply of the site.
func appliedOn(t *testing.T, arch string) *fakeHost {
	t.Helper()
	host := newHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: buildA}), apply.Arch(arch))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return host
}

// The manifest records the architecture the apply was made for.
func TestTheManifestRecordsTheArchitecture(t *testing.T) {
	host := appliedOn(t, "amd64")
	var m render.Manifest
	if err := json.Unmarshal([]byte(host.files[manifestFile]), &m); err != nil {
		t.Fatal(err)
	}
	if m.Arch != "amd64" {
		t.Errorf("the manifest records arch %q", m.Arch)
	}
}

// An apply that moves nothing, on the architecture the last one recorded,
// asks the registry for no platform list: each build already runs on the
// host, which proves it runs on that architecture, and a digest names one
// platform list. On a real registry each answer is a manifest GET, which
// Docker Hub counts against an anonymous allowance.
func TestABuildTheHostRunsIsNotCheckedAgain(t *testing.T) {
	host := appliedOn(t, "amd64")
	r := &registry{digest: buildA}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(r), apply.Arch("amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if r.platformsAsked != 0 {
		t.Errorf("asked for %d platform lists of builds the host runs", r.platformsAsked)
	}
	if len(p.Writes()) != 0 {
		t.Errorf("an unchanged apply writes %v", p.Writes())
	}
	// A new build is checked.
	moved := &registry{digest: buildB}
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(moved), apply.Arch("amd64")); err != nil {
		t.Fatal(err)
	}
	if moved.platformsAsked == 0 {
		t.Error("a new build was not checked")
	}
}

// A host whose architecture is not the one recorded, or whose manifest
// recorded none, is checked: a host rebuilt on another architecture with its
// files restored runs nothing it once proved.
func TestAnotherArchitectureIsCheckedAgain(t *testing.T) {
	host := appliedOn(t, "amd64")
	r := &registry{digest: buildA}
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(r), apply.Arch("arm64")); err != nil {
		t.Fatal(err)
	}
	if r.platformsAsked == 0 {
		t.Error("an architecture change was not checked")
	}

	host = appliedOn(t, "amd64")
	host.files[manifestFile] = strings.Replace(host.files[manifestFile], `"arch": "amd64",`, "", 1)
	r = &registry{digest: buildA}
	if _, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(r), apply.Arch("amd64")); err != nil {
		t.Fatal(err)
	}
	if r.platformsAsked == 0 {
		t.Error("a manifest that recorded no architecture was not checked")
	}
}

// A registry rate limiting this workstation keeps the build the site runs,
// and the plan says so in those words.
func TestARateLimitKeepsTheDeployedBuild(t *testing.T) {
	host := appliedOn(t, "amd64")
	limit := fmt.Errorf("ghcr.io answered 429 Too Many Requests, and says to retry after 3600: %w", imagedigest.ErrRateLimited)
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{err: limit}), apply.Arch("amd64"))
	if err != nil {
		t.Fatalf("a rate limit stopped an apply with builds to keep: %v", err)
	}
	if len(p.Writes()) != 0 {
		t.Errorf("a rate limit moved %v", p.Writes())
	}
	var said bool
	for _, n := range p.Notes {
		if strings.Contains(n.Hint, "rate limiting") && strings.Contains(n.Text, "retry after 3600") {
			said = true
		}
	}
	if !said {
		t.Errorf("no note says the registry is rate limiting: %+v", p.Notes)
	}
}

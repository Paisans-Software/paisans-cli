package apply_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const mbinRepo = "ghcr.io/example-org/mbin"

// removedRefs is every image `docker image rm` was asked for, by ID.
func removedRefs(host *fakeHost) []string {
	var out []string
	for _, command := range host.commands {
		if id, ok := strings.CutPrefix(command, "docker image rm "); ok {
			out = append(out, strings.Trim(id, "'"))
		}
	}
	return out
}

func hostManifest(t *testing.T, host *fakeHost) render.Manifest {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(host.files[manifestFile]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// recordHistory adds repositories to the manifest's record of what the site
// rendered before, as an earlier apply would have left it.
func recordHistory(t *testing.T, host *fakeHost, images ...render.ManifestImage) {
	t.Helper()
	m := hostManifest(t, host)
	m.Images = append(m.Images, images...)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	host.files[manifestFile] = string(data)
}

// applyAgain plans and executes a second apply of the site, reporting to a
// recorder it returns.
func applyAgain(t *testing.T, site string, host *fakeHost, opts ...apply.Option) (*apply.Plan, *ui.Recorder) {
	t.Helper()
	p, err := apply.Build(site, plan(t), acmeModule(t), host, opts...)
	if err != nil {
		t.Fatal(err)
	}
	progress := &ui.Recorder{}
	p.Report = progress
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	return p, progress
}

// An apply with nothing to do still sweeps, and so reaches a stack it did
// not touch: an old Mbin image an earlier --keep-images run left goes, the
// one talk runs stays, and so does one a stopped container was created from.
func TestAnApplyWithNothingToDoSweepsUntouchedStacks(t *testing.T) {
	host := applied(t, "home-a")
	host.images = imageLine(mbinRepo, "v1.10.1-fork", fakeID(talkImage)) +
		imageLine(mbinRepo, "v1.9.0", fakeID("kept back")) +
		imageLine(mbinRepo, "v1.8.0", fakeID("stopped"))
	host.containers = mbinRepo + ":v1.8.0\n"

	p, progress := applyAgain(t, "home-a", host)
	if len(p.Actions) != 0 {
		t.Fatalf("the second apply plans %v, want nothing", p.Actions)
	}
	if len(p.Sweep) != 1 || p.Sweep[0].Ref != mbinRepo+":v1.9.0" {
		t.Errorf("the plan sweeps %v, want only v1.9.0", p.Sweep)
	}
	if got := removedRefs(host); !slices.Equal(got, []string{fakeID("kept back")}) {
		t.Errorf("removed %v, want only v1.9.0", got)
	}
	if !progress.Has("detail", "swept "+mbinRepo+":v1.9.0") {
		t.Errorf("the sweep was not reported:\n%s", progress.Lines())
	}
}

// The sweep is the last step: after every stack's gate and the manifest.
func TestTheSweepRunsLast(t *testing.T) {
	host := applied(t, "home-a")
	host.images = imageLine(mbinRepo, "v1.9.0", fakeID("kept back"))
	applyAgain(t, "home-a", host, apply.Recreate("infra"))
	rm, gate := host.indexOf("docker image rm "), host.indexOf("/srv/paisans/f2a9/infra/compose.yaml ps --all")
	if gate < 0 || rm < gate {
		t.Errorf("the sweep ran before the infrastructure stack's gate:\n%s", strings.Join(host.commands, "\n"))
	}
}

// A repository the manifest records and the site no longer renders, an app
// taken out of the configuration, is swept. A repository never recorded is
// somebody else's and is never touched. What another site of the deployment
// renders is not kept on this one: a site is a machine, so another site's
// images are on its own.
func TestTheSweepReachesRecordedRepositoriesOnly(t *testing.T) {
	host := applied(t, "home-a")
	recordHistory(t, host, render.ManifestImage{Repository: "example/removed-app", References: []string{"example/removed-app:1"}})
	// home-a renders postgres:18-trixie, and vm's chat renders
	// postgres:17-trixie, which on home-a is an old version like any other.
	host.images = imageLine("example/removed-app", "1", fakeID("removed")) +
		imageLine("example/removed-app", "<none>", fakeID("removed, older")) +
		imageLine("example/operators-own", "1", fakeID("operator")) +
		imageLine("postgres", "17-trixie", fakeID("postgres:17-trixie")) +
		imageLine("postgres", "16-trixie", fakeID("postgres:16-trixie"))

	p, _ := applyAgain(t, "home-a", host)
	got := removedRefs(host)
	want := []string{fakeID("removed"), fakeID("removed, older"), fakeID("postgres:17-trixie"), fakeID("postgres:16-trixie")}
	if !slices.Equal(got, want) {
		t.Errorf("removed %v, want the removed app's two images, postgres:17-trixie and postgres:16-trixie", got)
	}
	var planned []string
	for _, s := range p.Sweep {
		planned = append(planned, s.Ref)
	}
	if strings.Join(planned, " ") != "example/removed-app:1 example/removed-app@"+strings.TrimPrefix(fakeID("removed, older"), "sha256:")[:12]+" postgres:17-trixie postgres:16-trixie" {
		t.Errorf("the plan sweeps %v", planned)
	}
}

// Caddy runs a build pinned by digest, which `docker image ls` shows
// untagged: it is kept by ID, and an older build of it is swept.
func TestTheSweepKeepsADigestPinnedCaddy(t *testing.T) {
	host := gatewayAt(t, buildA)
	const caddyRepo = "ghcr.io/paisans-software/caddy"
	pinned := caddyTag + "@" + buildA
	host.images = imageLine(caddyRepo, "<none>", fakeID(pinned)) +
		imageLine(caddyRepo, "<none>", fakeID("an older build"))

	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(caddyAt(buildA)))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if got := removedRefs(host); !slices.Equal(got, []string{fakeID("an older build")}) {
		t.Errorf("removed %v, want only the older build", got)
	}
}

// --keep-images reads nothing for the sweep and removes nothing.
func TestKeepImagesSweepsNothing(t *testing.T) {
	host := applied(t, "home-a")
	host.images = imageLine(mbinRepo, "v1.9.0", fakeID("kept back"))
	p, _ := applyAgain(t, "home-a", host, apply.KeepImages())
	if len(p.Sweep) != 0 || host.ran("paisans-sweep-end") || host.ran("docker image rm") {
		t.Errorf("--keep-images swept: planned %v, ran %v", p.Sweep, host.commands)
	}
}

// A partial apply is a step of a larger change, and the whole apply that
// finishes it sweeps: --only and a pass holding apps back read nothing for
// the sweep and remove nothing beyond what the stacks they move superseded.
func TestAPartialApplyDoesNotSweep(t *testing.T) {
	for name, opt := range map[string]apply.Option{"only": apply.Only("talk"), "except": apply.Except("talk")} {
		host := applied(t, "home-a")
		host.images = imageLine("example/removed-app", "1", fakeID("removed"))
		recordHistory(t, host, render.ManifestImage{Repository: "example/removed-app"})
		p, _ := applyAgain(t, "home-a", host, opt)
		if len(p.Sweep) != 0 || host.ran("paisans-sweep-end") || host.ran("docker image rm") {
			t.Errorf("%s: a partial apply swept: planned %v, ran %v", name, p.Sweep, host.commands)
		}
		// Its manifest still records the history, for the whole apply.
		if !slices.ContainsFunc(hostManifest(t, host).Images, func(i render.ManifestImage) bool { return i.Repository == "example/removed-app" }) {
			t.Errorf("%s: a partial apply dropped the recorded history", name)
		}
	}
}

// Another deployment's root on the host means images this apply cannot
// account for: the sweep removes nothing there, and the plan says why.
func TestAHostWithAnotherDeploymentIsNotSwept(t *testing.T) {
	host := applied(t, "home-a")
	host.others = []string{"b7c1"}
	host.images = imageLine(mbinRepo, "v1.9.0", fakeID("kept back"))
	p, progress := applyAgain(t, "home-a", host)
	if len(p.Sweep) != 0 || host.ran("docker image rm") {
		t.Errorf("a host with another deployment was swept: %v", p.Sweep)
	}
	var said bool
	for _, n := range p.Notes {
		if n.Hint == "" && strings.Contains(n.Text, "/srv/paisans/b7c1") {
			said = true
		}
	}
	if !said || !progress.Has("detail", "/srv/paisans/b7c1") {
		t.Errorf("the plan and the run do not say why nothing was swept: %+v\n%s", p.Notes, progress.Lines())
	}
}

// The sweep costs disk, never the apply: a failed read and a failed removal
// are warnings.
func TestAFailedSweepIsAWarning(t *testing.T) {
	host := applied(t, "home-a")
	host.images = imageLine(mbinRepo, "v1.9.0", fakeID("kept back"))
	host.fail = "docker image rm"
	_, progress := applyAgain(t, "home-a", host)
	if !progress.Has("warn", "home-a: old images left in place") {
		t.Errorf("the failed removal was not a warning:\n%s", progress.Lines())
	}

	host.fail = "paisans-sweep-end"
	p, progress := applyAgain(t, "home-a", host)
	if !progress.Has("warn", "home-a: old images left in place") {
		t.Errorf("the failed read was not a warning:\n%s", progress.Lines())
	}
	if len(p.Notes) == 0 || p.Notes[len(p.Notes)-1].Hint != "home-a: old images not listed" {
		t.Errorf("the plan does not note the failed read: %+v", p.Notes)
	}
}

// An image a stack about to be recreated still runs is that stack's prune,
// listed under it, and not the sweep's.
func TestAPrunedImageIsNotListedAsSwept(t *testing.T) {
	host := supersededHost()
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Sweep {
		for _, prune := range p.Prunes {
			if s.ID == prune.ID {
				t.Errorf("%s is listed as both a prune and a sweep", s.Ref)
			}
		}
	}
}

// The manifest records every repository the site rendered and every
// reference from it, and keeps what an earlier apply recorded. A manifest
// written before the record existed starts it with this apply.
func TestTheManifestAccumulatesWhatTheSiteRendered(t *testing.T) {
	host := applied(t, "home-a")
	m := hostManifest(t, host)
	m.Images = nil
	data, _ := json.Marshal(m)
	host.files[manifestFile] = string(data)

	applyAgain(t, "home-a", host)
	repos := map[string][]string{}
	for _, i := range hostManifest(t, host).Images {
		repos[i.Repository] = i.References
	}
	if !slices.Equal(repos[mbinRepo], []string{talkImage}) {
		t.Errorf("an old manifest's first record has %v for Mbin, want %s", repos[mbinRepo], talkImage)
	}

	recordHistory(t, host, render.ManifestImage{Repository: mbinRepo, References: []string{mbinRepo + ":v1.9.0"}})
	applyAgain(t, "home-a", host)
	repos = map[string][]string{}
	for _, i := range hostManifest(t, host).Images {
		repos[i.Repository] = i.References
	}
	if !slices.Equal(repos[mbinRepo], []string{talkImage, mbinRepo + ":v1.9.0"}) {
		t.Errorf("the record has %v for Mbin, want the earlier reference kept beside the current one", repos[mbinRepo])
	}
}

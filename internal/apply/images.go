package apply

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// Resolver says which build a tag names now, as the digest `docker pull`
// would record for it (`sha256:<hex>`). imagedigest.Registry is the one
// `paisans` uses; a test hands Build its own.
type Resolver interface {
	Digest(ref string) (string, error)
}

// Versioner is a Resolver that can also read the version label of a build
// named by its digest, empty when the build has none. Build asks it only
// about an image whose build moves, so the dry run can name the versions
// beside the digests. imagedigest.Registry and imagedigest.Cache are both.
type Versioner interface {
	Version(ref string) (string, error)
}

// ResolveImages has Build resolve every image the configuration names by a
// tag (render.Plan.Images: each app stack's images, and the gateway's Caddy)
// to the build that tag names now, and render that digest beside the tag on
// every line naming it. `paisans apply` passes it on every plan.
//
// A tag in a compose file never changes, and apply recreates a stack only
// when its file changes, so a new build behind a tag (a fork's stack-1 moved
// to a new release, Caddy's 2 rebuilt for a fix, a version tag a publisher
// rebuilt) would never reach a host. Pinning the digest at plan time turns a
// new build into a changed compose file: the dry run shows it, and the apply
// pulls, checks, validates and health gates the stack exactly as for any
// image change. One resolver serves every site of a run (imagedigest.Cache),
// so every site and every service planned in it get the same build of one
// tag.
//
// Without it, Build keeps the build the host already runs, and renders the
// tag as it is on a host that runs none. Commands that write a compose file as
// part of a narrower change (Eg: `site remove` moving a heartbeat, `rotate-key`
// switching an app's storage key) do that, so they never move an image to a
// build nobody planned.
func ResolveImages(r Resolver) Option {
	return func(o *options) { o.resolver = r }
}

// PeerCompose is an app's compose file as another site running the app
// holds it, for a plan of one of its sites that does not cover that one.
// Err is why it could not be read, nil when it was.
type PeerCompose struct {
	Site    string
	Stack   string
	Content string
	Found   bool
	Err     error
}

// OtherSites tells Build what the other sites of an app this site runs hold,
// when the apply does not plan them. `paisans apply --site` passes it for
// every app that runs on more than one site; an apply that plans every site
// passes none, and its sites resolve together through the run's one resolver.
//
// A clustered app runs one database from every site it is on, and two builds
// against one database is how a migration runs twice or not at all. So an
// apply that covers only some of its sites keeps the app on the build its
// other sites run, and refuses when they already run different ones: see
// holdImage.
func OtherSites(peers ...PeerCompose) Option {
	return func(o *options) { o.peers = append(o.peers, peers...) }
}

// ReadPeers reads, through each site's transport, the compose file of app's
// stack there, for OtherSites.
func ReadPeers(d deployment.Deployment, app string, sites map[string]Transport) []PeerCompose {
	names := make([]string, 0, len(sites))
	for name := range sites {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]PeerCompose, 0, len(names))
	for _, name := range names {
		content, found, err := sites[name].ReadFile(d.Compose(app))
		out = append(out, PeerCompose{Site: name, Stack: app, Content: content, Found: found, Err: err})
	}
	return out
}

// ImagePin is the build one image a site's compose file names is planned to
// run, and how Build came by it.
type ImagePin struct {
	// Stack is the stack whose compose file names the image: infra, or an
	// app's name. App is the app, empty for infra.
	Stack string
	App   string
	// Services are the configuration's names for it (Eg: app; caddy).
	Services []string
	// Image is the reference as the configuration names it.
	Image string
	// Reference is what the compose file names: Image with the digest
	// pinned beside it, Image itself when it already named one, or the
	// build an app's other sites run.
	Reference string
	// Deployed is what the compose file on the host names now for the same
	// services, empty when the host has none, or its services disagree.
	Deployed string
	// Source is render.ManifestPin's: registry, deployed, held or
	// configured.
	Source string
	// Version and DeployedVersion are the version labels of Reference and
	// Deployed, read only when the build moves and the resolver can, and
	// empty otherwise.
	Version         string
	DeployedVersion string
}

// Moved reports whether the plan changes which build the services run,
// comparing digests rather than references, since a deployment once pinned to
// a version tag names the same build differently.
//
// A deployed reference that names no digest (a stack an older toolkit
// applied, which runs a bare tag) names no one build: pinning a digest of the
// same tag beside it is a first pin (see FirstPin), not a move, and only
// another tag or another image is.
func (p ImagePin) Moved() bool {
	if p.Deployed == "" {
		return false
	}
	if digestOf(p.Deployed) != "" {
		return buildKey(p.Deployed) != buildKey(p.Reference)
	}
	return nameOf(p.Deployed) != nameOf(p.Reference)
}

// FirstPin reports whether the plan pins, for the first time, a tag the host
// runs bare: the build the tag names now is written beside it. Which build
// the host runs under the bare tag is not known, so it is not called a move;
// but the compose file changes, so the stack is recreated once, on the build
// pinned.
func (p ImagePin) FirstPin() bool {
	return p.Deployed != "" && digestOf(p.Deployed) == "" && digestOf(p.Reference) != "" && nameOf(p.Reference) == strings.TrimSpace(p.Deployed)
}

// Describe is the pin as the dry run lists a moved image: the app (or infra),
// the services, the reference as configured, then the build it leaves and
// the build it moves to, each by its version label when the build has one
// (Eg: talk  app  ghcr.io/paisans-software/mbin:stack-1  1.15.0-paisans
// (sha256:1a2b3c4d5e6f) -> 1.16.0-paisans (sha256:3c4d5e6f7a8b)).
//
// A first pin reads `<stack>  <services>  <image>  pins <image> to <build>`.
func (p ImagePin) Describe() string {
	if p.FirstPin() {
		return fmt.Sprintf("%s  %s  %s  pins %s to %s", p.Stack, strings.Join(p.Services, ","), p.Image, p.Deployed, buildLabel(p.Reference, p.Version))
	}
	return fmt.Sprintf("%s  %s  %s  %s -> %s", p.Stack, strings.Join(p.Services, ","), p.Image, buildLabel(p.Deployed, p.DeployedVersion), buildLabel(p.Reference, p.Version))
}

// buildLabel names one build for the dry run: its version and its short
// digest, the digest alone without a version, the reference without a
// digest.
func buildLabel(ref, version string) string {
	digest := digestOf(ref)
	if digest == "" {
		return ref
	}
	if short, ok := strings.CutPrefix(digest, "sha256:"); ok && len(short) > 12 {
		digest = "sha256:" + short[:12]
	}
	if version == "" {
		return digest
	}
	return version + " (" + digest + ")"
}

func digestOf(ref string) string {
	return kinds.ParseReference(ref).Digest
}

// buildKey is what identifies the build a reference runs: its digest, or the
// reference itself when it names none.
func buildKey(ref string) string {
	if d := digestOf(ref); d != "" {
		return d
	}
	return ref
}

// Caddy is the pin of this site's Caddy image, nil when the site runs no
// Caddy or this plan did not read its compose file.
func (p *Plan) Caddy() *ImagePin {
	for i := range p.Pins {
		if p.Pins[i].Stack == infraStack && p.Pins[i].App == "" {
			return &p.Pins[i]
		}
	}
	return nil
}

// Moved is every pin whose build this plan changes, in the order the plan
// lists them.
func (p *Plan) Moved() []ImagePin {
	var out []ImagePin
	for _, pin := range p.Pins {
		if pin.Moved() {
			out = append(out, pin)
		}
	}
	return out
}

// FirstPins is every pin that writes a digest beside a tag the host runs
// bare, in the order the plan lists them. See ImagePin.FirstPin.
func (p *Plan) FirstPins() []ImagePin {
	var out []ImagePin
	for _, pin := range p.Pins {
		if pin.FirstPin() {
			out = append(out, pin)
		}
	}
	return out
}

// pinImages pins every image a compose file names that images lists, as
// ResolveImages and OtherSites describe. compose is the file as rendered,
// current and found the host's copy. It returns the file to compare and
// write, the pins for the plan and its manifest, and notes for the operator.
func pinImages(site string, images []render.ComposeImage, compose, current string, found bool, r Resolver, peers []PeerCompose) (string, []ImagePin, []Note, error) {
	rendered, err := kinds.ComposeMounts(compose)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: reading the images its compose file names: %w", site, err)
	}
	// A host file that does not parse is not this function's to report: the
	// comparison that follows calls it a conflict.
	var host map[string]kinds.ServiceMounts
	if found {
		host, _ = kinds.ComposeMounts(current)
	}
	pinned, lines := map[string]string{}, map[string]int{}
	var pins []ImagePin
	var notes []Note
	for _, image := range images {
		services := servicesNaming(rendered, image.Image)
		var stackPeers []PeerCompose
		for _, peer := range peers {
			if peer.Stack == image.Stack {
				stackPeers = append(stackPeers, peer)
			}
		}
		pin, imageNotes, err := pinImage(site, image, services, host, r, stackPeers)
		if err != nil {
			return "", nil, nil, err
		}
		notes = append(notes, imageNotes...)
		if pin == nil {
			continue
		}
		if pin.Reference != image.Image {
			pinned[image.Image], lines[image.Image] = pin.Reference, image.Lines
		}
		if v, ok := r.(Versioner); ok && (pin.Moved() || pin.FirstPin()) {
			// Labels are for the operator's eye. A build without one, or a
			// registry that will not say, shows its digest alone.
			pin.Version, _ = v.Version(pin.Reference)
			if digestOf(pin.Deployed) != "" {
				pin.DeployedVersion, _ = v.Version(pin.Deployed)
			}
		}
		pins = append(pins, *pin)
	}
	out, err := render.PinImages(compose, pinned, lines)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: %w", site, err)
	}
	return out, pins, notes, nil
}

// pinImage decides the build of one image. services are the compose
// services that run it, host the host's compose file, nil when it has none.
// The pin is nil when nothing was pinned: no resolver, and no build of the
// tag on the host to keep.
func pinImage(site string, image render.ComposeImage, services []string, host map[string]kinds.ServiceMounts, r Resolver, peers []PeerCompose) (*ImagePin, []Note, error) {
	want := image.Image
	pin := &ImagePin{Stack: image.Stack, App: image.App, Services: image.Services, Image: want}
	deployed, _ := deployedRef(host, services)
	pin.Deployed = deployed
	ref := kinds.ParseReference(want)
	label := imageLabel(image)
	if r == nil {
		// No resolver is a command with a narrower purpose than moving
		// images: it keeps whatever the host runs, whatever the
		// configuration now names (another tag, a channel moved, another
		// repository), so it never moves an image to a build nobody
		// planned. The next apply shows and makes that move.
		if deployed != "" {
			pin.Reference, pin.Source = deployed, "deployed"
			return pin, nil, nil
		}
		if ref.Digest != "" {
			pin.Reference, pin.Source = want, "configured"
			return pin, nil, nil
		}
		// Nothing runs: the tag is rendered as it is, and the next apply
		// pins it.
		return nil, nil, nil
	}
	if ref.Digest != "" || ref.Tag == "" {
		// A digest names its build already, and the operator who wrote it
		// gets exactly that build. No tag is `latest`, which validate
		// refuses, and is rendered as it is for a caller that did not
		// validate.
		pin.Reference, pin.Source = want, "configured"
		return pin, nil, nil
	}

	if len(peers) > 0 {
		held, notes, err := holdImage(site, image, services, host, peers, r)
		if err != nil {
			return nil, nil, err
		}
		if held != "" {
			pin.Reference, pin.Source = held, "held"
			return pin, notes, nil
		}
		// No site of the app runs it yet: it is resolved like any other.
	}

	digest, failed := r.Digest(want)
	if failed == nil {
		pin.Reference, pin.Source = want+"@"+digest, "registry"
		return pin, resolvedNotes(site, label, *pin), nil
	}

	// The registry did not answer. What the host runs is what the operator
	// last saw planned, so it is the safe thing to keep, provided it is the
	// same tag, pinned or bare: another tag or another image is not the
	// configuration's.
	if deployed != "" && nameOf(deployed) == want {
		pin.Reference, pin.Source = deployed, "deployed"
		kept := fmt.Sprintf("%s, the build this site already runs", deployed)
		if digestOf(deployed) == "" {
			kept = fmt.Sprintf("%s as the site runs it, unpinned, until an apply can resolve it", deployed)
		}
		return pin, []Note{{
			Hint: fmt.Sprintf("%s: %s stays on the build it runs, %s could not be resolved", site, label, want),
			Text: fmt.Sprintf("%s: asking the registry which build %s names failed (%v), so the plan keeps %s. A newer build, if one exists, reaches the site on the next apply that can reach the registry.", site, want, failed, kept),
		}}, nil
	}
	runs := "This site runs no build of it to keep instead"
	if deployed != "" {
		runs = fmt.Sprintf("This site runs %s, which is not a build of it, so keeping that would not be the configuration's", deployed)
	}
	return nil, nil, fmt.Errorf("%s: could not resolve which build %s names, for %s: %v. %s, and a compose file naming only the tag would never move again. Run apply again once this workstation reaches the registry, or set %s to a digest", site, want, label, failed, runs, image.Setting())
}

// holdImage is the build an app this site shares with sites the apply does
// not cover must stay on: the one every site of it that runs it runs, this
// one included. It is empty when no site runs the app yet, and an error when
// the sites run different builds, or none can be read.
//
// The other sites are compared service by service, by the compose services
// that run the image here, since every site of an app renders the same
// template. A build is compared by its digest, or by its reference when it
// names none.
func holdImage(site string, image render.ComposeImage, services []string, host map[string]kinds.ServiceMounts, peers []PeerCompose, r Resolver) (string, []Note, error) {
	label := imageLabel(image)
	builds := map[string]string{}
	var unread, all []string
	var notes []Note
	consider := func(name string, mounts map[string]kinds.ServiceMounts) error {
		ref, mixed := deployedRef(mounts, services)
		if len(mixed) > 1 {
			return fmt.Errorf("%s runs %s from more than one build on %s (%s), so there is no one build for this apply to keep it on. Run `paisans apply` without --site, which plans every site of %s, to move it to one build", image.App, label, name, strings.Join(mixed, ", "), image.App)
		}
		if ref != "" {
			builds[name] = ref
		}
		return nil
	}
	if err := consider(site, host); err != nil {
		return "", nil, fmt.Errorf("%s: %w", site, err)
	}
	for _, peer := range peers {
		all = append(all, peer.Site)
		if peer.Err != nil {
			unread = append(unread, peer.Site)
			notes = append(notes, Note{
				Hint: fmt.Sprintf("%s: %s's build on %s not read", site, image.App, peer.Site),
				Text: fmt.Sprintf("%s: reading %s's compose file on %s failed (%v), so the build %s runs there is not compared with this site's", site, image.App, peer.Site, peer.Err, label),
			})
			continue
		}
		if !peer.Found {
			continue
		}
		mounts, err := kinds.ComposeMounts(peer.Content)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %s's compose file on %s is not readable (%v), so the build %s runs there is not known. Fix the file there, or run `paisans apply` without --site, which plans every site of %s", site, image.App, peer.Site, err, label, image.App)
		}
		if err := consider(peer.Site, mounts); err != nil {
			return "", nil, fmt.Errorf("%s: %w", site, err)
		}
	}

	switch agreed := agreement(builds); {
	case !agreed:
		var each []string
		for _, name := range siteNames(builds) {
			each = append(each, name+" runs "+builds[name])
		}
		return "", nil, fmt.Errorf("%s: %s runs on %s, and its sites run different builds of %s: %s. An apply covering only some of them cannot choose between them, and two builds against one database is how a migration runs twice or not at all. Run `paisans apply` without --site, which plans every site of %s and moves them to one build", site, image.App, strings.Join(append([]string{site}, all...), ", "), label, strings.Join(each, "; "), image.App)
	case len(builds) == 0 && len(unread) > 0:
		return "", nil, fmt.Errorf("%s: %s runs on %s as well, which could not be read, and no site of it that could be read runs %s yet, so this apply cannot tell which build to start it on. Run apply again once %s can be reached", site, image.App, strings.Join(unread, ", "), label, strings.Join(unread, ", "))
	case len(builds) == 0:
		return "", nil, nil
	}
	held := heldBuild(site, builds)

	// Say so, and say it louder when the tag names a newer build than the
	// one kept: that build is what the operator would have got.
	text := fmt.Sprintf("%s: %s also runs on %s, which this apply does not cover, so %s stays on %s, the build its sites run. Moving it to a new build takes an apply that covers all its sites: `paisans apply` without --site", site, image.App, strings.Join(all, ", "), label, held)
	note := Note{Text: text}
	if r != nil {
		if digest, err := r.Digest(image.Image); err == nil && digest != digestOf(held) {
			note.Hint = fmt.Sprintf("%s: %s stays on the build its other sites run", site, image.App)
			note.Text = fmt.Sprintf("%s. %s names a newer build, %s, which this apply does not move it to", text, image.Image, digest)
		}
	}
	return held, append(notes, note), nil
}

// agreement reports whether the sites' builds can be one build. Two digests
// that differ are two builds. A reference naming no digest (a site an older
// toolkit applied, which runs a bare tag) names no one build, so it agrees
// with a digest of the same tag and with the same bare tag, and with nothing
// else: it is not known to be the pinned build, and not known to differ, and
// holding it on the pinned one moves it to the build its other sites run.
func agreement(builds map[string]string) bool {
	digests := map[string]bool{}
	names := map[string]bool{}
	for _, ref := range builds {
		if d := digestOf(ref); d != "" {
			digests[d] = true
		}
		names[nameOf(ref)] = true
	}
	if len(digests) > 1 {
		return false
	}
	if len(digests) == 1 {
		for _, ref := range builds {
			if digestOf(ref) == "" && !sameTag(ref, builds) {
				return false
			}
		}
		return true
	}
	return len(names) <= 1
}

// sameTag reports whether bare, a reference with no digest, is the tag one
// of builds' digests was resolved from.
func sameTag(bare string, builds map[string]string) bool {
	for _, ref := range builds {
		if digestOf(ref) != "" && nameOf(ref) == bare {
			return true
		}
	}
	return false
}

// heldBuild is the build agreeing sites are held on: one pinned by digest
// when any site runs one, since that one is known, and this site's own name
// for it when it has one, so this site's file does not change for nothing.
func heldBuild(site string, builds map[string]string) string {
	own, ok := builds[site]
	if ok && digestOf(own) != "" {
		return own
	}
	for _, name := range siteNames(builds) {
		if digestOf(builds[name]) != "" {
			return builds[name]
		}
	}
	if ok {
		return own
	}
	return builds[siteNames(builds)[0]]
}

// nameOf is a reference without its digest: the repository and tag a build
// was resolved from, or the reference itself when it names no digest.
func nameOf(ref string) string {
	if parsed := kinds.ParseReference(ref); parsed.Digest != "" {
		return parsed.Name
	}
	return strings.TrimSpace(ref)
}

// deployedRef is the reference the given services of a compose file run,
// when they all run one; mixed lists each reference when they run more than
// one. Both are empty when the file names none of them.
func deployedRef(mounts map[string]kinds.ServiceMounts, services []string) (ref string, mixed []string) {
	seen := map[string]bool{}
	for _, service := range services {
		image := strings.TrimSpace(mounts[service].Image)
		if image != "" && !seen[image] {
			seen[image] = true
			mixed = append(mixed, image)
		}
	}
	sort.Strings(mixed)
	if len(mixed) == 1 {
		return mixed[0], mixed
	}
	return "", mixed
}

// fileImages are the images listed for the compose file at path.
func fileImages(images []render.ComposeImage, path string) []render.ComposeImage {
	var out []render.ComposeImage
	for _, image := range images {
		if image.Path == path {
			out = append(out, image)
		}
	}
	return out
}

// servicesNaming is every compose service of a rendered file running image.
func servicesNaming(mounts map[string]kinds.ServiceMounts, image string) []string {
	var out []string
	for name, m := range mounts {
		if strings.TrimSpace(m.Image) == image {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// imageLabel names an image for the operator: Caddy, or an app's service.
func imageLabel(image render.ComposeImage) string {
	if image.App == "" {
		return "Caddy"
	}
	return fmt.Sprintf("%s's %s image", image.App, strings.Join(image.Services, "/"))
}

// resolvedNotes say what resolving decided, when it decided anything the
// host does not already run.
func resolvedNotes(site, label string, pin ImagePin) []Note {
	switch {
	case pin.Deployed == "":
		return []Note{{Text: fmt.Sprintf("%s: %s, %s, resolves to %s", site, label, pin.Image, digestOf(pin.Reference))}}
	case pin.FirstPin():
		return []Note{{Text: fmt.Sprintf("%s: %s, %s, runs as a bare tag and is pinned for the first time, to %s, the build the tag names now; the %s stack's compose file changes once, so its containers are recreated once, on that build", site, label, pin.Image, digestOf(pin.Reference), pin.Stack)}}
	case pin.Moved():
		before := digestOf(pin.Deployed)
		if before == "" {
			before = pin.Deployed
		}
		return []Note{{Text: fmt.Sprintf("%s: %s, %s, names a new build, %s, where the site runs %s, so the %s stack's compose file changes and its containers are replaced and checked as for any new image", site, label, pin.Image, digestOf(pin.Reference), before, pin.Stack)}}
	}
	return nil
}

// pinRecord is what the manifest records of the builds this site runs after
// this plan, one entry per service: this plan's pins for each stack whose
// compose file it wrote or confirmed, and what the last apply recorded for
// every other stack the site still renders. A build already recorded keeps
// the time it was first resolved, so the record says when that build went in.
func pinRecord(plan *Plan) []render.ManifestPin {
	settled := plan.settledStacks()
	renders := map[string]bool{}
	for rel := range plan.rendered {
		if stack := stackOf(rel); stack != "" {
			renders[stack] = true
		}
	}
	type key struct{ stack, service string }
	old := map[key]render.ManifestPin{}
	var out []render.ManifestPin
	for _, rec := range plan.recordedPins {
		old[key{rec.Stack, rec.Service}] = rec
		if !settled[rec.Stack] && renders[rec.Stack] {
			out = append(out, rec)
		}
	}
	at := now().UTC().Format(time.RFC3339)
	for _, pin := range plan.Pins {
		if !settled[pin.Stack] {
			continue
		}
		for _, service := range pin.Services {
			if rec, ok := old[key{pin.Stack, service}]; ok && rec.Reference == pin.Reference {
				out = append(out, rec)
				continue
			}
			out = append(out, render.ManifestPin{Stack: pin.Stack, Service: service, Image: pin.Image, Reference: pin.Reference, ResolvedAt: at, Source: pin.Source})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stack != out[j].Stack {
			return out[i].Stack < out[j].Stack
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// settledStacks are the stacks whose compose file this plan leaves as
// rendered: written, or already identical. A conflict is not written, and a
// file held back or out of scope is not in Changes at all.
func (p *Plan) settledStacks() map[string]bool {
	out := map[string]bool{}
	for _, c := range p.Changes {
		if c.Kind != Conflict && isStackCompose(c) {
			out[c.Stack] = true
		}
	}
	return out
}

// siteNames is a map's keys, sorted.
func siteNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

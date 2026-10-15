package apply

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostarch"
	"github.com/paisans-software/paisans-cli/internal/imagedigest"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
// when its file changes, so a new build behind a tag (a series tag moved to a
// new release, Caddy's 2 rebuilt for a fix, a version tag a publisher
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

// FreshImages, when set, is what Build resolves an image through when the
// image follows a track, runs nowhere on the host yet, and the plan has no
// resolver of its own (ResolveImages). A command that starts a stack on a
// site that had none, such as `site add`, then starts it on the newest build
// of the track: a track is a pattern, which no host can pull. Every image
// the host already runs is kept, as without a resolver. `paisans` sets it.
var FreshImages func() Resolver

// unresolvedTrack is the first image line of compose still naming a track,
// empty when there is none.
func unresolvedTrack(compose string) string {
	for _, line := range strings.Split(compose, "\n") {
		if ref, _, ok := render.ImageLine(strings.TrimSpace(line)); ok && imagedigest.IsTrack(ref) {
			return ref
		}
	}
	return ""
}

// Tracker is a Resolver that can also put a track (imagedigest.IsTrack) on
// the newest tag matching it. imagedigest.Registry and imagedigest.Cache are
// both.
type Tracker interface {
	Track(ref string) (string, error)
}

// Platformer is a Resolver that can also name the platforms a build was
// published for. imagedigest.Registry and imagedigest.Cache are both.
type Platformer interface {
	Platforms(ref string) ([]string, error)
}

// goArchs are the architecture names an image index uses (Go's GOARCH),
// for telling an architecture from an OS in Build.Platform.
var goArchs = map[string]bool{"amd64": true, "arm64": true, "arm": true, "386": true, "ppc64le": true, "s390x": true, "riscv64": true, "mips64le": true}

// platformCheck refuses a pin whose build has no Linux platform for arch,
// before anything is pulled, so a dry run names it. A list that cannot be
// read is a note rather than a refusal: the pull on the host still says so,
// and a registry's bad moment should not stop an apply that would succeed.
func platformCheck(site, arch string, image ImagePin, r Resolver) (*Note, error) {
	p, ok := r.(Platformer)
	if !ok || arch == "" {
		return nil, nil
	}
	platforms, err := p.Platforms(image.Reference)
	if err != nil {
		return &Note{
			Hint: fmt.Sprintf("could not read the platforms of %s", ui.Name(image.Image)),
			Text: fmt.Sprintf("Reading which platforms %s was published for failed (%v), so the plan cannot say whether it runs on %s. The pull on %s says so if it does not.", ui.Name(image.Image), err, arch, site),
		}, nil
	}
	if platforms == nil || hostarch.Runs(platforms, arch) {
		return nil, nil
	}
	// Build.Platform names the OS only when it is not linux (windows/amd64),
	// so linux is put back where the first segment is an architecture.
	named := make([]string, len(platforms))
	for i, pl := range platforms {
		first, _, _ := strings.Cut(pl, "/")
		named[i] = pl
		if goArchs[first] {
			named[i] = "linux/" + pl
		}
	}
	return nil, fmt.Errorf("%s (%s): %s is published for %s only.", site, arch, ui.Name(image.Image), strings.Join(named, ", "))
}

// provenOn reports whether pin's build needs no platform check on this site:
// the host already runs that very build, by digest, and the site's
// architecture is the one the last apply recorded. A digest names one
// platform list, which the registry cannot change under it, and a host
// running the build proves it runs on its architecture, so asking the
// registry again proves nothing; Docker Hub counts every such manifest GET
// against an address's anonymous allowance. The architecture is compared
// because a host can be rebuilt on another one with its files restored, and
// a manifest that recorded none, or a plan that read none, is checked.
func provenOn(pin ImagePin, arch, recorded string) bool {
	d := digestOf(pin.Reference)
	return d != "" && d == digestOf(pin.Deployed) && arch != "" && arch == recorded
}

// CheckPlatforms is Build's platform check for the images a render names for
// site (render.Plan.Images), made without reading its host. A whole apply
// runs it on every site before it prepares any host, which a site's plan
// waits on, so a dry run of a new deployment names an image with no build
// for a site, and --execute refuses before the first host changes. Build
// still checks each pin as it plans.
//
// Each tag is resolved first, since an index names the platforms of a build
// and a tag is only a name for one; a reference pinned by digest is asked as
// it is. A tag that cannot be resolved is a note, as an unreadable platform
// list is: the plan or the pull says so.
func CheckPlatforms(site, arch string, images []render.ComposeImage, r Resolver) ([]Note, []error) {
	if _, ok := r.(Platformer); !ok || arch == "" {
		return nil, nil
	}
	var notes []Note
	var refusals []error
	seen := map[string]bool{}
	for _, image := range images {
		if seen[image.Image] {
			continue
		}
		seen[image.Image] = true
		pin := ImagePin{Image: image.Image, Reference: image.Image}
		if ref := blueprints.ParseReference(image.Image); ref.Digest == "" && ref.Tag != "" {
			tagged, err := image.Image, error(nil)
			if t, ok := r.(Tracker); ok && imagedigest.IsTrack(image.Image) {
				tagged, err = t.Track(image.Image)
			}
			digest := ""
			if err == nil {
				digest, err = r.Digest(tagged)
			}
			if err != nil {
				notes = append(notes, Note{
					Hint: fmt.Sprintf("could not resolve %s", ui.Name(image.Image)),
					Text: fmt.Sprintf("Resolving %s failed (%v), so the plan cannot say whether it runs on %s. The pull on %s says so if it does not.", ui.Name(image.Image), err, arch, site),
				})
				continue
			}
			pin.Reference = tagged + "@" + digest
		}
		note, err := platformCheck(site, arch, pin, r)
		if err != nil {
			refusals = append(refusals, err)
		}
		if note != nil {
			notes = append(notes, *note)
		}
	}
	return notes, refusals
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
// (Eg: talk  app  ghcr.io/example-org/mbin:v1-fork  1.15.0-fork
// (sha256:1a2b3c4d5e6f) -> 1.16.0-fork (sha256:3c4d5e6f7a8b)).
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
	return blueprints.ParseReference(ref).Digest
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
		if p.Pins[i].Stack == infraStack && p.Pins[i].App == "" && len(p.Pins[i].Services) == 1 && p.Pins[i].Services[0] == "caddy" {
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
func pinImages(site string, images []render.ComposeImage, compose, current string, found bool, r, fresh Resolver, peers []PeerCompose) (string, []ImagePin, []Note, error) {
	rendered, err := blueprints.ComposeMounts(compose)
	if err != nil {
		return "", nil, nil, fmt.Errorf("reading the images its compose file names: %w", err)
	}
	// A host file that does not parse is not this function's to report: the
	// comparison that follows calls it a conflict.
	var host map[string]blueprints.ServiceMounts
	if found {
		host, _ = blueprints.ComposeMounts(current)
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
		pin, imageNotes, err := pinImage(site, image, services, host, r, fresh, stackPeers)
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
		return "", nil, nil, err
	}
	return out, pins, notes, nil
}

// pinImage decides the build of one image. services are the compose
// services that run it, host the host's compose file, nil when it has none.
// The pin is nil when nothing was pinned: no resolver, and no build of the
// tag on the host to keep.
func pinImage(site string, image render.ComposeImage, services []string, host map[string]blueprints.ServiceMounts, r, fresh Resolver, peers []PeerCompose) (*ImagePin, []Note, error) {
	want := image.Image
	pin := &ImagePin{Stack: image.Stack, App: image.App, Services: image.Services, Image: want}
	deployed, _ := deployedRef(host, services)
	if imagedigest.IsTrack(deployed) {
		// No host can pull a track, so a file naming one runs nothing.
		deployed = ""
	}
	pin.Deployed = deployed
	ref := blueprints.ParseReference(want)
	label := imageLabel(image)
	if r == nil {
		// No resolver is a command with a narrower purpose than moving
		// images: it keeps whatever the host runs, whatever the
		// configuration now names (another tag, a tag moved, another
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
		if imagedigest.IsTrack(want) && fresh != nil {
			// A track is a pattern, not a tag a host can pull: one that
			// runs nowhere here yet is resolved for a command that starts
			// it (ResolveFresh).
			resolved, err := resolve(want, fresh)
			if err != nil {
				return nil, nil, unresolved(image, "", err, fresh)
			}
			pin.Reference, pin.Source = resolved, "registry"
			return pin, nil, nil
		}
		// Nothing runs: the tag is rendered as it is, and the next apply
		// pins it. A track rendered as it is cannot be pulled, so Build
		// refuses to write one (unresolvedTrack).
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

	resolved, failed := resolve(want, r)
	if failed == nil {
		pin.Reference, pin.Source = resolved, "registry"
		return pin, resolvedNotes(label, *pin), nil
	}

	// The registry gave no digest. What the host runs is what the operator
	// last saw planned, so it is the safe thing to keep, provided it is the
	// same tag, pinned or bare, or a build on the same track: another tag or
	// another image is not what the configuration names.
	if deployed != "" && (nameOf(deployed) == want || imagedigest.OnTrack(want, deployed)) {
		pin.Reference, pin.Source = deployed, "deployed"
		kept := fmt.Sprintf("keeps %s, the build this site runs", ui.Name(deployed))
		if digestOf(deployed) == "" {
			kept = fmt.Sprintf("keeps %s unpinned, as this site runs it, until an apply can resolve it", ui.Name(deployed))
		}
		hint := fmt.Sprintf("could not resolve %s %s; keeping the build this site runs", label, ui.Name(want))
		if errors.Is(failed, imagedigest.ErrRateLimited) {
			hint = fmt.Sprintf("%s is rate limiting this workstation; keeping the build this site runs of %s %s", imagedigest.Host(want), label, ui.Name(want))
		}
		return pin, []Note{{
			Hint: hint,
			Text: fmt.Sprintf("Resolving %s failed (%v), so the plan %s. A newer build, if one exists, arrives with the next apply that resolves %s.", ui.Name(want), failed, kept, ui.Name(want)),
		}}, nil
	}
	return nil, nil, unresolved(image, deployed, failed, r)
}

// resolve is the build want names now, as a reference with its digest: a
// track is first put on the newest tag that matches it, then that tag is
// resolved like any other.
func resolve(want string, r Resolver) (string, error) {
	tagged := want
	if imagedigest.IsTrack(want) {
		t, ok := r.(Tracker)
		if !ok {
			return "", fmt.Errorf("this resolver lists no tags")
		}
		var err error
		if tagged, err = t.Track(want); err != nil {
			return "", err
		}
	}
	digest, err := r.Digest(tagged)
	if err != nil {
		return "", err
	}
	return tagged + "@" + digest, nil
}

// unresolved is the refusal for an image whose tag could not be resolved,
// on a site that runs no build of that tag to keep. It names no site: the
// line it is shown on (converge) or the section above it (`apply --site`)
// already does.
//
// apply stops rather than carry on, for two reasons the message leaves out
// unless the operator needs them to act:
//   - it does not keep the image the site runs, because a different tag or
//     image is not what the configuration asks for, and apply cannot tell
//     whether it is a build of the requested tag;
//   - it does not write the bare tag, because a compose file naming only a
//     tag never changes, so Docker would never pull a newer build behind it
//     (see ResolveImages).
//
// The advice depends on what the registry said (imagedigest.ErrNotFound,
// imagedigest.ErrDenied, or no answer about the tag at all).
func unresolved(image render.ComposeImage, deployed string, failed error, r Resolver) error {
	want := image.Image
	named := imageLabel(image) + " " + ui.Name(want)
	setting := ui.Name(image.Setting())
	host := imagedigest.Host(want)
	if errors.Is(failed, imagedigest.ErrRateLimited) {
		// Asking again at once is refused again, and counts against the
		// same limit, so the advice is to wait.
		return &ui.Problem{
			Hint:    fmt.Sprintf("%s is rate limiting this workstation, so %s could not be resolved", host, named),
			Explain: "Nothing runs it on this site to keep. Run apply again once the registry's limit resets; its answer says when, if it said.",
			Cause:   failed,
		}
	}
	refused := errors.Is(failed, imagedigest.ErrNotFound) || errors.Is(failed, imagedigest.ErrDenied)
	if image.Setting() == "" || image.Origin == render.OriginFoundation && refused {
		// The core catalog's foundation image: the catalog's release fixes
		// it, and the setting, when there is one, holds a build until then.
		// One the registry could not be asked about takes the advice below,
		// which names its setting.
		explain := fmt.Sprintf("Run apply again once this workstation reaches %s.", host)
		if refused {
			explain = "It is the core catalog's own image (foundation.yaml), and its track names nothing this registry publishes anonymously. A release of the core catalog fixes it; pinning core to an older release in `catalogs` works until then."
			if image.Setting() != "" {
				explain += fmt.Sprintf(" So does setting %s to a published tag.", setting)
			}
		}
		return &ui.Problem{
			Hint:    fmt.Sprintf("could not resolve %s (%s)", named, host),
			Explain: explain,
			Cause:   failed,
		}
	}
	switch {
	case errors.Is(failed, imagedigest.ErrNotFound):
		return &ui.Problem{
			Hint:    fmt.Sprintf("%s does not exist (%s: not found)", named, host),
			Explain: fmt.Sprintf("Check %s: set it to a published tag (Eg: %s) or a digest.", setting, ui.Name(exampleTag(want, r))),
			Cause:   failed,
		}
	case errors.Is(failed, imagedigest.ErrDenied):
		// imagedigest asks anonymously, so a refusal is a private image or
		// a name the registry does not know and will not say so for.
		return &ui.Problem{
			Hint:    fmt.Sprintf("%s is not public (%s: access denied)", named, host),
			Explain: fmt.Sprintf("Either the image is private, which needs credentials this lookup does not use, or its name is wrong. Check %s, or set it to a digest.", setting),
			Cause:   failed,
		}
	}
	explain := fmt.Sprintf("Run apply again once this workstation reaches %s, or set %s to a digest.", host, setting)
	if deployed != "" {
		// Named without its digest: the tag is what the operator compares
		// with the configuration.
		// The request is named by its tag, or its digest, alone: the
		// repository is already in the hint.
		asked := want
		if ref := blueprints.ParseReference(want); ref.Digest != "" {
			asked = ref.Digest
		} else if ref.Tag != "" {
			asked = ref.Tag
		}
		explain += fmt.Sprintf("\nApply does not keep the running %s: it can't confirm that build is %s.", ui.Name(nameOf(deployed)), ui.Name(asked))
	}
	return &ui.Problem{
		Hint:    fmt.Sprintf("could not reach %s to resolve %s", host, named),
		Explain: explain,
		Cause:   failed,
	}
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
func holdImage(site string, image render.ComposeImage, services []string, host map[string]blueprints.ServiceMounts, peers []PeerCompose, r Resolver) (string, []Note, error) {
	app := ui.Name(image.App)
	builds := map[string]string{}
	var unread, all []string
	var notes []Note
	consider := func(name string, mounts map[string]blueprints.ServiceMounts) error {
		ref, mixed := deployedRef(mounts, services)
		if len(mixed) > 1 {
			return &ui.Problem{
				Hint:    fmt.Sprintf("%s runs more than one build on %s", app, ui.Name(name)),
				Explain: fmt.Sprintf("%s runs %s on %s, so this apply has no one build to keep. Run `paisans apply` without --site, which plans every site of %s and moves them to one build.", app, names(mixed, ", "), ui.Name(name), app),
			}
		}
		if ref != "" {
			builds[name] = ref
		}
		return nil
	}
	if err := consider(site, host); err != nil {
		return "", nil, err
	}
	for _, peer := range peers {
		all = append(all, peer.Site)
		if peer.Err != nil {
			unread = append(unread, peer.Site)
			notes = append(notes, Note{
				Hint: fmt.Sprintf("build of %s on %s not read", app, ui.Name(peer.Site)),
				Text: fmt.Sprintf("Reading the compose file of %s on %s failed (%v), so the build there is not compared with this site's.", app, ui.Name(peer.Site), peer.Err),
			})
			continue
		}
		if !peer.Found {
			continue
		}
		mounts, err := blueprints.ComposeMounts(peer.Content)
		if err != nil {
			return "", nil, &ui.Problem{
				Hint:    fmt.Sprintf("compose file of %s on %s is not readable", app, ui.Name(peer.Site)),
				Explain: fmt.Sprintf("This apply cannot tell which build %s runs there. Fix the file on %s, or run `paisans apply` without --site, which plans every site of %s.", app, ui.Name(peer.Site), app),
				Cause:   err,
			}
		}
		if err := consider(peer.Site, mounts); err != nil {
			return "", nil, err
		}
	}

	switch agreed := agreement(builds); {
	case !agreed:
		var each []string
		for _, name := range siteNames(builds) {
			each = append(each, ui.Name(name)+" runs "+ui.Name(builds[name]))
		}
		// Two builds against one database is how a migration runs twice or
		// not at all, so an apply of some sites must not pick one.
		return "", nil, &ui.Problem{
			Hint:    fmt.Sprintf("the sites of %s run different builds", app),
			Explain: fmt.Sprintf("%s. An apply of only some of them cannot choose one. Run `paisans apply` without --site, which plans every site of %s and moves them to one build.", strings.Join(each, "; "), app),
		}
	case len(builds) == 0 && len(unread) > 0:
		return "", nil, &ui.Problem{
			Hint:    fmt.Sprintf("cannot tell which build to start %s on", app),
			Explain: fmt.Sprintf("%s also runs on %s, which could not be read, and no site that could be read runs %s yet. Run apply again once %s can be reached.", app, names(unread, ", "), app, names(unread, " and ")),
		}
	case len(builds) == 0:
		return "", nil, nil
	}
	held := heldBuild(site, builds)

	// Say so, and say it louder when the tag names a newer build than the
	// one kept: that build is what the operator would have got.
	text := fmt.Sprintf("%s also runs on %s, which this apply does not cover, so %s stays on %s, the build its sites run. Moving %s to a new build takes `paisans apply` without --site.", app, names(all, ", "), app, ui.Name(held), app)
	note := Note{Text: text}
	if r != nil {
		if digest, err := r.Digest(image.Image); err == nil && digest != digestOf(held) {
			note.Hint = fmt.Sprintf("%s stays on the build its other sites run", app)
			note.Text = fmt.Sprintf("%s %s names a newer build, %s, which this apply does not move %s to.", text, ui.Name(image.Image), ui.Name(digest), app)
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
	if parsed := blueprints.ParseReference(ref); parsed.Digest != "" {
		return parsed.Name
	}
	return strings.TrimSpace(ref)
}

// deployedRef is the reference the given services of a compose file run,
// when they all run one; mixed lists each reference when they run more than
// one. Both are empty when the file names none of them.
func deployedRef(mounts map[string]blueprints.ServiceMounts, services []string) (ref string, mixed []string) {
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
func servicesNaming(mounts map[string]blueprints.ServiceMounts, image string) []string {
	var out []string
	for name, m := range mounts {
		if strings.TrimSpace(m.Image) == image {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// imageLabel names an image for the operator, ahead of its reference: the
// Caddy image, or an app's (Eg: `talk` image). The reference that follows
// says which of an app's images it is, and Setting names its service.
func imageLabel(image render.ComposeImage) string {
	if image.App == "" {
		if image.Setting() == "acme.image" {
			return "Caddy image"
		}
		return ui.Name(strings.Join(image.Services, ",")) + " image"
	}
	return ui.Name(image.App) + " image"
}

// resolvedNotes say what resolving decided, when it decided anything the
// host does not already run. Like unresolved, they name no site.
func resolvedNotes(label string, pin ImagePin) []Note {
	named := label + " " + ui.Name(pin.Image)
	stack := ui.Name(pin.Stack)
	switch {
	case pin.Deployed == "":
		return []Note{{Text: fmt.Sprintf("%s resolves to %s.", named, ui.Name(digestOf(pin.Reference)))}}
	case pin.FirstPin():
		// The compose file changes once, from the bare tag to the tag and
		// its digest, which is why the stack is recreated even though the
		// build may be the one already running.
		return []Note{{Text: fmt.Sprintf("%s runs unpinned. This apply pins it to %s, the build the tag names now, and recreates the %s stack once.", named, ui.Name(digestOf(pin.Reference)), stack)}}
	case pin.Moved():
		before := digestOf(pin.Deployed)
		if before == "" {
			before = pin.Deployed
		}
		return []Note{{Text: fmt.Sprintf("%s names a new build, %s, where the site runs %s. The %s stack's containers are replaced and checked as for any new image.", named, ui.Name(digestOf(pin.Reference)), ui.Name(before), stack)}}
	}
	return nil
}

// Lister names the newest version-shaped tag of a reference's repository.
// imagedigest.Registry and imagedigest.Cache are both.
type Lister interface {
	NewestVersion(ref string) (string, error)
}

// exampleTag is a published tag of ref's repository for advice to show: the
// newest version r lists, or, when r lists none or cannot, the repository
// with a placeholder for the version (Eg: ghcr.io/example/mbin:<version>).
// It is read from the registry rather than written here so the example is
// never a version the registry has moved past.
func exampleTag(ref string, r Resolver) string {
	name := blueprints.ParseReference(ref).Name
	if l, ok := r.(Lister); ok {
		if newest, err := l.NewestVersion(ref); err == nil && newest != "" {
			return name + ":" + newest
		}
	}
	return name + ":<version>"
}

// names is each of list as a name (see ui.Name), joined by sep.
func names(list []string, sep string) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = ui.Name(s)
	}
	return strings.Join(out, sep)
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

// Arch has Build refuse an image with no build for the site's architecture
// (amd64, arm64), before anything is pulled. Empty, or not given, checks
// nothing, which is a site whose architecture is not known yet.
func Arch(arch string) Option {
	return func(o *options) { o.arch = arch }
}

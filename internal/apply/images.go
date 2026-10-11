package apply

import (
	"fmt"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// Resolver says which build a tag names now, as the digest `docker pull`
// would record for it (`sha256:<hex>`). imagedigest.Registry is the one
// `paisans` uses; a test hands Build its own.
type Resolver interface {
	Digest(ref string) (string, error)
}

// ResolveImages has Build resolve the Caddy image a site renders, when it is
// a tag that moves, to the build that tag names now, and render that digest
// into the infrastructure compose file. `paisans apply` passes it on every
// plan.
//
// The configuration names Caddy by a tag that moves (acme.Published), so that
// a security rebuild needs no toolkit release. A tag in the compose file would
// never change, and apply recreates a stack only when its file changes, so a
// new build would never reach a host. Pinning the digest at plan time turns a
// new build into a changed compose file: the dry run shows it, and the apply
// pulls, module checks, validates and health gates the gateway exactly as for
// any image change.
//
// Without it, Build keeps the build the host already runs, and renders the
// tag as it is on a host that runs none. Commands that write a gateway's
// compose file as part of a narrower change (Eg: `site remove` moving a
// heartbeat) do that, so they never move Caddy to a build nobody planned.
func ResolveImages(r Resolver) Option {
	return func(o *options) { o.resolver = r }
}

// CaddyPin is the build of its image a site's Caddy is planned to run, and
// how Build came by it.
type CaddyPin struct {
	// Image is the reference as the configuration names it.
	Image string
	// Reference is what the compose file names: Image with the digest
	// pinned beside it, or Image itself when it already named one.
	Reference string
	// Deployed is what the compose file on the host names now, empty when
	// the host has none.
	Deployed string
	// Source is render.ManifestCaddy's: registry, deployed or configured.
	Source string
}

// Moved reports whether the plan changes which build the site's Caddy runs,
// comparing digests rather than references, since a deployment once pinned to
// a version tag names the same build differently.
func (p CaddyPin) Moved() bool {
	return p.Deployed != "" && digestOf(p.Deployed) != digestOf(p.Reference)
}

func digestOf(ref string) string {
	return kinds.ParseReference(ref).Digest
}

// pinCaddy pins the Caddy image a site's infrastructure compose file names,
// as ResolveImages describes. compose is the file as rendered, current and
// found the host's copy. It returns the file to compare and write, the pin
// for the plan and its manifest, nil when nothing was pinned, and a note for
// the operator, nil when there is nothing to say.
func pinCaddy(site, want, compose, current string, found bool, r Resolver) (string, *CaddyPin, *Note, error) {
	pin := &CaddyPin{Image: want}
	if found {
		pin.Deployed = composeCaddyImage(current)
	}
	ref := kinds.ParseReference(want)
	if ref.Digest != "" || ref.Tag == "" {
		// A digest names its build already. No tag is `latest`, which
		// validate refuses on the gateway, and is rendered as it is for a
		// caller that did not validate.
		pin.Reference, pin.Source = want, "configured"
		return compose, pin, nil, nil
	}

	var failed error
	if r != nil {
		digest, err := r.Digest(want)
		if err == nil {
			pin.Reference, pin.Source = want+"@"+digest, "registry"
			pinned, err := render.PinCaddyImage(compose, want, pin.Reference)
			if err != nil {
				return "", nil, nil, err
			}
			return pinned, pin, resolvedNote(site, *pin), nil
		}
		failed = err
	}

	// The registry did not answer, or was not asked. The build the host runs
	// is the one the operator last saw planned, so it is the safe one to keep,
	// provided it is a build of the same tag: one of another tag or another
	// image is not the configuration's.
	if kinds.ParseReference(pin.Deployed).Name == want && digestOf(pin.Deployed) != "" {
		pin.Reference, pin.Source = pin.Deployed, "deployed"
		pinned, err := render.PinCaddyImage(compose, want, pin.Reference)
		if err != nil {
			return "", nil, nil, err
		}
		var note *Note
		if failed != nil {
			note = &Note{
				Hint: fmt.Sprintf("%s: Caddy stays on the build it runs, %s could not be resolved", site, want),
				Text: fmt.Sprintf("%s: asking the registry which build %s names failed (%v), so the plan keeps %s, the build this site already runs. A newer build, if one exists, reaches the site on the next apply that can reach the registry.", site, want, failed, pin.Deployed),
			}
		}
		return pinned, pin, note, nil
	}
	if failed != nil {
		return "", nil, nil, fmt.Errorf("%s: could not resolve which build %s names: %v. This site runs no build of it to keep instead, and a compose file naming only the tag would never move again. Run apply again once this workstation reaches the registry, or set acme.image to a digest", site, want, failed)
	}
	return compose, nil, nil, nil
}

// resolvedNote says what resolving decided, when it decided anything the
// host does not already run.
func resolvedNote(site string, pin CaddyPin) *Note {
	switch {
	case pin.Deployed == "":
		return &Note{Text: fmt.Sprintf("%s: Caddy's %s resolves to %s", site, pin.Image, digestOf(pin.Reference))}
	case pin.Moved():
		before := digestOf(pin.Deployed)
		if before == "" {
			before = pin.Deployed
		}
		return &Note{Text: fmt.Sprintf("%s: Caddy's %s names a new build, %s, where the site runs %s, so the infrastructure compose file changes and Caddy is replaced and checked as for any new image", site, pin.Image, digestOf(pin.Reference), before)}
	}
	return nil
}

// composeCaddyImage is the image the caddy service of an infrastructure
// compose file names, empty when it has none or cannot be read. A file the
// host holds that does not parse is somebody else's problem to report: the
// comparison that follows calls it a conflict.
func composeCaddyImage(compose string) string {
	services, err := kinds.ComposeMounts(compose)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(services["caddy"].Image)
}

// caddyRecord is what the manifest records for the site's Caddy after this
// plan: its own pin when it wrote or confirmed the compose file, and what the
// last apply recorded otherwise. A build already recorded keeps the time it
// was first resolved, so the record says when that build went in.
func caddyRecord(plan *Plan) *render.ManifestCaddy {
	pin := plan.Caddy
	if pin == nil || !plan.composeSettled() {
		return plan.recordedCaddy
	}
	if old := plan.recordedCaddy; old != nil && old.Reference == pin.Reference {
		return old
	}
	return &render.ManifestCaddy{
		Image:      pin.Image,
		Reference:  pin.Reference,
		ResolvedAt: now().UTC().Format(time.RFC3339),
		Source:     pin.Source,
	}
}

// composeSettled reports whether this plan leaves the infrastructure compose
// file as rendered: written, or already identical. A conflict is not written,
// and a file held back or out of scope is not in Changes at all.
func (p *Plan) composeSettled() bool {
	want := remoteRoot + p.Deployment.RelPath(gatewayCompose)
	for _, c := range p.Changes {
		if c.Path == want {
			return c.Kind != Conflict
		}
	}
	return false
}

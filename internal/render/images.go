package render

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ComposeImage is one image reference a site's compose file names, as the
// configuration decides it, and the services that run it.
//
// A reference here may be a track (Eg: ghcr.io/paisans-software/caddy:2.*.*)
// or a tag that moves (an operator's series tag), and is rendered as written: render reaches no network,
// so it cannot know which build a tag names today. apply does, and pins the
// digest into every line naming the reference before it compares the file
// with the host's (see PinImages).
type ComposeImage struct {
	// Path is the compose file's path in Plan.Files, the site first.
	Path string
	// Stack is the stack the file belongs to: infra, or an app's name.
	Stack string
	// App is the app the stack runs, empty for infra.
	App string
	// Image is the reference as rendered.
	Image string
	// Services are the names the configuration knows the image by: the
	// blueprint's services naming it, which are the keys of apps.<name>.images
	// (Eg: app, for Mbin's application and its messenger consumers alike),
	// or caddy for the gateway's.
	Services []string
	// Lines is how many services of the compose file run the image, and so
	// how many `image:` lines name it.
	Lines int
	// FoundationServices are the compose services of the file, among those
	// running Image, whose image the render itself took from the official
	// core catalog's foundation.yaml for a service of the toolkit's own: one
	// of the infrastructure stack's, a service a reserved or core blueprint
	// names with foundation:, a pinned app's defaulted database, or the
	// operators heartbeat. Only that file is reviewed by the team, in the
	// official repository's pull requests, so a core read from anywhere else
	// (blueprints.CoreIsOfficial) marks nothing, and a service a third party
	// catalog's blueprint names with foundation: is not one. The mark is the
	// service's, never the reference's: another service of the same file that
	// a template points at the same reference was chosen by the template, not
	// by the render, and is reviewed.
	FoundationServices []string
	// Origin is where Image came from: the configuration key that declares
	// it (Eg: infra.images.etcd, apps.talk.images.app, acme.image), or
	// OriginFoundation or OriginBlueprint when the configuration declares
	// none.
	Origin string
	// Replaces is the reference the render takes when the configuration
	// declares none, empty when it declares none or there is none.
	Replaces string

	// setting is Setting's answer for an image the configuration key of
	// which the render knows directly: the infrastructure stack's and the
	// operators heartbeat.
	setting string
	// blueprint is the app's blueprint, for Setting.
	blueprint config.Blueprint
}

// The origins of an image no configuration key declares.
const (
	// OriginFoundation is the core catalog's foundation.yaml.
	OriginFoundation = "foundation"
	// OriginBlueprint is the image a blueprint names itself.
	OriginBlueprint = "blueprint"
)

// imageOrigin is where the render took one service's image.
type imageOrigin struct {
	image string
	// declared reports that a configuration key named image.
	declared bool
	// replaces is the reference the render takes without that key.
	replaces string
	// foundation reports that the render took image from the core
	// catalog's foundation for one of the toolkit's own services, which is
	// what marks it for the review exemption (FoundationServices).
	foundation bool
	// blueprint reports that a blueprint named image itself.
	blueprint bool
}

// origin is o's ComposeImage.Origin, setting being the key that declares it.
func (o imageOrigin) origin(setting string) string {
	switch {
	case o.declared:
		return setting
	case o.blueprint:
		return OriginBlueprint
	}
	return OriginFoundation
}

// declaredOr is the reference the configuration declares, when it does, and
// otherwise the foundation's image of that name.
func declaredOr(declared, name string) (imageOrigin, error) {
	def, err := blueprints.FoundationImage(name)
	if err != nil {
		return imageOrigin{}, err
	}
	if declared != "" {
		return imageOrigin{image: declared, declared: true, replaces: def}, nil
	}
	return imageOrigin{image: def, foundation: true}, nil
}

// Setting is the configuration key that decides Image, for a message that
// tells the operator where to pin it: acme.image for the gateway's Caddy,
// infra.images.<key> for the rest of the infrastructure stack and the
// operators heartbeat, infra.postgres.<major>.spilo for Spilo,
// apps.<name>.images.<service> for a service of an app's blueprint, and empty
// for an image no configuration key decides.
func (c ComposeImage) Setting() string {
	if c.setting != "" {
		return c.setting
	}
	if c.App == "" {
		if len(c.Services) == 1 && c.Services[0] == "caddy" {
			return "acme.image"
		}
		return ""
	}
	if len(c.Services) == 0 || !blueprints.Has(c.blueprint, c.Services[0]) {
		return ""
	}
	return fmt.Sprintf("apps.%s.images.%s", c.App, c.Services[0])
}

// listImages is Plan.Images: for each site, every image a compose file
// names, the infrastructure stack's and every app stack's. Each may be a
// track, which apply resolves to the newest build on it, so every one is
// listed for apply to pin.
//
// It reads the compose files as rendered rather than the templates' inputs,
// so the count of lines is what the file holds, which is what apply checks
// its rewrite against.
func (p *planner) listImages(files []File) (map[string][]ComposeImage, error) {
	out := map[string][]ComposeImage{}
	for _, file := range files {
		site, rel := splitSite(file.Path)
		_, stack, rest, ok := deployment.SplitRel(rel)
		if !ok || rest != "compose.yaml" {
			// A compose file anywhere but a stack's own directory would run
			// images this list never names, and so never reviewed.
			if path.Base(rel) == "compose.yaml" {
				return nil, fmt.Errorf("%s is a compose file outside any stack's own directory", file.Path)
			}
			continue
		}
		services, err := blueprints.ComposeMounts(file.Content)
		if err != nil {
			return nil, fmt.Errorf("reading the images %s names: %w", file.Path, err)
		}
		lines := map[string]int{}
		running := map[string][]string{}
		for name, service := range services {
			if image := strings.TrimSpace(service.Image); image != "" {
				lines[image]++
				running[image] = append(running[image], name)
			}
		}
		app := ""
		named := map[string][]string{}
		// chosen is each compose service whose image the render took from
		// the foundation, with that image. fromElsewhere is each reference
		// the render chose for this stack otherwise: a reference chosen both
		// ways is not the foundation's, so a declaration that happens to name
		// a foundation image is still reviewed.
		chosen, fromElsewhere := map[string]string{}, map[string]bool{}
		// origins is where the render took each service's image, and
		// settings the configuration key that decides it: keyed by compose
		// service for infra and the injected heartbeat, by blueprint service
		// for an app's.
		origins := map[string]imageOrigin{}
		settings := map[string]string{}
		if stack == config.InfraStack {
			// The infrastructure template is the toolkit's own, so each of
			// its services running the foundation's image was chosen by the
			// render. Only its own services count, so nothing else that
			// reaches this file is marked.
			infra, err := p.infraImageOrigins()
			if err != nil {
				return nil, err
			}
			// Every reference the render took from elsewhere is unmarked
			// in this file, whichever service runs it.
			for _, o := range infra {
				if !o.foundation {
					fromElsewhere[o.image] = true
				}
			}
			for name, service := range services {
				o, ok := infra[name]
				if !ok || !infraServices[name] || o.image != strings.TrimSpace(service.Image) {
					continue
				}
				origins[name], settings[name] = o, p.infraSetting(name)
			}
		} else {
			declared, isApp := p.cfg.Apps[stack]
			if !isApp {
				continue
			}
			app = stack
			appOrigins, err := p.appImageOrigins(declared)
			if err != nil {
				return nil, err
			}
			for service, o := range appOrigins {
				if !o.foundation {
					fromElsewhere[o.image] = true
				}
				// An origin labels the reference it chose, so it is kept
				// only when the file runs that reference.
				if lines[o.image] == 0 {
					continue
				}
				named[o.image] = append(named[o.image], service)
				origins[service], settings[service] = o, fmt.Sprintf("apps.%s.images.%s", stack, service)
			}
			injected, err := p.injectedImages(declared)
			if err != nil {
				return nil, err
			}
			for service, o := range injected {
				if !o.foundation {
					fromElsewhere[o.image] = true
				}
				origins[service], settings[service] = o, "infra.images."+blueprints.FoundationHeartbeat
			}
		}
		// A declared image is never the foundation's, even when it names the
		// foundation's own reference: nobody reviewed the declaration.
		for service, o := range origins {
			if o.foundation {
				chosen[service] = o.image
			} else {
				fromElsewhere[o.image] = true
			}
		}
		// Asked once the origins are known, since choosing them is what
		// reads the core catalog.
		official := blueprints.CoreIsOfficial()
		marked := map[string][]string{}
		for name, service := range services {
			image := strings.TrimSpace(service.Image)
			if official && image != "" && chosen[name] == image && !fromElsewhere[image] {
				marked[image] = append(marked[image], name)
			}
		}
		for image, n := range lines {
			// An image no service of the blueprint declares (the operators
			// heartbeat in a Pocket ID stack), and every image of the
			// infrastructure stack, is named by the compose services
			// that run it.
			names := named[image]
			if len(names) == 0 {
				names = running[image]
			}
			sort.Strings(names)
			sort.Strings(marked[image])
			// The origin is the first of names that has one for this very
			// reference, names being sorted, so it is the same on every
			// render. A service whose origin is another reference (a
			// template's hard-coded image under the name of a service the
			// configuration overrides) lends this image nothing.
			var origin imageOrigin
			var setting, from string
			for _, name := range names {
				if o, ok := origins[name]; ok && o.image == image {
					origin, setting, from = o, settings[name], name
					break
				}
			}
			ci := ComposeImage{Path: file.Path, Stack: stack, App: app, Image: image, Services: names, Lines: n, FoundationServices: marked[image], blueprint: p.cfg.Apps[app].Blueprint}
			if setting != "" {
				ci.Origin, ci.Replaces = origin.origin(setting), origin.replaces
				// Setting answers for a blueprint's own service itself; the
				// infrastructure stack's and the operators heartbeat's key is
				// the render's to say.
				if stack == config.InfraStack || !blueprints.Has(ci.blueprint, from) {
					ci.setting = setting
				}
			}
			out[site] = append(out[site], ci)
		}
	}
	for site := range out {
		list := out[site]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Stack != list[j].Stack {
				return list[i].Stack < list[j].Stack
			}
			return list[i].Image < list[j].Image
		})
	}
	return out, nil
}

// infraServices are the services the infrastructure template,
// templates/infra-compose.yaml.tmpl, writes.
var infraServices = map[string]bool{"etcd": true, "patroni": true, "haproxy": true, "garage": true, "caddy": true, "heartbeat": true}

// infraImageOrigins is infraOrigins and the gateway's Caddy: acme.image when
// it declares one, the foundation's otherwise.
func (p *planner) infraImageOrigins() (map[string]imageOrigin, error) {
	out, err := p.infraOrigins()
	if err != nil {
		return nil, err
	}
	caddy, err := blueprints.FoundationImage(blueprints.FoundationCaddy)
	if err != nil {
		return nil, err
	}
	if ref := p.cfg.ACME.Image; ref != "" {
		o := imageOrigin{image: ref, declared: true}
		if acme.Published(p.cfg.ACME.Provider) {
			o.replaces = caddy
		}
		out["caddy"] = o
		return out, nil
	}
	out["caddy"] = imageOrigin{image: caddy, foundation: true}
	return out, nil
}

// injectedImages are the services an app's stack runs that no service of
// its blueprint names, with their images: services the toolkit adds to a
// blueprint built into it. The pocket-id blueprint's operators heartbeat is
// the one, appValues.HeartbeatImage, and infra.images.heartbeat overrides it.
func (p *planner) injectedImages(app config.App) (map[string]imageOrigin, error) {
	if app.Blueprint != config.BlueprintPocketID {
		return nil, nil
	}
	o, err := p.heartbeatOrigin()
	if err != nil {
		return nil, err
	}
	return map[string]imageOrigin{operatorsHeartbeatService: o}, nil
}

// heartbeatOrigin is the heartbeat image: infra.images.heartbeat, or the
// foundation's.
func (p *planner) heartbeatOrigin() (imageOrigin, error) {
	return declaredOr(p.cfg.Infra.Images[blueprints.FoundationHeartbeat], blueprints.FoundationHeartbeat)
}

// ImageLine is the reference an `image:` line of a compose file names,
// trimmed of its indentation, and whether it is written quoted, as the
// toolkit's own templates write it (yamlquote), or bare, as a catalog's may.
// A quoted reference holding an escape is not read: no reference has one.
func ImageLine(trimmed string) (ref string, quoted, ok bool) {
	ref, ok = strings.CutPrefix(trimmed, "image: ")
	if !ok {
		return "", false, false
	}
	if inner, isQuoted := strings.CutPrefix(ref, `"`); isQuoted {
		inner, closed := strings.CutSuffix(inner, `"`)
		if !closed || strings.ContainsAny(inner, `"\$`) {
			return "", false, false
		}
		return inner, true, true
	}
	return ref, false, true
}

// PinImages returns a compose file with each image line naming a key of
// pinned naming its value instead, which is how apply puts the build it
// resolved into the file.
//
// The lines are this package's: every template writes an image as
// `image: <reference>` on a line of its own, one per service, quoted in the
// toolkit's own templates (ImageLine). Each reference
// must be named on exactly as many lines as lines gives it, the count
// render reported in Plan.Images. A different count means the template
// changed under this function, and pinning some of the services and not the
// others would run two builds of one image in one stack (Eg: Mbin's
// application and its messenger consumers), which the operator was not shown.
func PinImages(compose string, pinned map[string]string, lines map[string]int) (string, error) {
	if len(pinned) == 0 {
		return compose, nil
	}
	found := map[string]int{}
	out := strings.SplitAfter(compose, "\n")
	for i, line := range out {
		body := strings.TrimRight(line, "\n")
		trimmed := strings.TrimLeft(body, " ")
		ref, quoted, ok := ImageLine(trimmed)
		if !ok {
			continue
		}
		to, ok := pinned[ref]
		if !ok {
			continue
		}
		found[ref]++
		if quoted {
			to = `"` + to + `"`
		}
		out[i] = body[:len(body)-len(trimmed)] + "image: " + to + line[len(body):]
	}
	refs := make([]string, 0, len(pinned))
	for ref := range pinned {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if found[ref] != lines[ref] {
			return "", fmt.Errorf("the compose file names the image %s on %d lines, where %d were rendered; the template and PinImages disagree", ref, found[ref], lines[ref])
		}
	}
	return strings.Join(out, ""), nil
}

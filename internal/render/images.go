package render

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
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

	// blueprint is the app's blueprint, for Setting.
	blueprint config.Blueprint
}

// Setting is the configuration key that decides Image, for a message that
// tells the operator where to pin it: acme.image for the gateway's Caddy,
// apps.<name>.images.<service> for a service of an app's blueprint, and empty
// for an image only the core catalog's foundation decides (the rest of the
// infrastructure stack, and the operators heartbeat).
func (c ComposeImage) Setting() string {
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
		if stack == config.InfraStack {
			// The infrastructure template is the toolkit's own, so each of
			// its services running a foundation image was chosen by the
			// render. Only its own services count, so nothing else that
			// reaches this file is marked.
			fromFoundation, declared, err := p.infraImageOrigins()
			if err != nil {
				return nil, err
			}
			fromElsewhere = declared
			for name, service := range services {
				if image := strings.TrimSpace(service.Image); fromFoundation[image] && infraServices[name] {
					chosen[name] = image
				}
			}
		} else {
			declared, isApp := p.cfg.Apps[stack]
			if !isApp {
				continue
			}
			app = stack
			images, foundation, err := p.appImageOrigins(declared)
			if err != nil {
				return nil, err
			}
			for service, image := range images {
				if lines[image] > 0 {
					named[image] = append(named[image], service)
				}
				if foundation[service] {
					chosen[service] = image
				} else {
					fromElsewhere[image] = true
				}
			}
			injected, err := p.injectedImages(declared)
			if err != nil {
				return nil, err
			}
			for service, image := range injected {
				chosen[service] = image
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
			out[site] = append(out[site], ComposeImage{Path: file.Path, Stack: stack, App: app, Image: image, Services: names, Lines: n, FoundationServices: marked[image], blueprint: p.cfg.Apps[app].Blueprint})
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

// infraImageOrigins is each reference the infrastructure stack runs, split
// by where the render took it: from the core catalog's foundation, which is
// every one of its images and the gateway's Caddy by default, or from the
// configuration, which is a Caddy image acme.image declares (caddyImage).
func (p *planner) infraImageOrigins() (foundation, declared map[string]bool, err error) {
	images, err := p.infraImages()
	if err != nil {
		return nil, nil, err
	}
	foundation, declared = map[string]bool{}, map[string]bool{}
	for _, image := range []string{images.etcd, images.spilo, images.haproxy, images.garage, images.heartbeat} {
		foundation[image] = true
	}
	if ref := p.cfg.ACME.Image; ref != "" {
		declared[ref] = true
		return foundation, declared, nil
	}
	caddy, err := blueprints.FoundationImage(blueprints.FoundationCaddy)
	if err != nil {
		return nil, nil, err
	}
	foundation[caddy] = true
	return foundation, declared, nil
}

// injectedImages are the services an app's stack runs that no service of
// its blueprint names, with their images: services the toolkit adds to a
// blueprint built into it, each the foundation's. The pocket-id blueprint's
// operators heartbeat is the one, appValues.HeartbeatImage.
func (p *planner) injectedImages(app config.App) (map[string]string, error) {
	if app.Blueprint != config.BlueprintPocketID {
		return nil, nil
	}
	heartbeat, err := blueprints.FoundationImage(blueprints.FoundationHeartbeat)
	if err != nil {
		return nil, err
	}
	return map[string]string{operatorsHeartbeatService: heartbeat}, nil
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

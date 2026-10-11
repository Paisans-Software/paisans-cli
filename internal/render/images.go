package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// ComposeImage is one image reference a site's compose file names, as the
// configuration decides it, and the services that run it.
//
// A reference here may be a tag that moves (Eg: a fork's stack-1, or the
// gateway's caddy:2), and is rendered as written: render reaches no network,
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
	// kind's services naming it, which are the keys of apps.<name>.images
	// (Eg: app, for Mbin's application and its messenger consumers alike),
	// or caddy for the gateway's.
	Services []string
	// Lines is how many services of the compose file run the image, and so
	// how many `image:` lines name it.
	Lines int
}

// Setting is the configuration key that decides Image, for a message that
// tells the operator where to pin it.
func (c ComposeImage) Setting() string {
	if c.App == "" {
		return "acme.image"
	}
	service := "<service>"
	if len(c.Services) > 0 {
		service = c.Services[0]
	}
	return fmt.Sprintf("apps.%s.images.%s", c.App, service)
}

// listImages is Plan.Images: for each site, every image an app stack's
// compose file names, and the gateway's Caddy image in the infrastructure
// stack's. Those are the images whose references the configuration decides,
// by the kind's default or an apps.<name>.images entry, and by acme.image.
// The rest of the infrastructure stack (etcd, Spilo, HAProxy, Garage, the
// heartbeat) is the toolkit's own, rendered at exact versions, and is not
// listed.
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
			continue
		}
		services, err := kinds.ComposeMounts(file.Content)
		if err != nil {
			return nil, fmt.Errorf("reading the images %s names: %w", file.Path, err)
		}
		lines := map[string]int{}
		for name, service := range services {
			if image := strings.TrimSpace(service.Image); image != "" && (stack != "infra" || name == "caddy") {
				lines[image]++
			}
		}
		named := map[string][]string{}
		app := ""
		if stack == "infra" {
			if image := strings.TrimSpace(services["caddy"].Image); image != "" {
				named[image] = []string{"caddy"}
			}
		} else {
			declared, isApp := p.cfg.Apps[stack]
			if !isApp {
				continue
			}
			app = stack
			for service, image := range p.appImages(declared) {
				if lines[image] > 0 {
					named[image] = append(named[image], service)
				}
			}
		}
		for image, n := range lines {
			names := named[image]
			sort.Strings(names)
			out[site] = append(out[site], ComposeImage{Path: file.Path, Stack: stack, App: app, Image: image, Services: names, Lines: n})
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

// PinImages returns a compose file with each image line naming a key of
// pinned naming its value instead, which is how apply puts the build it
// resolved into the file.
//
// The lines are this package's: every template writes an image as
// `image: <reference>` on a line of its own, one per service. Each reference
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
		ref, ok := strings.CutPrefix(trimmed, "image: ")
		if !ok {
			continue
		}
		to, ok := pinned[ref]
		if !ok {
			continue
		}
		found[ref]++
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

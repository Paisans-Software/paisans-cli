// Package ownership reports what is on a host that apply leaves alone: this
// deployment's own leftovers, and foreign things that rely on its Caddy.
//
// A thing is this deployment's only when that is provable: a container with
// the deployment label carrying this id and a compose project named
// paisans-<token>-<stack>, or a file listed in this deployment's manifest.
// Everything else is foreign. Neither kind is changed here or by apply: a
// leftover is reported so the operator knows it is there, and a foreign thing
// relying on the gateway's Caddy is reported so whoever moves or stops the
// gateway knows what else goes with it. The Caddyfile is this deployment's
// and Caddy runs in the host's network namespace, so the one way something
// foreign is served through it is a site block in the deployment's caddy.d, and that
// directory is what counts.
//
// Classify is a pure function of the configuration, an inventory from
// internal/hostcheck and the manifest's entries, so every judgement is tested
// without a host.
package ownership

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Report is one site's leftovers and the foreign users of its Caddy.
type Report struct {
	Site string
	// Stacks are this deployment's compose projects on the host that the
	// configuration no longer renders for the site, sorted by name.
	Stacks []Stack
	// Files are the manifest's entries marked left over, sorted by path.
	Files []render.ManifestFile
	// Services are this deployment's containers in a stack the site still
	// renders, for a service that stack's compose file no longer declares,
	// sorted by stack and service. Compose leaves such a container running.
	// Classify leaves it empty; UndeclaredServices fills it from a render.
	Services []Service
	// HostSites are the *.caddy files in the deployment's render.HostSitesDir, which the
	// gateway's Caddy serves, on a gateway site only.
	HostSites []string
}

// Stack is one left over compose project.
type Stack struct {
	// Name is the stack, Project its compose project.
	Name, Project string
	// Containers are its containers by name, sorted, and Running how many
	// of them run.
	Containers []string
	Running    int
}

// Service is one left over compose service of a stack still rendered.
type Service struct {
	// Stack is the stack, Project its compose project, Name the service.
	Stack, Project, Name string
	// Containers are its containers by name, sorted, and Running how many
	// of them run.
	Containers []string
	Running    int
}

// Leftover reports whether anything of this deployment's is left over.
func (r Report) Leftover() bool {
	return len(r.Stacks) > 0 || len(r.Files) > 0 || len(r.Services) > 0
}

// Foreign reports whether anything foreign relies on this site's Caddy.
func (r Report) Foreign() bool { return len(r.HostSites) > 0 }

// Classify builds site's report from inv, the inventory of its host, and
// manifest, the entries this deployment's manifest holds for it.
func Classify(cfg *config.Config, site string, inv *hostcheck.Inventory, manifest []render.ManifestFile) (Report, error) {
	out := Report{Site: site}
	declared, ok := cfg.Sites[site]
	if !ok {
		return out, fmt.Errorf("no site %q is declared", site)
	}
	stacks, err := render.SiteStacks(cfg, site)
	if err != nil {
		return out, err
	}
	rendered := map[string]bool{}
	for _, s := range stacks {
		rendered[s] = true
	}
	d := cfg.Deployment()
	prefix := d.Prefix() + "-"

	left := map[string]*Stack{}
	for _, c := range inv.Containers {
		if c.Deployment != d.ID {
			continue
		}
		name, ok := strings.CutPrefix(c.Project, prefix)
		if !ok || name == "" {
			continue
		}
		if rendered[name] {
			continue
		}
		s := left[name]
		if s == nil {
			s = &Stack{Name: name, Project: c.Project}
			left[name] = s
		}
		s.Containers = append(s.Containers, c.Name)
		if c.PID > 0 {
			s.Running++
		}
	}
	for _, s := range left {
		sort.Strings(s.Containers)
		out.Stacks = append(out.Stacks, *s)
	}
	sort.Slice(out.Stacks, func(i, j int) bool { return out.Stacks[i].Name < out.Stacks[j].Name })

	for _, f := range manifest {
		if f.Leftover {
			out.Files = append(out.Files, f)
		}
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })

	if declared.Has(config.RoleGateway) {
		out.HostSites = append(out.HostSites, inv.HostSites...)
		sort.Strings(out.HostSites)
	}

	return out, nil
}

// UndeclaredServices is every container of d's in a stack of declared, the
// services each stack's rendered compose file declares, whose service is not
// among them. A stack absent from declared is not judged, and a container
// without a service label is not one compose started.
func UndeclaredServices(d deployment.Deployment, inv *hostcheck.Inventory, declared map[string][]string) []Service {
	prefix := d.Prefix() + "-"
	left := map[string]*Service{}
	for _, c := range inv.Containers {
		if c.Deployment != d.ID || c.Service == "" {
			continue
		}
		stack, ok := strings.CutPrefix(c.Project, prefix)
		if !ok {
			continue
		}
		services, ok := declared[stack]
		if !ok || slices.Contains(services, c.Service) {
			continue
		}
		key := stack + "/" + c.Service
		s := left[key]
		if s == nil {
			s = &Service{Stack: stack, Project: c.Project, Name: c.Service}
			left[key] = s
		}
		s.Containers = append(s.Containers, c.Name)
		if c.PID > 0 {
			s.Running++
		}
	}
	out := make([]Service, 0, len(left))
	for _, s := range left {
		sort.Strings(s.Containers)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stack != out[j].Stack {
			return out[i].Stack < out[j].Stack
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// LeftoverLines describes each leftover in one line, for a plan or a report:
// the stacks, then the files, then the services.
func (r Report) LeftoverLines() []string {
	var out []string
	for _, s := range r.Stacks {
		out = append(out, fmt.Sprintf("stack %s (compose project %s, %s): %s", s.Name, s.Project, runState(s.Running, len(s.Containers)), strings.Join(s.Containers, ", ")))
	}
	for _, f := range r.Files {
		line := "file /" + f.Path
		if f.LeftoverSince != "" {
			line += ", left over since " + f.LeftoverSince
		}
		out = append(out, line)
	}
	for _, s := range r.Services {
		out = append(out, fmt.Sprintf("service %s of stack %s (compose project %s, %s): %s", s.Name, s.Stack, s.Project, runState(s.Running, len(s.Containers)), strings.Join(s.Containers, ", ")))
	}
	return out
}

// runState says how many of n containers run.
func runState(running, n int) string {
	if running == 0 {
		return "stopped"
	}
	return fmt.Sprintf("%d of %d running", running, n)
}

// ForeignLines describes each foreign user of the site's Caddy in one line.
func (r Report) ForeignLines() []string {
	var out []string
	for _, f := range r.HostSites {
		out = append(out, "site block "+f+", served by this deployment's Caddy")
	}
	return out
}

// Apps names the apps the leftovers belong to, sorted: each left over stack
// but the infrastructure stack, and the app each left over file is under or
// routes to. These are what `paisans app remove <app>` takes. A left over
// service is not one: its app is still declared, and removing the app would
// take the services it still runs.
func (r Report) Apps() []string {
	seen := map[string]bool{}
	for _, s := range r.Stacks {
		if s.Name != "infra" {
			seen[s.Name] = true
		}
	}
	for _, f := range r.Files {
		if app := FileApp(f.Path); app != "" {
			seen[app] = true
		}
	}
	out := make([]string, 0, len(seen))
	for app := range seen {
		out = append(out, app)
	}
	sort.Strings(out)
	return out
}

// FileApp names the app a rendered path belongs to, empty for none: the
// stack directory it is under, or for a route in the infrastructure stack's
// snippets, the app the snippet is named for, without a role suffix.
func FileApp(rel string) string {
	token, stack, _, ok := deployment.SplitRel(rel)
	if !ok {
		return ""
	}
	if stack != "infra" {
		return stack
	}
	dir, file := path.Split(rel)
	if dir != render.SnippetsDir(deployment.Deployment{ID: token})+"/" {
		return ""
	}
	name, ok := strings.CutSuffix(file, ".caddy")
	if !ok {
		return ""
	}
	for _, role := range RouteSuffixes() {
		if app, ok := strings.CutSuffix(name, "-"+role); ok && app != "" {
			return app
		}
	}
	return name
}

// RouteSuffixes are what follows <app>- in the name of an app's snippet
// other than its primary route: each hostname role a blueprint understands, and
// an oauth2-proxy app's named gate snippets.
func RouteSuffixes() []string {
	return append(blueprints.ExtraRoles(), render.GatesSnippet)
}

// RouteOf reports whether a snippet name, without .caddy, is one an app's
// routes are written under: <app>, or <app>-<suffix>.
func RouteOf(app, name string) bool {
	if name == app {
		return true
	}
	suffix, ok := strings.CutPrefix(name, app+"-")
	if !ok {
		return false
	}
	for _, s := range RouteSuffixes() {
		if suffix == s {
			return true
		}
	}
	return false
}

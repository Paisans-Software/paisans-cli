package render_test

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/validate"
	"gopkg.in/yaml.v3"
)

// loopbackPattern matches every way a rendered file names the host's
// loopback: any 127/8 address, localhost, and ::1 bare or bracketed, but not
// an IPv6 address that merely ends in ::1.
var loopbackPattern = regexp.MustCompile(`\b127\.\d{1,3}\.\d{1,3}\.\d{1,3}\b|(?i:\blocalhost\b)|(^|[^0-9A-Fa-f:])::1($|[^0-9A-Fa-f:])`)

// renderedPlans is every plan the golden trees are rendered from.
func renderedPlans(t *testing.T) map[string]*render.Plan {
	t.Helper()
	plans := map[string]*render.Plan{"golden": build(t)}
	local, err := render.Build(localStorageFixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatalf("building the local storage plan: %v", err)
	}
	plans["golden-local-storage"] = local
	cfg, secrets := fixture(t), fixtureSecrets(t)
	openSignup(cfg, secrets)
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("the open signup fixture is refused: %v", refusals)
	}
	open, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatalf("building the open signup plan: %v", err)
	}
	plans["golden-open-signup"] = open
	return plans
}

// hostNetworked is one service with network_mode: host in a rendered compose
// file, with everything it reads: its own definition, its env files and every
// rendered file under a path it mounts.
type hostNetworked struct {
	where string
	def   string
	files map[string]string
}

// hostNetworkedServices finds every host-networked service in a plan, from
// the compose files themselves rather than a list of names, so a service
// added later is checked with no change here.
func hostNetworkedServices(t *testing.T, plan *render.Plan) []hostNetworked {
	t.Helper()
	files := map[string]string{}
	for _, f := range plan.Files {
		files[f.Path] = f.Content
	}
	var out []hostNetworked
	for _, f := range plan.Files {
		if path.Base(f.Path) != "compose.yaml" {
			continue
		}
		var doc struct {
			Services map[string]map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(f.Content), &doc); err != nil {
			t.Fatalf("%s does not parse: %v", f.Path, err)
		}
		site, _, _ := strings.Cut(f.Path, "/")
		dir := path.Dir(f.Path)
		for name, svc := range doc.Services {
			if svc["network_mode"] != "host" {
				continue
			}
			def, err := yaml.Marshal(svc)
			if err != nil {
				t.Fatal(err)
			}
			hn := hostNetworked{where: f.Path + " " + name, def: string(def), files: map[string]string{}}
			envFiles, _ := svc["env_file"].([]any)
			for _, e := range envFiles {
				p := path.Join(dir, e.(string))
				content, ok := files[p]
				if !ok {
					t.Errorf("%s: env file %s is not rendered", hn.where, p)
				}
				hn.files[p] = content
			}
			volumes, _ := svc["volumes"].([]any)
			for _, v := range volumes {
				source, _, _ := strings.Cut(v.(string), ":")
				if !strings.HasPrefix(source, "/") {
					continue
				}
				p := site + source
				for fp, content := range files {
					if fp == p || strings.HasPrefix(fp, p+"/") {
						hn.files[fp] = content
					}
				}
			}
			out = append(out, hn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].where < out[j].where })
	return out
}

// uncommented is content without its whole-line comments, which explain and
// neither bind nor dial.
func uncommented(content string) string {
	var keep []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// A host-networked container shares the host's network namespace, and the
// egress table host admit loads drops whatever the deployment's user and its
// subordinate range send to loopback. So nothing a host-networked service is
// given, its own definition, its env files or a file it mounts, may name
// loopback: a listener there is one the deployment cannot dial, and a dial
// there never connects. Each binds and dials the site's mesh address.
func TestHostNetworkedServicesStayOffLoopback(t *testing.T) {
	for name, plan := range renderedPlans(t) {
		services := hostNetworkedServices(t, plan)
		seen := map[string]bool{}
		for _, hn := range services {
			if m := loopbackPattern.FindString(hn.def); m != "" {
				t.Errorf("%s: %s names loopback (%q) in its definition", name, hn.where, strings.TrimSpace(m))
			}
			for p, content := range hn.files {
				seen[path.Base(p)] = true
				for _, line := range strings.Split(uncommented(content), "\n") {
					if loopbackPattern.MatchString(line) {
						t.Errorf("%s: %s reads %s, which names loopback: %s", name, hn.where, p, strings.TrimSpace(line))
					}
				}
			}
		}
		if name != "golden" {
			continue
		}
		// The guard is only as good as what it found, so the main tree must
		// have shown it each infrastructure file a host-networked service reads.
		for _, want := range []string{"patroni.env", "haproxy.cfg", "garage.toml", "Caddyfile", "caddy.env"} {
			if !seen[want] {
				t.Errorf("no host-networked service in the golden tree reads a %s, so the guard checked none", want)
			}
		}
	}
}

// Caddy serves its admin API on localhost:2019 unless told otherwise, and
// `caddy reload` dials it there. Every Caddy a host-networked service runs
// puts it on a unix socket inside its container instead.
func TestHostNetworkedCaddyAdminIsAUnixSocket(t *testing.T) {
	admin := regexp.MustCompile(`(?m)^\s*admin unix//\S+$`)
	for name, plan := range renderedPlans(t) {
		for _, hn := range hostNetworkedServices(t, plan) {
			for p, content := range hn.files {
				if path.Base(p) != "Caddyfile" {
					continue
				}
				global, _, _ := strings.Cut(content, "\n}")
				if !admin.MatchString(global) {
					t.Errorf("%s: %s has no admin unix// line in its global options block", name, p)
				}
			}
		}
	}
}

// Package grant is what a deployment's configuration grants on a host: who
// may log in to it and what code runs there. Each host records the grant this
// deployment last applied, and apply and host prepare show what would change
// from that record before they change anything, so an operator approves an
// added key or a changed image at a terminal rather than discovering it in a
// plan line (docs/specs/2026-10-11-config-review.md).
//
// The grant is computed from the configuration and the render, never from
// the host: what the host holds is what the record is compared with, and a
// value read back from the host would be one the review could not trust.
package grant

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// Grant is one site's grant.
type Grant struct {
	// Login is ssh.user, the account every key in Keys may log in as.
	Login string
	// Keys is one entry per key of every admin ssh.keys names, sorted by
	// fingerprint.
	Keys []Key
	// Images is one entry per service of every compose file the render
	// writes for the site, sorted by stack and service.
	Images []Image
	// WebServer is the directory apply writes the operator's web server's
	// files into, nil on a site with none.
	WebServer *WebServer
	// Blueprints is one entry per app on the site whose blueprint comes from
	// a catalog, sorted by app: what decides the rest of that stack, since a
	// catalog's author wrote its templates.
	Blueprints []Blueprint
}

// Blueprint is one app's catalog blueprint as the render used it: the
// blueprint, the release and commit it was read at, the SHA-256 of its files,
// and whether its catalog's tags are checked against a signer.
type Blueprint struct {
	App       string `json:"app"`
	Blueprint string `json:"blueprint"`
	Tag       string `json:"tag,omitempty"`
	Commit    string `json:"commit"`
	SHA256    string `json:"sha256"`
	Unsigned  bool   `json:"unsigned,omitempty"`
}

// Key is one key the site authorises. A key is the same key when its
// fingerprint is; its admin and comment are what the review shows.
type Key struct {
	Admin       string `json:"admin"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment,omitempty"`
}

// Image is one service of one stack and the image reference its compose file
// names, as the render writes it.
type Image struct {
	Stack     string `json:"stack"`
	Service   string `json:"service"`
	Reference string `json:"reference"`
}

// WebServer is where apply writes the files the operator's web server
// includes. It is a directory root writes into outside the deployment's own,
// which is why it is reviewed.
type WebServer struct {
	IncludeDir string `json:"include_dir"`
}

// For computes site's grant from cfg and plan, plan being the render of cfg.
// A nil plan computes the login and the keys alone, which is all host prepare
// needs.
func For(cfg *config.Config, plan *render.Plan, site string) (Grant, error) {
	declared, ok := cfg.Sites[site]
	if !ok {
		return Grant{}, fmt.Errorf("no site %q is declared", site)
	}
	g := Grant{Login: declared.SSH.User, Keys: KeysOf(declared.SSH), WebServer: WebServerOf(declared)}
	if plan != nil {
		images, err := ImagesOf(plan, site)
		if err != nil {
			return Grant{}, err
		}
		g.Images = images
		blueprints, err := BlueprintsOf(cfg, site)
		if err != nil {
			return Grant{}, err
		}
		g.Blueprints = blueprints
	}
	return g, nil
}

// BlueprintsOf is every app on site whose blueprint a catalog provides, with
// the SHA-256 of the files it was read from.
func BlueprintsOf(cfg *config.Config, site string) ([]Blueprint, error) {
	var out []Blueprint
	placed := render.AppSites(cfg)
	for _, app := range cfg.AppNames() {
		if !slices.Contains(placed[app], site) {
			continue
		}
		kind := cfg.Apps[app].Kind
		dir, fromCatalog := kinds.Source(kind)
		if !fromCatalog {
			continue
		}
		catalog, _ := config.SplitBlueprint(kind)
		if catalog == "" {
			catalog = config.CoreCatalog
		}
		cat := cfg.Catalogs[catalog]
		sum, err := blueprints.Digest(os.DirFS(dir), ".")
		if err != nil {
			return nil, fmt.Errorf("reading the blueprint of apps.%s: %w", app, err)
		}
		out = append(out, Blueprint{App: app, Blueprint: string(kind), Tag: cat.Tag, Commit: cat.Commit, SHA256: sum, Unsigned: cat.Unsigned})
	}
	return out, nil
}

// KeysOf is every key s authorises. A key validate would refuse is left
// out: a configuration with one is never applied.
func KeysOf(s config.SSH) []Key {
	keys, _ := s.AuthorizedKeys()
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		out = append(out, Key{Admin: k.Name, Fingerprint: k.Fingerprint, Comment: k.Comment})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

// ImagesOf is every service of every compose file plan writes for site, with
// the image it names. A service naming no image is left out, since it runs
// nothing a reference chose.
func ImagesOf(plan *render.Plan, site string) ([]Image, error) {
	var out []Image
	for _, file := range plan.Files {
		on, rel, ok := strings.Cut(file.Path, "/")
		if !ok || on != site {
			continue
		}
		_, stack, rest, ok := deployment.SplitRel(rel)
		if !ok || rest != "compose.yaml" {
			continue
		}
		services, err := kinds.ComposeMounts(file.Content)
		if err != nil {
			return nil, fmt.Errorf("reading the images %s names: %w", file.Path, err)
		}
		for name, service := range services {
			if ref := strings.TrimSpace(service.Image); ref != "" {
				out = append(out, Image{Stack: stack, Service: name, Reference: ref})
			}
		}
	}
	sortImages(out)
	return out, nil
}

func sortImages(list []Image) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Stack != list[j].Stack {
			return list[i].Stack < list[j].Stack
		}
		return list[i].Service < list[j].Service
	})
}

// WebServerOf is the include directory of a site whose apps the operator's
// own web server serves, when paisans-stack.yaml names one. An include_dir left
// unset is derived on the host by apply and written into paisans-stack.yaml, so
// it is reviewed from the run after that; false writes nothing there.
func WebServerOf(s config.Site) *WebServer {
	if !s.Has(config.RoleMonitor) || s.WebServerManagedBy() != config.ManagedByExternal || s.WebServer == nil || s.WebServer.IncludeDir.Path == "" {
		return nil
	}
	return &WebServer{IncludeDir: s.WebServer.IncludeDir.Path}
}

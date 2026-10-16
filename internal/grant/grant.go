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
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Grant is one site's grant.
type Grant struct {
	// Login is the login user from SSHUser(): the declared ssh.user, or the
	// deployment's own user when none is declared. Every key in Keys may log
	// in as it.
	Login string
	// Keys is one entry per key of every admin ssh.keys names, sorted by
	// fingerprint.
	Keys []Key
	// Images is one entry per service of every compose file the render
	// writes for the site, sorted by stack and service.
	Images []Image
	// Blueprints is one entry per app on the site whose blueprint comes from
	// a catalog, sorted by app: what decides the rest of that stack, since a
	// catalog's author wrote its templates.
	Blueprints []Blueprint
}

// Blueprint is one app's catalog blueprint as the render used it: the
// blueprint, the release and commit it was read at, and the SHA-256 of its
// files.
type Blueprint struct {
	App       string `json:"app"`
	Blueprint string `json:"blueprint"`
	Tag       string `json:"tag,omitempty"`
	Commit    string `json:"commit"`
	SHA256    string `json:"sha256"`
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
	// Foundation is an image the render took from the official core
	// catalog's foundation.yaml for one of the toolkit's own services, which
	// the team reviews in that repository's pull requests. It is decided by
	// where the render took the image for this service
	// (render.ComposeImage.FoundationServices), never by its name.
	Foundation bool `json:"foundation,omitempty"`
}

// For computes site's grant from cfg and plan, plan being the render of cfg.
// A nil plan computes the login and the keys alone, which is all host prepare
// needs.
func For(cfg *config.Config, plan *render.Plan, site string) (Grant, error) {
	declared, ok := cfg.Sites[site]
	if !ok {
		return Grant{}, fmt.Errorf("no site %q is declared", site)
	}
	g := Grant{Login: declared.SSHUser(), Keys: KeysOf(declared.SSH)}
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

// BlueprintsOf is every app on site whose blueprint a catalog provides, the
// core catalog included, with the SHA-256 of the files it was read from.
func BlueprintsOf(cfg *config.Config, site string) ([]Blueprint, error) {
	var out []Blueprint
	placed := render.AppSites(cfg)
	for _, app := range cfg.AppNames() {
		if !slices.Contains(placed[app], site) {
			continue
		}
		blueprint := cfg.Apps[app].Blueprint
		dir, fromCatalog := blueprints.Source(blueprint)
		if !fromCatalog {
			continue
		}
		catalog, _ := config.SplitBlueprint(blueprint)
		if catalog == "" {
			catalog = config.CoreCatalog
		}
		cat := blueprints.CorePin(cfg, catalog)
		sum, err := blueprints.Digest(os.DirFS(dir), ".")
		if err != nil {
			return nil, fmt.Errorf("reading the blueprint of apps.%s: %w", app, err)
		}
		out = append(out, Blueprint{App: app, Blueprint: string(blueprint), Tag: cat.Tag, Commit: cat.Commit, SHA256: sum})
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
// nothing a reference chose. An image is marked Foundation when the render
// says it took it from the foundation for that service of that file
// (render.ComposeImage.FoundationServices).
func ImagesOf(plan *render.Plan, site string) ([]Image, error) {
	var out []Image
	// Keyed by the compose service, never by the reference, so a service a
	// template points at a foundation image's reference is not marked.
	foundation := map[[2]string]bool{}
	for _, image := range plan.Images[site] {
		for _, service := range image.FoundationServices {
			foundation[[2]string{image.Path, service}] = true
		}
	}
	for _, file := range plan.Files {
		on, rel, ok := strings.Cut(file.Path, "/")
		if !ok || on != site {
			continue
		}
		_, stack, rest, ok := deployment.SplitRel(rel)
		if !ok || rest != "compose.yaml" {
			// A compose file anywhere but a stack's own directory would run
			// images the review never shows.
			if path.Base(rel) == "compose.yaml" {
				return nil, fmt.Errorf("%s is a compose file outside any stack's own directory", file.Path)
			}
			continue
		}
		services, err := blueprints.ComposeMounts(file.Content)
		if err != nil {
			return nil, fmt.Errorf("reading the images %s names: %w", file.Path, err)
		}
		for name, service := range services {
			if ref := strings.TrimSpace(service.Image); ref != "" {
				out = append(out, Image{Stack: stack, Service: name, Reference: ref, Foundation: foundation[[2]string{file.Path, name}]})
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

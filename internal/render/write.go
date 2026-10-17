package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestName is the record of what was rendered and what it hashed to.
const ManifestName = "manifest.json"

// Manifest lets a later `apply` tell an unchanged artifact from one somebody
// edited on the host. A rendered .env is a build artifact and nothing edits it
// in place, so a file whose hash no longer matches is a local modification and
// must be resolved rather than clobbered.
//
// It records the hash of what was rendered, not of what is on the host: apply
// compares the two.
type Manifest struct {
	// Version is this manifest's format, not the configuration's.
	Version int            `json:"version"`
	Files   []ManifestFile `json:"files"`
	// Pins are the builds the site's stacks run, one per service the
	// configuration names an image for: each app's, and the gateway's
	// Caddy, set by apply. The configuration may name an image by a tag
	// that moves (Eg: Caddy's 2), so the compose file's
	// digest is the only fact about what ran, and this keeps it beside the
	// tag it was resolved from, in a form an operator can read on the host
	// without parsing YAML.
	Pins []ManifestPin `json:"pins,omitempty"`
	// Arch is the site's architecture as the apply that wrote this manifest
	// read it (Eg: amd64), empty when it read none. A build the site runs
	// was proven on this architecture, so the next apply skips its platform
	// check while the architecture is the same (see apply's platformCheck).
	Arch string `json:"arch,omitempty"`
	// Images is every image repository the site's stacks have rendered, with
	// each reference rendered from it, accumulated over every apply: what
	// the stack uses now and what it used before. A whole apply removes the
	// images of these repositories that nothing renders or runs any more. A
	// manifest written before apply recorded it has none, and its history
	// starts with the next apply.
	Images []ManifestImage `json:"images,omitempty"`
	// WebServer are the reference files apply wrote, under the deployment
	// root's web-server directory, for the host owner's web server on a
	// monitor site with managed_by: external. apply writes them after the
	// render, from the admission record, so they are listed apart from
	// Files, with the app and hostname whose block the owner removes once
	// one is gone.
	WebServer []ManifestWebServer `json:"web_server,omitempty"`
	// Pending is the sites that were pending when these files were
	// rendered, sorted: the effective topology left them out, so a check
	// comparing this site with a render uses the same view.
	Pending []string `json:"pending,omitempty"`
	// ToolkitVersion is the release of the paisans that wrote this
	// manifest, MAJOR.MINOR.PATCH, without the commit, which cannot be
	// ordered. site remove refuses a site a newer release applied, since it
	// may not know everything that release put on the host. A manifest
	// written before apply recorded it has none.
	ToolkitVersion string `json:"toolkit_version,omitempty"`
}

// ManifestImage is one repository a site's stacks rendered, and every
// reference they rendered from it, sorted.
type ManifestImage struct {
	// Repository is as `docker image ls` shows it (Eg: postgres,
	// ghcr.io/paisans-software/caddy).
	Repository string   `json:"repository"`
	References []string `json:"references"`
}

// ManifestWebServer is one reference file apply wrote, in the deployment's
// root, for the host owner's web server: its checksum tells a rewrite apart,
// and the entry proves the file this deployment's to the command that
// removes it.
type ManifestWebServer struct {
	// App is the app the file serves, and Hostname the name the owner's
	// web server serves it at.
	App      string `json:"app"`
	Hostname string `json:"hostname,omitempty"`
	// Path is the reference file on the host, absolute, in the
	// deployment's root.
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	// Server is the web server the file is for: caddy, nginx or apache.
	Server string `json:"server"`
	// Container is the docker container the owner's server runs in, as site
	// admit found it, empty when it runs on the host itself.
	Container string `json:"container,omitempty"`
	// Config is the owner's server's configuration, as admit found it,
	// empty when it found none.
	Config string `json:"config"`
}

// ManifestPin is the build one service of a stack was deployed with, and
// how apply came by it.
type ManifestPin struct {
	// Stack is the stack: infra, or an app's name.
	Stack string `json:"stack"`
	// Service is the configuration's name for the image: a key of
	// apps.<name>.images (Eg: app), or caddy.
	Service string `json:"service"`
	// Image is the reference as the configuration names it
	// (Eg: ghcr.io/paisans-software/caddy:2.*.*).
	Image string `json:"image"`
	// Reference is what the compose file names and the host pulled: Image
	// on the tag and the digest it resolved to
	// (Eg: ghcr.io/paisans-software/caddy:2.11.7@sha256:...).
	Reference string `json:"reference"`
	// ResolvedAt is when the apply that first deployed this Reference
	// resolved it, RFC 3339. A later apply resolving the same build keeps it.
	ResolvedAt string `json:"resolved_at"`
	// Source is where the build came from: "registry" when apply asked the
	// registry, "deployed" when the registry could not be reached and apply
	// kept the build the host already ran, "held" when apply kept an app on
	// the build its other sites run, "configured" when the configuration
	// named a digest itself.
	Source string `json:"source"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
	// Leftover marks a file an apply wrote that the site no longer renders,
	// Eg: an app taken out of paisans-stack.yaml. A whole apply keeps the entry, so
	// the file stays provably this deployment's, and never deletes the file.
	// LeftoverSince is when a whole apply first found it left over, RFC 3339.
	// A file rendered again loses both.
	Leftover      bool   `json:"leftover,omitempty"`
	LeftoverSince string `json:"leftover_since,omitempty"`
}

// Write writes the plan under dir, creating directories as needed, and adds a
// manifest per site.
//
// Rendering is deterministic: the same input produces byte identical output,
// which is what makes a diff between two renders meaningful. Nothing here
// records a timestamp for that reason.
func Write(plan *Plan, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	bySite := map[string][]ManifestFile{}
	for _, file := range plan.Files {
		full := filepath.Join(dir, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(file.Content), os.FileMode(file.Mode)); err != nil {
			return fmt.Errorf("writing %s: %w", full, err)
		}
		site, rest := splitSite(file.Path)
		sum := sha256.Sum256([]byte(file.Content))
		bySite[site] = append(bySite[site], ManifestFile{
			Path:   rest,
			SHA256: hex.EncodeToString(sum[:]),
			Mode:   fmt.Sprintf("%04o", file.Mode),
		})
	}

	sites := make([]string, 0, len(bySite))
	for site := range bySite {
		sites = append(sites, site)
	}
	sort.Strings(sites)

	for _, site := range sites {
		entries := bySite[site]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		data, err := json.MarshalIndent(Manifest{Version: 1, Files: entries}, "", "  ")
		if err != nil {
			return fmt.Errorf("building the manifest for %s: %w", site, err)
		}
		data = append(data, '\n')
		path := filepath.Join(dir, site, ManifestName)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

func splitSite(path string) (site, rest string) {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", path
	}
	return parts[0], parts[1]
}

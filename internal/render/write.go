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
	// Caddy is the build of the gateway's image the site's Caddy runs, set
	// by apply on a site running Caddy and absent elsewhere. A manifest
	// written before apply recorded it has none.
	Caddy *ManifestCaddy `json:"caddy,omitempty"`
	// Images is every image repository the site's stacks have rendered, with
	// each reference rendered from it, accumulated over every apply: what
	// the stack uses now and what it used before. A whole apply removes the
	// images of these repositories that nothing renders or runs any more. A
	// manifest written before apply recorded it has none, and its history
	// starts with the next apply.
	Images []ManifestImage `json:"images,omitempty"`
	// WebServer are the files apply wrote into the include directory of the
	// operator's web server, on a monitor site with managed_by: external.
	// They are outside the deployment root, so they are listed apart from
	// Files, with what removing one needs: the server to validate and
	// reload once it is gone.
	WebServer []ManifestWebServer `json:"web_server,omitempty"`
}

// ManifestImage is one repository a site's stacks rendered, and every
// reference they rendered from it, sorted.
type ManifestImage struct {
	// Repository is as `docker image ls` shows it (Eg: postgres,
	// ghcr.io/paisans-software/caddy).
	Repository string   `json:"repository"`
	References []string `json:"references"`
}

// ManifestWebServer is one file apply wrote for the operator's web server,
// which proves it this deployment's to the command that removes it.
type ManifestWebServer struct {
	// App is the app the file serves.
	App string `json:"app"`
	// Path is the file on the host, absolute.
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	// Server is the web server that includes it: caddy, nginx or apache.
	Server string `json:"server"`
	// Container is the docker container the server runs in, empty when it
	// runs on the host itself.
	Container string `json:"container,omitempty"`
	// Config is the server's main configuration, as the server reads it:
	// inside the container, for one in a container.
	Config string `json:"config"`
}

// ManifestCaddy is which build of its image a site's Caddy was deployed
// with, and how apply came by it. The configuration names a tag that moves
// (see acme.Published), so the compose file's digest is the only fact about
// what ran, and this keeps it beside the tag it was resolved from in a form
// an operator can read on the host without parsing YAML.
type ManifestCaddy struct {
	// Image is the reference as the configuration names it
	// (Eg: ghcr.io/paisans-software/caddy:2).
	Image string `json:"image"`
	// Reference is what the compose file names and the host pulled: Image
	// with the digest it resolved to (Eg: ghcr.io/paisans-software/caddy:2@sha256:...).
	Reference string `json:"reference"`
	// ResolvedAt is when the apply that first deployed this Reference
	// resolved it, RFC 3339. A later apply resolving the same build keeps it.
	ResolvedAt string `json:"resolved_at"`
	// Source is where the digest came from: "registry" when apply asked the
	// registry, "deployed" when the registry could not be reached and apply
	// kept the build the host already ran, "configured" when acme.image named
	// a digest itself.
	Source string `json:"source"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
	// Leftover marks a file an apply wrote that the site no longer renders,
	// Eg: an app taken out of paisans.yaml. A whole apply keeps the entry, so
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

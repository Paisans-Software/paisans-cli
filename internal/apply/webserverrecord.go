package apply

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// ReadWebServerRecord is what the site's manifest records of the files apply
// wrote for the operator's web server, nil when it has no manifest.
func ReadWebServerRecord(t Transport, d deployment.Deployment) ([]render.ManifestWebServer, error) {
	content, found, err := t.ReadFile(manifestPath(d))
	if err != nil || !found {
		return nil, err
	}
	m, err := render.ParseManifest([]byte(content))
	if err != nil {
		return nil, manifestRefused(d, err)
	}
	return m.WebServer, nil
}

// manifestRefused is ParseManifest's refusal of the site's manifest, with
// what to do about it: apply the site again for one that does not say which
// build of paisans wrote it, which writes it anew; delete one that is not a
// manifest at all, so every file on the host reads as somebody else's.
func manifestRefused(d deployment.Deployment, err error) error {
	var noBuild *version.NoBuildError
	if errors.As(err, &noBuild) {
		return fmt.Errorf("%s: %w. Apply the site again, which writes it anew", manifestPath(d), err)
	}
	if version.IsNewer(err) {
		return fmt.Errorf("%s: %w", manifestPath(d), err)
	}
	return fmt.Errorf("%s is not readable as a manifest: %w\nIt records what the last apply wrote. Delete it to treat every file on this host as somebody else's, which is the safe reading", manifestPath(d), err)
}

// WriteWebServerRecord replaces what the site's manifest records of the files
// written for the operator's web server with entries, keeping every other
// part of it as it was. The manifest is read again just before it is
// written, and written the way apply writes it: whole, 0600.
func WriteWebServerRecord(t Transport, d deployment.Deployment, entries []render.ManifestWebServer) error {
	content, found, err := t.ReadFile(manifestPath(d))
	if err != nil {
		return err
	}
	m := render.Manifest{Version: 1, Build: version.Current()}
	if found {
		if m, err = render.ParseManifest([]byte(content)); err != nil {
			return fmt.Errorf("%w, so it was not rewritten", manifestRefused(d, err))
		}
	}
	m.WebServer = append([]render.ManifestWebServer(nil), entries...)
	sort.Slice(m.WebServer, func(i, j int) bool { return m.WebServer[i].Path < m.WebServer[j].Path })
	if len(m.WebServer) == 0 {
		m.WebServer = nil
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if found && string(data)+"\n" == content {
		return nil
	}
	return t.WriteFile(manifestPath(d), string(data)+"\n", 0o600)
}

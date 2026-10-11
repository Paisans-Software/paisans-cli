package apply

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// ReadWebServerRecord is what the site's manifest records of the files apply
// wrote for the operator's web server, nil when it has no manifest.
func ReadWebServerRecord(t Transport, d deployment.Deployment) ([]render.ManifestWebServer, error) {
	content, found, err := t.ReadFile(manifestPath(d))
	if err != nil || !found {
		return nil, err
	}
	var m render.Manifest
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("%s is not readable as a manifest: %w\nIt records what the last apply wrote. Delete it to treat every file on this host as somebody else's, which is the safe reading", manifestPath(d), err)
	}
	return m.WebServer, nil
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
	m := render.Manifest{Version: 1}
	if found {
		if err := json.Unmarshal([]byte(content), &m); err != nil {
			return fmt.Errorf("%s is not readable as a manifest, so it was not rewritten: %w", manifestPath(d), err)
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

package apply

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// ReadIngressRecord is what the site's manifest records of the files apply
// wrote for the operator's web server, nil when it has no manifest.
func ReadIngressRecord(t Transport, d deployment.Deployment) ([]render.ManifestIngress, error) {
	content, found, err := t.ReadFile(manifestPath(d))
	if err != nil || !found {
		return nil, err
	}
	var m render.Manifest
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("%s is not readable as a manifest: %w\nIt records what the last apply wrote. Delete it to treat every file on this host as somebody else's, which is the safe reading", manifestPath(d), err)
	}
	return m.Ingress, nil
}

// WriteIngressRecord replaces what the site's manifest records of the files
// written for the operator's web server with entries, keeping every other
// part of it as it was. The manifest is read again just before it is
// written, and written the way apply writes it: whole, 0600.
func WriteIngressRecord(t Transport, d deployment.Deployment, entries []render.ManifestIngress) error {
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
	m.Ingress = append([]render.ManifestIngress(nil), entries...)
	sort.Slice(m.Ingress, func(i, j int) bool { return m.Ingress[i].Path < m.Ingress[j].Path })
	if len(m.Ingress) == 0 {
		m.Ingress = nil
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

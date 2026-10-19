package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// convergeWebServerFiles writes each external app's reference file in the
// deployment's root, before the web server check. Tests not about it
// replace it.
var convergeWebServerFiles = writeWebServerFiles

// webServerFiles is what writing the reference files came to, for the web
// server check after it and for how the run ends.
type webServerFiles struct {
	// headed is whether the web server section is open.
	headed bool
	// apps is each app's reference file, by app.
	apps map[string]*siteFile
	// failed are the steps that failed outright.
	failed []string
}

// siteFile is what the reference step came to for one app.
type siteFile struct {
	// until is set when the step ended on a failure with instructions of
	// its own: what the app's check waits on.
	until string
	// ws is the owner's web server as admit recorded it; path the app's
	// reference file, or files, joined; changed whether this run rewrote
	// it, so the owner's copy is older than it.
	ws      webserver.WebServer
	path    string
	changed bool
}

// writeWebServerFiles writes, on each monitor site with managed_by:
// external, each external app's reference snippet into the deployment's
// root, filled from the web server site admit recorded, removes the ones no
// longer wanted, and records them in the manifest with their checksums, so
// a rewrite is told apart. Nothing of the owner's is read or written: the
// owner installs the snippet. What cannot be read or written on the host is
// a failure. Only the sites scope covers are written.
func writeWebServerFiles(r ui.Reporter, cfg *config.Config, configPath string, scope webServerScope) webServerFiles {
	out := webServerFiles{apps: map[string]*siteFile{}}
	d := cfg.Deployment()
	head := func() {
		if !out.headed {
			r.Section("web server")
			out.headed = true
		}
	}
	for _, site := range cfg.MonitorSites() {
		s := cfg.Sites[site]
		if !scope.covers(site) || s.WebServerManagedBy() != config.ManagedByExternal || s.WebServer == nil {
			continue
		}
		var apps []string
		for _, app := range externalApps(cfg) {
			if t, err := webserver.For(cfg, app); err == nil && t.Site == site {
				apps = append(apps, app)
			}
		}
		t := scope.recordHost(cfg, site)
		fail := func(err error) {
			head()
			title := "reference files on " + site
			r.Step(title).Fail(err)
			r.Explain(errLine(err))
			out.failed = append(out.failed, title)
			for _, app := range apps {
				out.apps[app] = &siteFile{until: "its reference file is written"}
			}
		}
		recorded, err := apply.ReadWebServerRecord(t, d)
		if err != nil {
			fail(err)
			continue
		}
		if len(apps) == 0 && len(recorded) == 0 {
			continue
		}
		var ws webserver.WebServer
		if len(apps) > 0 {
			rec, err := admission.Read(t, d.Token())
			if err != nil {
				fail(admissionProblem(site, d.Token(), err))
				continue
			}
			ws = webserver.FromRecord(rec.WebServer)
		}
		head()
		var keep []render.ManifestWebServer
		wanted, failedApps := map[string]bool{}, map[string]bool{}
		for _, app := range apps {
			title := "reference file for " + app
			step := r.Step(title)
			sf := &siteFile{ws: ws, until: "its reference file is written"}
			out.apps[app] = sf
			failed := func(err error) {
				step.Fail(err)
				r.Explain(errLine(err))
				out.failed = append(out.failed, title)
				failedApps[app] = true
			}
			target, err := webserver.For(cfg, app)
			if err != nil {
				failed(err)
				continue
			}
			if res, err := t.Run("install -d -m 755 " + shellQuote(webserver.ReferenceDir(d))); err != nil {
				failed(fmt.Errorf("creating %s: %w: %s", webserver.ReferenceDir(d), err, strings.TrimSpace(res)))
				continue
			}
			var paths []string
			var werr error
			for _, ref := range webserver.References(d, target, ws) {
				wanted[ref.Path] = true
				entry := render.ManifestWebServer{App: app, Hostname: target.Hostname, Path: ref.Path, SHA256: sha(ref.Content), Server: string(ref.Server), Container: ws.Container, Config: ws.Config}
				// The file itself is compared, not only its entry: one
				// deleted or edited on the host is written again. A changed
				// snippet is a rewrite the owner's copy lacks.
				prev, had := recordedAt(recorded, ref.Path)
				onHost, found, err := t.ReadFile(ref.Path)
				if err != nil {
					werr = fmt.Errorf("reading %s: %w", ref.Path, err)
					break
				}
				if !found || onHost != ref.Content {
					if err := t.WriteFile(ref.Path, ref.Content, 0o644); err != nil {
						werr = fmt.Errorf("writing %s: %w", ref.Path, err)
						break
					}
					step.Detail("wrote %s", ref.Path)
				}
				sf.changed = sf.changed || (had && prev.SHA256 != entry.SHA256)
				paths = append(paths, ref.Path)
				keep = append(keep, entry)
			}
			if werr != nil {
				failed(werr)
				continue
			}
			sf.path, sf.until = strings.Join(paths, ", "), ""
			step.Done(sf.path)
		}
		// What is recorded and no longer wanted goes, but for an app whose
		// step failed, which keeps what it had.
		for _, e := range recorded {
			if _, ok := recordedAt(keep, e.Path); ok {
				continue
			}
			if !webserver.OwnReference(d, e.Path) {
				// The manifest is the deployment user's own file: an entry
				// outside the reference directory is never deleted.
				r.Warn("not a reference file of this deployment's: "+e.Path, fmt.Sprintf("The manifest on %s lists it among the reference files, but it is not in %s, so apply neither read nor deleted it. Remove the entry from the manifest by hand once you know whose it is.", site, webserver.ReferenceDir(d)))
				keep = append(keep, e)
				continue
			}
			if wanted[e.Path] || failedApps[e.App] {
				keep = append(keep, e)
				continue
			}
			step := r.Step("remove reference file " + e.Path)
			if res, err := t.Run("rm -f -- " + shellQuote(e.Path)); err != nil {
				step.Fail(fmt.Errorf("%w: %s", err, strings.TrimSpace(res)))
				out.failed = append(out.failed, "reference files on "+site)
				keep = append(keep, e)
				continue
			}
			step.Done("the host's owner removes the block for " + hostnameOr(e) + " from their web server")
		}
		if err := apply.WriteWebServerRecord(t, d, keep); err != nil {
			title := "reference files on " + site
			r.Step(title).Fail(err)
			r.Explain(errLine(err))
			out.failed = append(out.failed, title)
		}
	}
	return out
}

// webServerScope is which monitor sites the web server step covers: every
// one, or with site set that one alone, as deployment apply --site applies it, reached
// through ssh, verbatim, in place of its ssh section when that is set, as
// deployment apply --site --ssh reaches it.
//
// down is the sites a run has down: the step never reaches them, since a
// down site's own steps are skipped (see sites.go).
type webServerScope struct {
	site, ssh string
	down      []string
	// degraded is a run without some declared site: it is not a complete
	// deployment, so a warning says "Applied, but", not "Deployment
	// complete, but".
	degraded bool
}

// covers reports whether the step covers site.
func (s webServerScope) covers(site string) bool {
	return (s.site == "" || s.site == site) && !slices.Contains(s.down, site)
}

// recordHost is how the step reaches site to read its admission record and
// to write its reference files and their record.
func (s webServerScope) recordHost(cfg *config.Config, site string) apply.Transport {
	if s.ssh != "" {
		return siteTransport(site, cfg.Sites[site], s.ssh)
	}
	return convergeHost(site, cfg.Sites[site])
}

// recordedAt is the entry for path in list.
func recordedAt(list []render.ManifestWebServer, path string) (render.ManifestWebServer, bool) {
	for _, e := range list {
		if e.Path == path {
			return e, true
		}
	}
	return render.ManifestWebServer{}, false
}

// hostnameOr is the hostname an entry was written for, or its app's name
// for an entry recorded without one.
func hostnameOr(e render.ManifestWebServer) string {
	if e.Hostname != "" {
		return e.Hostname
	}
	return "the app " + e.App
}

// sha is the hex SHA-256 of s, as the manifest records it.
func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

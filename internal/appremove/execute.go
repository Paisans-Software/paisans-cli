package appremove

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/garage"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// composeProjectLabel is the label Compose puts on every container, network
// and volume of a project.
const composeProjectLabel = "com.docker.compose.project"

// Region is the S3 region every rendered Garage answers to
// (templates/garage.toml.tmpl, s3_region).
const Region = "garage"

// ContainersCommand stops and removes the project's containers and then its
// networks, selecting both by this deployment's label and the project's
// label together (Docker ANDs repeated label filters), so an object without
// this deployment's label is never selected, whatever its name. With
// deleteData the containers' anonymous volumes go with them (`docker rm
// -v`); named volumes are VolumesCommand's. It is a no-op once both are gone.
func ContainersCommand(d deployment.Deployment, project string, deleteData bool) string {
	filters := fmt.Sprintf("--filter %s --filter label=%s=%s", quote(d.LabelFilter()), composeProjectLabel, quote(project))
	rm := "docker rm"
	if deleteData {
		rm += " -v"
	}
	return fmt.Sprintf(`set -e; ids=$(docker ps -aq --no-trunc %[1]s); if [ -n "$ids" ]; then docker stop $ids >/dev/null; %[2]s $ids >/dev/null; fi; nets=$(docker network ls -q --no-trunc %[1]s); if [ -n "$nets" ]; then docker network rm $nets >/dev/null; fi`, filters, rm)
}

// AgentCommand stops and removes the infrastructure stack's Beszel agent,
// selected by this deployment's label, the infra project's and the
// service's together, so no other service of the stack is touched. It runs
// on the host's network, so it has no network of its own to remove. It is a
// no-op once the agent is gone.
func AgentCommand(d deployment.Deployment) string {
	filters := fmt.Sprintf("--filter %s --filter label=%s=%s --filter label=%s=%s", quote(d.LabelFilter()), composeProjectLabel, quote(d.Project(infraStack)), composeServiceLabel, quote(agentService))
	return fmt.Sprintf(`set -e; ids=$(docker ps -aq --no-trunc %s); if [ -n "$ids" ]; then docker stop $ids >/dev/null; docker rm $ids >/dev/null; fi`, filters)
}

// composeServiceLabel is the label Compose puts on every container of a
// service.
const composeServiceLabel = "com.docker.compose.service"

// VolumesCommand removes the project's named volumes that carry this
// deployment's label.
func VolumesCommand(d deployment.Deployment, project string) string {
	return fmt.Sprintf(`set -e; vols=$(docker volume ls -q %s --filter label=%s=%s); if [ -n "$vols" ]; then docker volume rm $vols >/dev/null; fi`, "--filter "+quote(d.LabelFilter()), composeProjectLabel, quote(project))
}

// FilesCommand removes each file whose content still hashes to what the
// manifest recorded, and prints one line per file: `removed`, `kept` (its
// content differs, so somebody edited it) or `gone`, then the path. The
// hash is checked on the host at the moment of removal. The path is used
// as it is: a directory above the file that is a symbolic link is
// followed, so the file removed is one with the content apply wrote,
// wherever the link leads.
func FilesCommand(files []File) string {
	var b strings.Builder
	for _, f := range files {
		p := "/" + f.Entry.Path
		fmt.Fprintf(&b, `f=%s; if [ ! -e "$f" ] && [ ! -L "$f" ]; then echo "gone $f"; elif [ -f "$f" ] && [ "$(sha256sum < "$f" | cut -d' ' -f1)" = %s ]; then rm -f -- "$f"; echo "removed $f"; else echo "kept $f"; fi`+"\n", quote(p), quote(f.Entry.SHA256))
	}
	return b.String()
}

// DirCommandFor empties what can go of the stack directory. Without
// deleteData it removes only the directories files were rendered in, each
// once it is empty, and then the stack directory if it is empty, so
// whatever the app wrote stays, the empty directories inside its data too:
// a database's data directory holds empty directories it needs. With it,
// the whole directory goes.
//
// Both run under rootlesskit, for the reason DirCommand does: a file or
// directory a container's non-root user wrote is the namespace root's to
// remove, and not the user's (Docker's rootless documentation, Uninstall:
// rootlesskit rm -rf ~/.local/share/docker). dir is always d.Dir(app), and
// app has passed nameShape, so it is one segment under the deployment's
// root. A find that fails fails the command. rmdir runs as the user: the stack directory is the deployment
// root's entry, and the user owns the root.
func DirCommandFor(dir string, files []File, deleteData bool) string {
	if deleteData {
		return "rootlesskit rm -rf -- " + quote(dir)
	}
	empty := ""
	if dirs := RenderedDirs(dir, files); len(dirs) > 0 {
		empty = EmptyDirsCommand(dir, dirs) + " && "
	}
	return fmt.Sprintf(`if [ -d %[1]s ]; then %[2]s{ rmdir %[1]s 2>/dev/null || true; }; fi`, quote(dir), empty)
}

// RenderedDirs is each directory below top that holds one of files, and
// each between it and top, deepest first: the directories apply made to
// write them. top is left out. A directory a container mounts and writes
// holds no rendered file, so it is never one of them.
func RenderedDirs(top string, files []File) []string {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = "/" + f.Entry.Path
	}
	return DirsOf(top, paths)
}

// DirsOf is each directory below top that holds one of paths, and each
// between it and top, deepest first. top is left out.
func DirsOf(top string, paths []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, p := range paths {
		for dir := path.Dir(p); strings.HasPrefix(dir, top+"/") && !seen[dir]; dir = path.Dir(dir) {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		if a, b := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/"); a != b {
			return a > b
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}

// EmptyDirsCommand removes each of dirs, all below top, that is empty, in
// order, so a directory emptied by removing the one before it goes too, and
// leaves one that holds anything, with all it holds. find examines each
// directory itself and never descends into it, and a directory underTop
// skips is left alone. It runs under rootlesskit, as DirCommandFor's does,
// from the directory's parent, which rootlesskit keeps as its working
// directory (observed with rootlesskit 3.3.0: cd X && rootlesskit pwd
// prints X), in a subshell whose status is the first find's that failed.
func EmptyDirsCommand(top string, dirs []string) string {
	return underTop(top, dirs, `rootlesskit find "./$n" -maxdepth 0 -type d -empty -delete`)
}

// EmptyDirsProbe names each of dirs, all below top, that is empty and that
// EmptyDirsCommand would remove, so a plan never lists a directory the
// command would leave.
func EmptyDirsProbe(top string, dirs []string) string {
	return underTop(top, dirs, `if [ ! -L "./$n" ] && [ -z "$(ls -A -- "./$n")" ]; then echo "$d"; fi`)
}

// underTop runs body for each of dirs, all below top, in a subshell in the
// directory's parent, with d the directory and n its name. A directory
// with a symbolic link anywhere from top down to it, top included, is
// skipped: the link would take body out of top, Eg: a container that
// replaced a directory it mounts with a link to one of the user's own. The
// shell enters the parent with cd -P and compares where it is with where
// the parent should be, and body takes the directory by its name there, so
// a link put in place above it after the check changes nothing.
func underTop(top string, dirs []string, body string) string {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = quote(d)
	}
	return fmt.Sprintf(`(t=%s; T=$(cd -P -- "${t%%/*}" 2>/dev/null && pwd -P) || exit 0; T=$T/${t##*/}; for d in %s; do p=${d%%/*}; n=${d##*/}; ( cd -P -- "$p" 2>/dev/null && [ "$(pwd -P)" = "$T${p#"$t"}" ] && [ -d "./$n" ] || exit 0; %s ) || exit 1; done)`, quote(top), strings.Join(quoted, " "), body)
}

// Executor runs a plan.
type Executor struct {
	// Hosts reaches each site by name.
	Hosts map[string]Host
	// Clients is Pocket ID's API at the plan's client site, nil when the
	// plan deletes no client.
	Clients Clients
	// Report receives a section per place the removal acts and a step per
	// thing it does there. Nil discards it.
	Report ui.Reporter
	// Kept collects what a step found had to stay, for the report.
	Kept []string

	// open is the step being done, ended when the next starts or the run does.
	open ui.Step
}

func (e *Executor) reporter() ui.Reporter {
	if e.Report == nil {
		return ui.Discard
	}
	return e.Report
}

// work starts the step now being done and ends the one before it, so a
// failure marks the step that failed.
func (e *Executor) work(title string) ui.Step {
	e.idle()
	e.open = e.reporter().Step(title)
	return e.open
}

// idle ends the open step as done.
func (e *Executor) idle() {
	if e.open != nil {
		e.open.Done("")
		e.open = nil
	}
}

// section starts a block of steps, after the step before it has ended.
func (e *Executor) section(format string, args ...any) {
	e.idle()
	e.reporter().Section(fmt.Sprintf(format, args...))
}

// Execute runs every step in order: on each site its containers and
// networks, its files and their manifest entries, its directory and, with
// --delete-data, its named volumes; then on each site a Beszel agent left
// with no hub (AgentFile); then the client at Pocket ID; then,
// with --delete-data, the database and the Garage buckets and keys; then
// each monitor's reseed, last, so that a failure there stops nothing of the
// removal and names the apply that finishes it. Each step reads live state
// and is a no-op once done, and the first failure stops the run, so running
// it again resumes.
func (e *Executor) Execute(p *Plan) error {
	err := e.execute(p)
	if err != nil && e.open != nil {
		e.open.Fail(err)
		e.open = nil
	}
	e.idle()
	return err
}

func (e *Executor) execute(p *Plan) error {
	ctx := context.Background()
	d := p.Deployment
	for _, s := range p.Sites {
		if s.appEmpty() {
			continue
		}
		h := e.Hosts[s.Site]
		if h == nil {
			return fmt.Errorf("%s: no way to reach the host", s.Site)
		}
		e.section("%s", s.Site)
		e.work("remove containers and networks").Detail("containers and networks of %s", s.Project)
		if out, err := h.Run(ContainersCommand(d, s.Project, p.DeleteData)); err != nil {
			return fmt.Errorf("%s: stopping and removing %s: %w: %s", s.Site, s.Project, err, firstLine(out))
		}
		if err := e.webServer(h, d, s); err != nil {
			return fmt.Errorf("%s: %w", s.Site, err)
		}
		drop, err := e.files(h, s.Site, s.Files)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Site, err)
		}
		dir := d.Dir(p.App)
		deleteDir := p.DeleteData && !s.edited(dir)
		if deleteDir {
			e.work("delete " + dir)
		} else {
			e.work("remove empty directories")
		}
		if out, err := h.Run(DirCommandFor(dir, s.Files, deleteDir)); err != nil {
			return fmt.Errorf("%s: %s: %w: %s", s.Site, dir, err, firstLine(out))
		}
		// After the directories: the entries name them, so a run that
		// stops before here finds them again.
		if err := dropEntries(h, d, drop); err != nil {
			return fmt.Errorf("%s: %w", s.Site, err)
		}
		if p.DeleteData && !deleteDir {
			e.Kept = append(e.Kept, dirEdited(s.Site, dir))
		}
		if dd, err := probeDir(h, dir); err != nil {
			return fmt.Errorf("%s: %w", s.Site, err)
		} else if dd.Files > 0 {
			// Empty directories alone are kept data too, which dataKept
			// names.
			e.Kept = append(e.Kept, dirData(s.Site, dir, dd.Files, size(dd.Bytes)))
		}
		if p.DeleteData {
			e.work("delete volumes").Detail("volumes %s", strings.Join(s.Volumes, ", "))
			if out, err := h.Run(VolumesCommand(d, s.Project)); err != nil {
				return fmt.Errorf("%s: removing %s's volumes: %w: %s", s.Site, s.Project, err, firstLine(out))
			}
		}
	}
	for _, s := range p.Sites {
		if err := e.agent(d, s); err != nil {
			return fmt.Errorf("%s: %w", s.Site, err)
		}
	}
	if c := p.Client.Delete; c != nil {
		e.section("Pocket ID (%s)", p.Client.Provider)
		e.work("delete client "+c.Name).Detail("client %s (id %s) at %s", c.Name, c.ID, p.Client.Provider)
		if e.Clients == nil {
			return fmt.Errorf("no way to reach Pocket ID to delete client %s", c.ID)
		}
		if err := e.Clients.DeleteOIDCClient(ctx, c.ID); err != nil {
			return fmt.Errorf("deleting client %s (id %s) at %s: %w", c.Name, c.ID, p.Client.Provider, err)
		}
	}
	if db := p.Database; db != nil && (db.DropDatabase || db.DropRole) {
		e.section("Postgres (the leader, %s)", db.Leader)
		if err := e.database(d, db); err != nil {
			return err
		}
	}
	if st := p.Storage; st != nil && (len(st.Buckets) > 0 || len(st.Keys) > 0) {
		e.section("Garage (on %s)", st.Anchor)
		if err := e.storage(d, st); err != nil {
			return err
		}
	}
	// The monitor's reseed is not a step of the removal above, so the last of
	// those ends first: a failure here must not mark an unrelated line.
	e.idle()
	for _, m := range p.Monitors {
		if m.Pending() {
			e.section("%s (the monitor)", m.Site)
			e.work("apply "+m.App+" on "+m.Site).Detail("%s on %s, on the seed rendered without %s", m.App, m.Site, p.App)
		}
		if err := m.Execute(); err != nil {
			return &MonitorError{Err: err, Monitors: p.Monitors}
		}
		if err := m.Gate(); err != nil {
			return &MonitorError{Err: err, Monitors: p.Monitors}
		}
	}
	return nil
}

// MonitorError is a monitor's reseed failing, after everything of the app's
// is gone. It names the apply that does the same, since the app itself is
// removed and a run of this command again finds nothing but the monitor.
type MonitorError struct {
	Err      error
	Monitors []*apply.MonitorReseed
}

func (e *MonitorError) Error() string {
	return fmt.Sprintf("reseeding the monitor: %v\n%s", e.Err, e.next())
}

// next is what to do about it.
func (e *MonitorError) next() string {
	return fmt.Sprintf("The app is removed, and only the monitor is out of date. Fix the cause and run %s, which does the same as this step", apply.MonitorCommands(e.Monitors))
}

// Unwrap is the reseed's error, or, when that is a Problem, Eg: the health
// gate's, the Problem with next added, so the default output says what to do
// and its cause, the log tail, waits for --verbose.
func (e *MonitorError) Unwrap() error {
	if p := ui.Around("", e.Err, e.next()); p != nil {
		return p
	}
	return e.Err
}

// agent stops and removes a site's hubless Beszel agent, then its unedited
// files, the directories apply made for them once empty, and their manifest
// entries, in that order, so a run that stops part way finds what is left
// again.
func (e *Executor) agent(d deployment.Deployment, s SitePlan) error {
	if !s.agent() {
		return nil
	}
	h := e.Hosts[s.Site]
	if h == nil {
		return fmt.Errorf("no way to reach the host")
	}
	e.section("%s (the Beszel agent)", s.Site)
	e.work("stop the Beszel agent").Detail("%s, whose hub is removed", agentService)
	if out, err := h.Run(AgentCommand(d)); err != nil {
		return fmt.Errorf("stopping and removing the Beszel agent: %w: %s", err, firstLine(out))
	}
	drop, err := e.files(h, s.Site, s.AgentFiles)
	if err != nil {
		return err
	}
	infra := d.Dir(infraStack)
	if dirs := RenderedDirs(infra, s.AgentFiles); len(dirs) > 0 {
		e.work("remove empty directories")
		if out, err := h.Run(EmptyDirsCommand(infra, dirs)); err != nil {
			return fmt.Errorf("%s: %w: %s", infra, err, firstLine(out))
		}
	}
	return dropEntries(h, d, drop)
}

// files removes the unedited files of one site's plan, and answers the
// entries of every file removed or already gone, for dropEntries.
func (e *Executor) files(h Host, site string, files []File) (map[string]bool, error) {
	if len(files) == 0 {
		return nil, nil
	}
	e.work("remove files")
	out, err := h.Run(FilesCommand(files))
	if err != nil {
		return nil, fmt.Errorf("removing files: %w: %s", err, firstLine(out))
	}
	drop := map[string]bool{}
	answered := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		verb, p, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		rel := strings.TrimPrefix(p, "/")
		answered[rel] = true
		switch verb {
		case "removed", "gone":
			drop[rel] = true
			if verb == "removed" {
				e.open.Detail("%s", p)
			}
		case "kept":
			e.Kept = append(e.Kept, fileEdited(site, p))
		}
	}
	for _, f := range files {
		if !answered[f.Entry.Path] {
			return nil, fmt.Errorf("removing files: no answer for /%s, so the manifest is left as it was", f.Entry.Path)
		}
	}
	return drop, nil
}

// dropEntries drops the manifest entries drop names, keeping every other
// entry. The manifest is read again just before it is written, and written
// the way apply writes it: whole, through a temporary file moved into
// place, 0600.
func dropEntries(h Host, d deployment.Deployment, drop map[string]bool) error {
	if len(drop) == 0 {
		return nil
	}
	content, found, err := h.ReadFile(d.Manifest())
	if err != nil {
		return fmt.Errorf("reading %s: %w", d.Manifest(), err)
	}
	if !found {
		return nil
	}
	m, err := render.ParseManifest([]byte(content))
	if err != nil {
		return fmt.Errorf("%s is not readable as a manifest, so it was not rewritten: %w", d.Manifest(), err)
	}
	kept := m.Files[:0]
	for _, entry := range m.Files {
		if !drop[entry.Path] {
			kept = append(kept, entry)
		}
	}
	m.Files = kept
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := h.WriteFile(d.Manifest(), string(data)+"\n", 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", d.Manifest(), err)
	}
	return nil
}

// webServer deletes the app's reference files, which apply wrote in the
// deployment's root for the host's owner, and drops their entries from the
// manifest. The owner's web server is theirs: nothing here reads, edits or
// reloads it, and what is left names the block the owner removes from it.
func (e *Executor) webServer(h Host, d deployment.Deployment, s SitePlan) error {
	if len(s.WebServer) == 0 {
		return nil
	}
	drop := map[string]bool{}
	var removed []render.ManifestWebServer
	for _, entry := range s.WebServer {
		if !webserver.OwnReference(d, entry.Path) {
			e.Kept = append(e.Kept, foreignReference(s.Site, entry.Path))
			continue
		}
		removed = append(removed, entry)
		e.work("remove reference files").Detail("%s", entry.Path)
		if out, err := h.Run("rm -f -- " + quote(entry.Path)); err != nil {
			return fmt.Errorf("%s: removing %s: %w: %s", s.Site, entry.Path, err, strings.TrimSpace(out))
		}
		drop[entry.Path] = true
	}
	if len(removed) == 0 {
		return nil
	}
	e.Kept = append(e.Kept, OwnerBlocks(s.Site, removed)...)
	recorded, err := apply.ReadWebServerRecord(h, d)
	if err != nil {
		return err
	}
	var keep []render.ManifestWebServer
	for _, entry := range recorded {
		if !drop[entry.Path] {
			keep = append(keep, entry)
		}
	}
	if err := apply.WriteWebServerRecord(h, d, keep); err != nil {
		return fmt.Errorf("writing %s: %w", d.Manifest(), err)
	}
	return nil
}

// DropSQL drops the database and then the role. DROP DATABASE ... WITH
// (FORCE) ends the sessions still connected to it (PostgreSQL 13 and later,
// "DROP DATABASE"), which a stopped app leaves none of, but a replica of
// the app on another site might. Both are IF EXISTS, so a re-run is a no-op.
func DropSQL(db *DatabasePlan) string {
	var b strings.Builder
	if db.DropDatabase {
		fmt.Fprintf(&b, "DROP DATABASE IF EXISTS %s WITH (FORCE);\n", quoteIdent(db.Name))
	}
	if db.DropRole {
		fmt.Fprintf(&b, "DROP ROLE IF EXISTS %s;\n", quoteIdent(db.Name))
	}
	return b.String()
}

func (e *Executor) database(d deployment.Deployment, db *DatabasePlan) error {
	h := e.Hosts[db.Leader]
	if h == nil {
		return fmt.Errorf("%s: no way to reach Patroni's leader", db.Leader)
	}
	e.work("drop database "+db.Name).Detail("database and role %s", db.Name)
	if out, err := h.RunInput(psql(d), DropSQL(db)); err != nil {
		return fmt.Errorf("%s: dropping %s: %w: %s", db.Leader, db.Name, err, strings.TrimSpace(out))
	}
	return nil
}

// maxPages bounds how many listings emptying one bucket may take, at a
// thousand objects a page.
const maxPages = 100000

func (e *Executor) storage(d deployment.Deployment, st *StoragePlan) error {
	h := e.Hosts[st.Anchor]
	if h == nil {
		return fmt.Errorf("%s: no way to reach Garage", st.Anchor)
	}
	for _, b := range st.Buckets {
		e.work("delete bucket " + b.Name)
		if err := e.empty(h, st.Address, b); err != nil {
			return fmt.Errorf("%s: emptying bucket %s: %w", st.Anchor, b.Name, err)
		}
		state, err := garage.ReadBucketState(h, d, b.Name)
		if err != nil {
			return err
		}
		if !state.Absent {
			if err := garage.Execute(&garage.Plan{Steps: []garage.Step{garage.DeleteBucketStep(d, b.Name)}}, h); err != nil {
				return fmt.Errorf("%s: %w", st.Anchor, err)
			}
		}
	}
	for _, id := range st.Keys {
		e.work("delete S3 key " + id)
		kb, err := garage.ReadKeyBuckets(h, d, id)
		if err != nil {
			return err
		}
		if !kb.Absent {
			if err := garage.Execute(&garage.Plan{Steps: []garage.Step{garage.DeleteKeyStep(d, id)}}, h); err != nil {
				return fmt.Errorf("%s: %w", st.Anchor, err)
			}
		}
	}
	return nil
}

// empty deletes every object in a bucket through Garage's S3 API with the
// app's own key, a page of up to a thousand at a time, until a listing
// comes back empty. A page that comes back unchanged after its deletes is
// an error rather than a loop.
func (e *Executor) empty(h Host, address string, b BucketPlan) error {
	req := garage.S3Request{Address: address, Bucket: b.Name, KeyID: b.KeyID, Secret: b.secret, Region: Region,
		Query: url.Values{"list-type": {"2"}, "max-keys": {"1000"}}}
	if b.secret == "" {
		return fmt.Errorf("no secret is recorded for key %s, so the bucket cannot be listed", b.KeyID)
	}
	last := ""
	for i := 0; i < maxPages; i++ {
		out, err := h.RunInput(garage.CurlStdin, req.ListConfig())
		if err != nil {
			return fmt.Errorf("listing: %s", req.Redact(fmt.Sprintf("%v: %s", err, firstLine(out))))
		}
		page, err := garage.ParseListPage(out)
		if err != nil {
			return err
		}
		if len(page.Keys) == 0 {
			return nil
		}
		if page.Keys[0] == last {
			return fmt.Errorf("object %q is still listed after its delete", last)
		}
		last = page.Keys[0]
		if out, err := h.RunInput(garage.CurlStdin, req.DeleteConfig(page.Keys)); err != nil {
			return fmt.Errorf("deleting objects: %s", req.Redact(fmt.Sprintf("%v: %s", err, firstLine(out))))
		}
	}
	return fmt.Errorf("still not empty after %d pages", maxPages)
}

func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

package siteremove

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/appremove"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/webserver"
)

// inspect reads the host's inventory. It is a variable so that this
// package's tests can hand in a fake host's Docker objects and manifest:
// hostcheck's own probes are tested in internal/hostcheck.
var inspect = func(t apply.Transport, cfg *config.Config) (*hostcheck.Inventory, error) {
	return hostcheck.Inspect(t, cfg.Deployment())
}

// hostPlan is what stage 3 does on the site's host, decided from what the
// probes read.
type hostPlan struct {
	dockerPresent bool
	containers    []string
	networks      []string
	volumes       []string
	// images are the IDs of the images only this deployment's containers
	// run, sorted.
	images []string
	files  []appremove.File
	// webServer are the reference files for the host owner's web server
	// the manifest records.
	webServer []render.ManifestWebServer
	manifest  bool
	// home is the deployment's user's home, and userUnits the units host
	// prepare wrote for the deployment in its systemd directory there.
	home      string
	userUnits []string
	// keyRecords is this deployment's record of the authorized keys host
	// admit and host prepare added, when the host has one.
	keyRecords []string
	root       dirState
	// record is whether the host holds this deployment's record, a gateway's.
	record bool
	// granted is whether the host holds the record of what this deployment
	// last granted there.
	granted bool
	// deleteRoot is --delete-data with no edited file under the root.
	deleteRoot bool
	// caddy is the gateway's Caddy kept for the host owner's sites, nil
	// when it goes like everything else.
	caddy *caddyPlan
	// dataLeft is whether, without --delete-data, the root keeps files
	// apply did not write.
	dataLeft bool
	// keepData is whether data is left, without --delete-data: files in the
	// root apply did not write, or the deployment's volumes.
	keepData bool
	// store is what the host holds of the deployment's configuration store:
	// the repository, the peers file, the sync log and the script directory.
	store []storeRoot
	// verified is set once the run has checked the host.
	verified bool
}

// dirState is what appremove.DirCommand read of a directory.
type dirState struct {
	exists  bool
	bytes   int64
	files   int
	entries []string
}

// storeRoot is one path of the configuration store, and the file that shows
// it is there. A directory goes with everything in it.
type storeRoot struct {
	path, probe string
	dir         bool
}

// storeRoots are the paths host prepare gives the configuration store, each
// probed by one file that is in it. A directory is probed by the file inside
// it that prepare always writes, so a path is listed only when it is there.
func (p *Plan) storeRoots() []storeRoot {
	d := p.dep()
	return []storeRoot{
		{hostprep.ConfigRepo(d), hostprep.ConfigRepo(d) + "/HEAD", true},
		{hostprep.ConfigPeers(d), hostprep.ConfigPeers(d), false},
		{hostprep.ConfigSyncLog(d), hostprep.ConfigSyncLog(d), false},
		{hostprep.ConfigSyncLog(d) + ".tmp", hostprep.ConfigSyncLog(d) + ".tmp", false},
		{hostprep.ConfigIncoming(d), hostprep.ConfigIncoming(d), false},
		{hostprep.ConfigScriptDir(d), hostprep.ConfigScriptDir(d) + "/config-sync.sh", true},
	}
}

// userUnitsCommand lists the deployment's units in its user's own systemd
// directory: the configuration store's, which host prepare writes.
func (p *Plan) userUnitsCommand(home string) string {
	return fmt.Sprintf(`for f in %s/%s-*; do [ -e "$f" ] && echo "unit $f"; done; true`, hostprep.UserUnitDir(home), p.dep().Prefix())
}

// removeUserUnitsCommand disables, stops and deletes each, then reloads the
// user's manager.
func (p *Plan) removeUserUnitsCommand(home string) string {
	return fmt.Sprintf(`for f in %[1]s/%[2]s-*; do [ -e "$f" ] || continue; %[3]s disable --now "$(basename "$f")" >/dev/null 2>&1 || true; rm -f -- "$f"; done; %[3]s daemon-reload`, hostprep.UserUnitDir(home), p.dep().Prefix(), hostprep.UserSystemctl)
}

// emptyRootCommand empties the deployment's root, as root of a new user
// namespace that maps the user's subordinate range, which is what rootlesskit
// runs a command as (Docker's rootless documentation, Uninstall: rootlesskit
// rm -rf ~/.local/share/docker): a file a container's user owns is the
// user's to remove there. With deleteData everything goes; without, only
// the empty directories. The root itself stays: /srv/paisans is root's, and
// host evict removes it.
func emptyRootCommand(d deployment.Deployment, deleteData bool) string {
	root := quote(d.Root())
	if deleteData {
		return fmt.Sprintf("if [ -d %[1]s ]; then rootlesskit find %[1]s -mindepth 1 -delete; fi", root)
	}
	return fmt.Sprintf("if [ -d %[1]s ]; then rootlesskit find %[1]s -mindepth 1 -depth -type d -empty -delete; fi", root)
}

// containersCommand stops and removes every container and network carrying
// this deployment's label, whatever its project, selected by Docker's own
// label filter so that nothing without the label is ever selected. A kept
// Caddy, by its full ID, is left out.
func (p *Plan) containersCommand() string {
	d := p.dep()
	filter := "--filter " + quote(d.LabelFilter())
	rm := "docker rm"
	if p.DeleteData {
		rm += " -v"
	}
	guard, keep := "", ""
	switch cp := p.host.caddy; {
	case cp != nil:
		// A separate step, so a docker ps that fails still stops the command.
		keep = `ids=$(printf '%s\n' $ids | grep -vxF ` + quote(cp.container.ID) + ` || true); `
	case p.cfg.Sites[p.Site].Has(config.RoleGateway):
		// The plan removes the Caddy. A site of the host owner's that
		// appeared since would lose it, so the run stops before anything.
		guard = fmt.Sprintf(`if { { [ -d %[1]s ] && ! { [ -r %[1]s ] && [ -x %[1]s ]; }; } || ls -d %[1]s/*.caddy >/dev/null 2>&1; } && [ -n "$(docker ps -aq %[2]s --filter %[3]s --filter %[4]s)" ]; then echo "a site of the host owner's is in %[1]s now, served by this deployment's Caddy, which this plan removes. Nothing was removed; run again, and the Caddy is kept for it"; exit 3; fi; `,
			render.HostSitesDir(d), filter, quote("label=com.docker.compose.project="+d.Project("infra")), quote("label=com.docker.compose.service=caddy"))
	}
	return fmt.Sprintf(`set -e; %[4]sids=$(docker ps -aq --no-trunc %[1]s); %[3]sif [ -n "$ids" ]; then docker stop $ids >/dev/null; %[2]s $ids >/dev/null; fi; nets=$(docker network ls -q --no-trunc %[1]s); if [ -n "$nets" ]; then docker network rm $nets >/dev/null; fi`, filter, rm, keep, guard)
}

// planImages finds the images only this deployment ran: those its containers
// run from, and those its compose files name, which the manifest proves are
// its own. The files find them even after a run stopped between removing the
// containers and removing their images. An image any other container uses,
// running or stopped, whoever's it is, is kept.
func (p *Plan) planImages(t apply.Transport, hp *hostPlan, inv *hostcheck.Inventory, refs []string) error {
	if !hp.dockerPresent {
		return nil
	}
	d := p.dep()
	ours, theirs := map[string]bool{}, map[string]bool{}
	running := map[string]bool{}
	for _, c := range inv.Containers {
		if c.Image == "" {
			continue
		}
		switch {
		case hp.caddy != nil && c.ID == hp.caddy.container.ID:
			running[c.Image] = true
		case c.Deployment == d.ID:
			ours[c.Image] = true
		default:
			theirs[c.Image] = true
		}
	}
	ids, err := apply.ProbeImages(dedupe(refs), t)
	if err != nil {
		return fmt.Errorf("site remove %s: %w", p.Site, err)
	}
	for _, id := range ids {
		if id != "" {
			ours[id] = true
		}
	}
	for id := range ours {
		if running[id] {
			// The kept Caddy runs from it.
			continue
		}
		if theirs[id] {
			p.Kept = append(p.Kept, imageKept(p.Site, id, "a container this deployment does not own runs from it"))
			continue
		}
		hp.images = append(hp.images, id)
	}
	sort.Strings(hp.images)
	return nil
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// imagesCommand removes each image by ID, without -f, and answers per image,
// so an image Docker refuses (one a container still runs from) is reported
// rather than failing the stage.
func imagesCommand(ids []string) string {
	var q []string
	for _, id := range ids {
		q = append(q, quote(id))
	}
	return fmt.Sprintf(`for i in %s; do if out=$(docker image rm "$i" 2>&1); then echo "removed $i"; else echo "kept $i $(printf %%s "$out" | tr '\n' ' ')"; fi; done`, strings.Join(q, " "))
}

func (p *Plan) volumesCommand() string {
	return fmt.Sprintf(`set -e; vols=$(docker volume ls -q --filter %s); if [ -n "$vols" ]; then docker volume rm $vols >/dev/null; fi`, quote(p.dep().LabelFilter()))
}

func parseDir(out string) (dirState, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "present" {
		return dirState{}, nil
	}
	if len(lines) < 3 {
		return dirState{}, fmt.Errorf("a cut short answer")
	}
	var d dirState
	var err error
	d.exists = true
	if d.bytes, err = strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64); err != nil {
		return dirState{}, err
	}
	if d.files, err = strconv.Atoi(strings.TrimSpace(lines[2])); err != nil {
		return dirState{}, err
	}
	for _, l := range lines[3:] {
		if l = strings.TrimSpace(l); l != "" {
			d.entries = append(d.entries, l)
		}
	}
	return d, nil
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// buildHost is stage 3. It probes the host and plans every removal, each
// proven this deployment's, and lists what it keeps.
func (p *Plan) buildHost() (*Stage, error) {
	st := &Stage{Number: 3, Name: "clean the host", Short: "nothing of this deployment is left"}
	d := p.dep()
	user := p.cfg.Sites[p.Site].SSHUser()
	p.Evict = EvictLine(d, user)
	if p.HostGone {
		st.Skipped = "--host-gone: " + p.Site + "'s host is not reached"
		p.Kept = append(p.Kept, p.hostGoneLeft())
		return st, nil
	}
	t := p.transports[p.Site]
	hp := &hostPlan{}
	p.host = hp

	if err := p.checkLogin(t); err != nil {
		return nil, err
	}
	hp.home = p.home
	p.Evict = EvictLine(d, p.loginUser)

	inv, err := inspect(t, p.cfg)
	if err != nil {
		return nil, fmt.Errorf("site remove %s: %w", p.Site, err)
	}
	if inv.Manifest && inv.ManifestFiles == nil {
		return nil, fmt.Errorf("site remove %s: %s is not readable as a manifest, so no file there is provably this deployment's. Nothing was changed", p.Site, inv.ManifestPath)
	}
	hp.dockerPresent = inv.Docker.Present
	hp.manifest = inv.Manifest
	for _, e := range inv.ManifestWebServer {
		// The manifest is the user's own file: an entry that is not one of
		// the deployment's reference files is never read or deleted.
		if !webserver.OwnReference(p.dep(), e.Path) {
			p.Kept = append(p.Kept, fmt.Sprintf("%s: %s: not a reference file of this deployment's, so site remove leaves it; the host's owner removes it if it is this deployment's", p.Site, e.Path))
			continue
		}
		hp.webServer = append(hp.webServer, e)
	}
	// Before anything else is planned: a kept Caddy changes what goes.
	if hp.caddy, err = p.planCaddy(t, inv); err != nil {
		return nil, err
	}
	cp := hp.caddy
	for _, c := range inv.Containers {
		if cp != nil && c.ID == cp.container.ID {
			continue
		}
		if c.Deployment == d.ID {
			hp.containers = append(hp.containers, c.Name)
		}
	}
	for _, n := range inv.Networks {
		if n.Deployment == d.ID {
			hp.networks = append(hp.networks, n.Name)
		}
	}
	for _, v := range inv.Volumes {
		if v.Deployment == d.ID && !v.Anonymous {
			hp.volumes = append(hp.volumes, v.Name)
			if !p.DeleteData {
				p.Kept = append(p.Kept, volumeKept(p.Site, v.Name))
			}
		}
	}

	edited := false
	var refs []string
	proven := map[string]bool{}
	var inside []render.ManifestFile
	for _, e := range inv.ManifestFiles {
		if strings.ContainsAny(e.Path, "'\n") {
			return nil, fmt.Errorf("site remove %s: the manifest names %q, which this command will not put in a command line", p.Site, e.Path)
		}
		// The manifest is the user's own file: a path that is not clean can
		// name a file anywhere while it starts with the root, so it is
		// never read or deleted.
		if !cleanRel(e.Path) {
			p.Kept = append(p.Kept, fmt.Sprintf("%s: /%s: not a clean path under %s, so site remove leaves it; the host's owner removes it if it is this deployment's", p.Site, e.Path, d.Root()))
			continue
		}
		// The user can remove nothing outside the root, and nothing there is
		// provably this deployment's to the host's owner.
		if !strings.HasPrefix(e.Path, d.Rel()+"/") {
			p.Kept = append(p.Kept, fmt.Sprintf("%s: /%s: outside %s, so site remove leaves it; the host's owner removes it if it is this deployment's", p.Site, e.Path, d.Root()))
			continue
		}
		inside = append(inside, e)
	}
	read, err := readManifestFiles(t, inside)
	if err != nil {
		return nil, fmt.Errorf("site remove %s: %w", p.Site, err)
	}
	for i, e := range inside {
		f := appremove.File{Entry: e}
		switch r := read[i]; {
		case !r.found:
			f.State = appremove.Gone
		case r.sha256 == e.SHA256:
			f.State = appremove.Remove
			proven[e.Path] = true
			if isCompose(e.Path) {
				named, err := apply.ComposeImages(r.content)
				if err != nil {
					return nil, fmt.Errorf("site remove %s: reading the images /%s names: %w", p.Site, e.Path, err)
				}
				refs = append(refs, named...)
			}
		default:
			f.State = appremove.Edited
			if cp == nil || !p.keptFile(e.Path) {
				edited = true
				p.Kept = append(p.Kept, editedFileKept(p.Site, e.Path))
			}
		}
		if cp != nil && p.keptFile(e.Path) {
			continue
		}
		hp.files = append(hp.files, f)
	}
	if cp != nil {
		// The manifest stays, listing the kept files, which proves them
		// this deployment's to the run that removes them.
		hp.manifest = false
		want, err := p.keptManifest(inv.ManifestFiles, proven, cp.reduced)
		if err != nil {
			return nil, err
		}
		have, _, err := t.ReadFile(d.Manifest())
		if err != nil {
			return nil, fmt.Errorf("site remove %s: reading %s: %w", p.Site, d.Manifest(), err)
		}
		if have != want {
			cp.manifest = want
		}
	}
	sort.Slice(hp.files, func(i, j int) bool { return hp.files[i].Entry.Path < hp.files[j].Entry.Path })
	if err := p.planImages(t, hp, inv, refs); err != nil {
		return nil, err
	}
	if _, hp.record, err = t.ReadFile(deployrecord.Path(d)); err != nil {
		return nil, fmt.Errorf("site remove %s: reading %s: %w", p.Site, deployrecord.Path(d), err)
	}
	if _, hp.granted, err = t.ReadFile(grant.Path(d)); err != nil {
		return nil, fmt.Errorf("site remove %s: reading %s: %w", p.Site, grant.Path(d), err)
	}

	for _, r := range p.storeRoots() {
		_, found, err := t.ReadFile(r.probe)
		if err != nil {
			return nil, fmt.Errorf("site remove %s: reading %s: %w", p.Site, r.probe, err)
		}
		if found {
			hp.store = append(hp.store, r)
		}
	}

	out, err := t.Run(p.userUnitsCommand(hp.home))
	if err != nil {
		return nil, fmt.Errorf("site remove %s: listing its units: %w: %s", p.Site, err, lastLines(out, 2))
	}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "unit "); ok {
			hp.userUnits = append(hp.userUnits, path)
		}
	}

	if err := p.probeKeys(t, hp); err != nil {
		return nil, err
	}

	if cp != nil {
		err = p.planKeptRoot(t, hp, edited)
	} else {
		err = p.planRoot(t, hp, inv, edited)
	}
	if err != nil {
		return nil, err
	}
	if cp == nil && !p.DeleteData && (hp.dataLeft || len(hp.volumes) > 0) {
		hp.keepData, p.DataKept = true, true
		p.Kept = append(p.Kept, fmt.Sprintf("%s: the data in %s and the deployment's volumes stay; host evict deletes them", p.Site, d.Root()))
	}
	if cp != nil {
		p.Kept = append(p.Kept, fmt.Sprintf("%s: its Caddy keeps serving the host owner's sites in %s; host evict stops it and deletes that directory, so the owner moves those sites first", p.Site, render.HostSitesDir(d)))
	}

	p.hostSteps(st)
	st.Gate = "no container, network, unit or file of this deployment's left that its user can remove, and the root empty but for what is kept"
	if hp.caddy != nil {
		st.Gate = "no container, network, unit or file of this deployment's left that its user can remove but its Caddy, whose Caddyfile is reduced to the host owner's sites, and the root empty but for what is kept"
	}
	st.run = p.runHost
	// The run checks the host last and records that it did; a run with
	// nothing left to do checks it here.
	st.gate = func() error {
		if hp.verified {
			return nil
		}
		return p.verifyHost(t)
	}
	return st, nil
}

// checkLogin confirms, once a run and before anything else on the host is
// read, that the login is the deployment's user: the one its admission
// record names, which is never root. As anyone else, root above all, a link
// the user planted under the root could steer a deletion into what that
// login may delete and the user may not, and the units, the containers and
// the evict line would be the login's, not the user's. The record is root's,
// in root's own directory, so reading it is safe whoever logs in.
func (p *Plan) checkLogin(t apply.Transport) error {
	if p.home != "" {
		return nil
	}
	d := p.dep()
	account, err := hostprep.LoginAccount(t)
	if err != nil {
		return fmt.Errorf("site remove %s: %w", p.Site, err)
	}
	rec, err := admission.Read(t, d.Token())
	recordPath, _ := admission.Path(d.Token())
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
		return fmt.Errorf("site remove %s: %s has no admission record for this deployment (%s), so nothing says who its user is, and site remove cleans a host only as that user. Nothing was changed. If the deployment's user is still there, the host's owner removes it and everything root's with sudo paisans host evict --deployment %s --user <the deployment's user> --execute", p.Site, t.Describe(), recordPath, d.ID)
	case err != nil:
		return fmt.Errorf("site remove %s: %w. Nothing was changed", p.Site, err)
	}
	if account.UID == 0 || account.Name != rec.User {
		who := account.Name
		if account.UID == 0 {
			who = "root"
		}
		dest := p.cfg.Sites[p.Site].Destination()
		dest.User = rec.User
		return fmt.Errorf("site remove %s: %s logs in as %s, not the deployment's user %s that %s names, and site remove cleans a host only as that user, so that nothing the user planted under %s can steer a deletion past what that user may delete. Nothing was changed. Run it again with --ssh %s; the host's owner then removes what is root's with %s", p.Site, t.Describe(), who, rec.User, recordPath, d.Root(), dest, EvictLine(d, rec.User))
	}
	p.home, p.loginUser = account.Home, rec.User
	return nil
}

// cleanRel reports whether a manifest path is relative and clean: no
// leading slash, no empty, . or .. element, so the root it starts with is
// the directory it is in.
func cleanRel(p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, e := range strings.Split(p, "/") {
		if e == ".." {
			return false
		}
	}
	return true
}

// planRoot plans the deployment's directory: its empty directories, or with
// --delete-data all of it unless a file in it was edited.
func (p *Plan) planRoot(t apply.Transport, hp *hostPlan, inv *hostcheck.Inventory, edited bool) error {
	d := p.dep()
	out, err := t.Run(appremove.DirCommand(d.Root()))
	if err != nil {
		return fmt.Errorf("site remove %s: reading %s: %w", p.Site, d.Root(), err)
	}
	if hp.root, err = parseDir(out); err != nil {
		return fmt.Errorf("site remove %s: reading %s: %w", p.Site, d.Root(), err)
	}
	hp.deleteRoot = p.DeleteData && !edited
	switch {
	case !hp.root.exists:
	case p.DeleteData && edited:
		p.Kept = append(p.Kept, rootEditedKept(p.Site, d.Root()))
	case !p.DeleteData:
		rendered := 0
		for _, e := range inv.ManifestFiles {
			if strings.HasPrefix("/"+e.Path, d.Root()+"/") {
				rendered++
			}
		}
		if hp.manifest {
			rendered++
		}
		if hp.root.files > rendered {
			hp.dataLeft = true
			p.Kept = append(p.Kept, dataLeft(p.Site, d.Root()))
		}
	}
	return nil
}

// planKeptRoot plans the deployment's directory around a kept Caddy: with
// --delete-data everything but Caddy's paths, unless a file was edited;
// without it, the empty directories once the files are gone.
func (p *Plan) planKeptRoot(t apply.Transport, hp *hostPlan, edited bool) error {
	d, cp := p.dep(), hp.caddy
	out, err := t.Run(p.keptProbe())
	if err != nil {
		return fmt.Errorf("site remove %s: reading %s: %w: %s", p.Site, d.Root(), err, lastLines(out, 2))
	}
	others, empty, files := p.parseKeptProbe(out)
	cp.others, cp.empty = others, empty
	hp.deleteRoot = p.DeleteData && !edited
	switch {
	case p.DeleteData && edited:
		p.Kept = append(p.Kept, rootEditedKept(p.Site, d.Root()))
	case !p.DeleteData:
		removed := map[string]bool{}
		for _, f := range hp.files {
			if f.State == appremove.Remove {
				removed["/"+f.Entry.Path] = true
			}
		}
		for _, f := range files {
			if !removed[f] {
				cp.dataFiles++
			}
		}
		if cp.dataFiles > 0 {
			p.Kept = append(p.Kept, dataLeft(p.Site, d.Root()))
		}
	}
	return nil
}

// probeKeys finds this deployment's record of the authorized keys host
// admit and host prepare added. The keys themselves are never removed: they
// may be the only way into the host, and nothing here can tell whether
// another exists. The record is this deployment's own file, so it goes with
// it.
func (p *Plan) probeKeys(t apply.Transport, hp *hostPlan) error {
	path := hostprep.OwnedKeysPath(p.dep())
	_, found, err := t.ReadFile(path)
	if err != nil {
		return fmt.Errorf("site remove %s: reading this deployment's key record: %w", p.Site, err)
	}
	hp.keyRecords = nil
	if found {
		hp.keyRecords = []string{path}
	}
	return nil
}

func (p *Plan) hostSteps(st *Stage) {
	hp := p.host
	d := p.dep()
	add := func(verb, title, format string, args ...any) {
		st.Steps = append(st.Steps, Step{Site: p.Site, Verb: verb, Title: title, Text: fmt.Sprintf(format, args...)})
	}
	cp := hp.caddy
	if cp != nil && cp.reduce() {
		add("reduce", "reduce Caddy to the host's sites", "Caddy %s, kept because it serves the host owner's sites (%s): its Caddyfile reduced to the global options, the snippets they may import and `import %s/*.caddy`, staged where the container sees it, validated in it and loaded with caddy reload, then written over %s in place; a failure puts the original back and removes nothing else. Its compose file, Caddyfile, caddy.env, data and config stay, kept with --delete-data too", cp.container.Name, strings.Join(cp.sites, ", "), render.HostSitesMount, p.caddyfile())
	}
	for _, f := range hp.webServer {
		add("remove", "remove reference files", "%s; the host's owner removes the block from %s", f.Path, appremove.OwnerServer([]render.ManifestWebServer{f}))
	}
	if len(hp.containers) > 0 || len(hp.networks) > 0 {
		add("remove", "remove containers and networks", "every container and network with this deployment's label: %s", strings.Join(append(append([]string{}, hp.containers...), hp.networks...), ", "))
	}
	if p.DeleteData && len(hp.volumes) > 0 {
		add("delete", "delete volumes", "volumes %s", strings.Join(hp.volumes, ", "))
	}
	if len(hp.images) > 0 {
		add("remove", "remove images", "images %s, which only this deployment's containers ran, by ID and without -f, so Docker refuses one in use", strings.Join(hp.images, ", "))
	}
	for _, f := range hp.files {
		switch f.State {
		case appremove.Remove:
			add("delete", "delete files", "/%s", f.Entry.Path)
		case appremove.Edited:
			add("keep", "keep edited files", "/%s: edited on the host since apply wrote it", f.Entry.Path)
		}
	}
	if hp.manifest {
		add("delete", "delete the manifest", "%s, the manifest", d.Manifest())
	}
	if cp != nil && cp.manifest != "" {
		add("rewrite", "rewrite the manifest", "%s, to list only the kept Caddy's files, which proves them this deployment's to the run that removes them", d.Manifest())
	}
	if hp.record {
		add("delete", "delete the deployment record", "%s, this deployment's record of what it deployed", deployrecord.Path(d))
	}
	for _, u := range hp.userUnits {
		add("remove", "remove units", "unit %s, disabled and stopped first", u)
	}
	for _, r := range hp.store {
		add("delete", "delete the configuration store", "%s, part of this deployment's configuration store", r.path)
	}
	switch {
	case cp != nil && hp.deleteRoot && len(cp.others) > 0:
		add("delete", "delete "+d.Root()+" but Caddy's files", "everything in %s but the kept Caddy's files, which are kept with --delete-data, through rootlesskit: %s", d.Root(), strings.Join(cp.others, ", "))
	case cp != nil && !p.DeleteData && (len(cp.empty) > 0 || len(hp.files) > 0):
		add("remove", "remove empty directories", "the empty directories under %s, but never one the kept Caddy mounts", d.Root())
	case cp != nil:
	case hp.root.exists && hp.deleteRoot:
		add("delete", "empty "+d.Root(), "everything in %s, through rootlesskit: %d file(s), %d bytes; %s itself stays for host evict", d.Root(), hp.root.files, hp.root.bytes, d.Root())
	case hp.root.exists:
		add("remove", "remove empty directories", "the empty directories under %s, which itself stays for host evict", d.Root())
	}
	if hp.granted {
		add("delete", "delete the grant record", "%s, the keys and images this deployment last granted here", grant.Path(d))
	}
}

// runHost cleans the host, in the order the plan lists, each step reading
// live state or keyed by the label, so a re-run does only what is left.
func (p *Plan) runHost() error {
	hp := p.host
	t := p.transports[p.Site]
	d := p.dep()
	run := func(what, command string) error {
		if out, err := t.Run(command); err != nil {
			return fmt.Errorf("%s: %s: %w: %s", p.Site, what, err, lastLines(out, 3))
		}
		return nil
	}
	// The Caddy first: a failure there leaves the deployment as it was.
	cp := hp.caddy
	if cp != nil && cp.reduce() {
		if err := p.reduceCaddy(t, cp); err != nil {
			return err
		}
	}
	// The reference files next: apply wrote them in the deployment's root
	// for the host's owner, who copied them into their web server. That
	// server is theirs, so nothing here touches it; what is left names the
	// block they remove.
	for _, f := range hp.webServer {
		p.work("remove reference files")
		if err := run("removing "+f.Path, "rm -f -- "+quote(f.Path)); err != nil {
			return err
		}
	}
	p.Kept = append(p.Kept, appremove.OwnerBlocks(p.Site, hp.webServer)...)
	if hp.dockerPresent {
		p.work("remove containers and networks")
		if err := run("removing this deployment's containers and networks", p.containersCommand()); err != nil {
			return err
		}
		if p.DeleteData {
			p.work("delete volumes")
			if err := run("removing this deployment's volumes", p.volumesCommand()); err != nil {
				return err
			}
		}
		if len(hp.images) > 0 {
			p.work("remove images")
			out, err := t.Run(imagesCommand(hp.images))
			if err != nil {
				return fmt.Errorf("%s: removing images: %w: %s", p.Site, err, lastLines(out, 3))
			}
			for _, line := range strings.Split(out, "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "kept "); ok {
					id, why, _ := strings.Cut(rest, " ")
					p.Kept = append(p.Kept, imageKept(p.Site, id, "Docker refused: "+why))
				}
			}
		}
	}
	if len(hp.files) > 0 {
		p.work("delete files")
		out, err := t.Run(appremove.FilesCommand(hp.files))
		if err != nil {
			return fmt.Errorf("%s: removing files: %w: %s", p.Site, err, lastLines(out, 3))
		}
		answered := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			if _, path, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
				answered[strings.TrimPrefix(path, "/")] = true
			}
		}
		for _, f := range hp.files {
			if !answered[f.Entry.Path] {
				return fmt.Errorf("%s: removing files: no answer for /%s, so the manifest is kept", p.Site, f.Entry.Path)
			}
		}
	}
	if cp != nil && cp.manifest != "" {
		p.work("rewrite the manifest")
		if err := t.WriteFile(d.Manifest(), cp.manifest, 0o600); err != nil {
			return err
		}
	}
	if hp.manifest {
		p.work("delete the manifest")
		if err := run("deleting the manifest", "rm -f -- "+quote(d.Manifest())); err != nil {
			return err
		}
	}
	if hp.record {
		p.work("delete the deployment record")
		if err := run("deleting the deployment record", deployrecord.RemoveCommand(d)); err != nil {
			return err
		}
	}
	// A plain rm, as the deployment and grant records go, and no lock: the
	// lock would create a lock file in the state directory again.
	for _, r := range hp.keyRecords {
		if err := run("deleting the key record", "rm -f -- "+quote(r)); err != nil {
			return err
		}
	}
	if len(hp.userUnits) > 0 {
		p.work("remove units")
		if err := run("removing units", p.removeUserUnitsCommand(hp.home)); err != nil {
			return err
		}
	}
	// After the units: the daemon and the sync have stopped using them.
	for _, r := range hp.store {
		p.work("delete the configuration store")
		command := "rm -f -- " + quote(r.path)
		if r.dir {
			command = "rm -rf -- " + quote(r.path)
		}
		if err := run("deleting "+r.path, command); err != nil {
			return err
		}
	}
	switch {
	case cp != nil && hp.deleteRoot && len(cp.others) > 0:
		p.work("delete " + d.Root() + " but Caddy's files")
		if err := run("removing "+d.Root()+" but Caddy's files", p.deleteBesideKept()); err != nil {
			return err
		}
	case cp != nil && !p.DeleteData && (len(cp.empty) > 0 || len(hp.files) > 0):
		p.work("remove empty directories")
		if err := run("removing the empty directories under "+d.Root(), p.emptyBesideKept()); err != nil {
			return err
		}
	case cp != nil:
	case hp.root.exists:
		if hp.deleteRoot {
			p.work("empty " + d.Root())
		} else {
			p.work("remove empty directories")
		}
		if err := run("emptying "+d.Root(), emptyRootCommand(d, hp.deleteRoot)); err != nil {
			return err
		}
	}
	// The grant record goes: a later deployment with this token would
	// otherwise be reviewed against a baseline it never applied.
	if hp.granted {
		p.work("delete the grant record")
		if err := run("deleting the grant record", grant.RemoveCommand(d)); err != nil {
			return err
		}
	}
	p.work("check the host")
	if err := p.verifyHost(t); err != nil {
		return fmt.Errorf("%s: %w", p.Site, err)
	}
	hp.verified = true
	return nil
}

// verifyHost is the host stage's gate: nothing of this deployment's is left
// on the host.
func (p *Plan) verifyHost(t apply.Transport) error {
	d := p.dep()
	inv, err := inspect(t, p.cfg)
	if err != nil {
		return err
	}
	cp := p.host.caddy
	var left []string
	for _, c := range inv.Containers {
		if c.Deployment == d.ID && (cp == nil || c.ID != cp.container.ID) {
			left = append(left, "container "+c.Name)
		}
	}
	for _, n := range inv.Networks {
		if n.Deployment == d.ID {
			left = append(left, "network "+n.Name)
		}
	}
	if p.DeleteData {
		for _, v := range inv.Volumes {
			if v.Deployment == d.ID && !v.Anonymous {
				left = append(left, "volume "+v.Name)
			}
		}
	}
	if inv.Manifest && cp == nil {
		left = append(left, d.Manifest())
	}
	for _, r := range p.storeRoots() {
		_, found, err := t.ReadFile(r.probe)
		if err != nil {
			return err
		}
		if found {
			left = append(left, r.path)
		}
	}
	out, err := t.Run(p.userUnitsCommand(p.host.home))
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(out); s != "" {
		left = append(left, strings.Split(s, "\n")...)
	}
	if cp != nil {
		left = append(left, p.verifyCaddy(t, cp)...)
	}
	if len(left) > 0 {
		return fmt.Errorf("still on the host: %s", strings.Join(left, "; "))
	}
	return nil
}

// hostGoneLeft is what a host left alone with --host-gone still holds, for
// the report.
func (p *Plan) hostGoneLeft() string {
	d := p.dep()
	web := ""
	if p.cfg.Sites[p.Site].WebServerManagedBy() == config.ManagedByExternal {
		web = fmt.Sprintf(" Its web server is its owner's: the host's owner removes the blocks apply's reference files in %s held, or it keeps routing this deployment's hostnames to apps that no longer run.", webserver.ReferenceDir(d))
	}
	user := p.cfg.Sites[p.Site].SSHUser()
	return fmt.Sprintf("%s: host not reached (--host-gone). If it ever comes back it still holds the deployment's user %s with its containers, volumes and units, %s, and its admission record; its owner removes all of it with %s. Nothing it runs can reach the cluster: no remaining site has it as a WireGuard peer, and its etcd member is gone",
		p.Site, user, d.Root(), EvictLine(d, user)) + web
}

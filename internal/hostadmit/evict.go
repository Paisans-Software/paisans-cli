package hostadmit

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// EvictInput is what evict is given: the deployment, and the user the
// owner names, from a request or typed.
type EvictInput struct{ Deployment, User string }

// evictor is one BuildEvict: the planner admit's parts use, the
// deployment's record (nil when the host holds none), every other admitted
// deployment's record and the names of those that cannot be read, and
// whether the plan removes sysctl.before, which decides whether
// /etc/paisans is left empty.
type evictor struct {
	*planner
	rec        *admission.Record
	others     []admission.Record
	unreadable []string
	beforeGone bool
}

// evictParts are evict's stages in the order they run. The user's manager
// stops first, so nothing of the deployment runs while what fences it (the
// mesh, the egress table) goes; the egress table goes before the user, so
// on a re-run whose user is gone there is no table left that needed its
// UID; the record and the registry entry go last, so a failed evict is
// found and run again.
var evictParts = []struct {
	name string
	plan func(*evictor) error
}{
	{"manager", (*evictor).manager},
	{"mesh", (*evictor).mesh},
	{"egress", (*evictor).egress},
	{"user", (*evictor).user},
	{"directories", (*evictor).dirs},
	{"wireguard", (*evictor).helper},
	{"watchdog", (*evictor).watchdog},
	{"firewall", (*evictor).firewall},
	{"host wide", (*evictor).hostWide},
	{"sysctls", (*evictor).sysctls},
	{"record", (*evictor).admissionRecord},
	{"empty directories", (*evictor).emptyDirs},
	{"registry", (*evictor).registry},
}

// BuildEvict plans undoing admit for one deployment, leaving every other
// admitted deployment and the owner's own services as they were: what is
// the deployment's goes, and a host-wide file or sysctl goes only when no
// other admitted deployment needs it. Another deployment's record that
// cannot be read is taken to need everything it might, so beside one evict
// keeps the host-wide files, the sysctls and /etc/paisans. Each step is
// planned from a probe, so a re-run after a failure plans only what is
// left, and a re-run on a host evict finished plans nothing.
//
// The user is deleted only when it is the deployment's: the record's user,
// or, with no record, paisans-<token>. Anything else is refused, so a
// mistyped or lost name never deletes the owner's own account.
func BuildEvict(t apply.Transport, in EvictInput) (*Plan, error) {
	if !deployment.ValidID(in.Deployment) {
		return nil, fmt.Errorf("%q is not a deployment id. Nothing was changed", ui.Visible(in.Deployment))
	}
	if prob := config.DeploymentUserProblem(in.User, in.Deployment); prob != "" {
		return nil, fmt.Errorf("--user: %s. Nothing was changed", prob)
	}
	d := deployment.Deployment{ID: in.Deployment}
	content, found, err := t.ReadFile("/etc/os-release")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/os-release on %s: %w", t.Describe(), shown(err))
	}
	if !found {
		return nil, fmt.Errorf("%s has no /etc/os-release, so no host profile can be chosen for it", t.Describe())
	}
	host := hostprep.ParseOSRelease(content)
	profile, err := hostprep.Select(host)
	if err != nil {
		return nil, err
	}
	var rec *admission.Record
	switch r, err := admission.Read(t, d.Token()); {
	case errors.Is(err, admission.ErrNotAdmitted):
	case err != nil:
		return nil, fmt.Errorf("this deployment's admission record: %w. Move it aside and run host evict again. Nothing was changed", shown(err))
	default:
		rec = &r
	}
	switch {
	case rec != nil && rec.User != in.User:
		return nil, fmt.Errorf("this host's admission record for %s names the user %s, not %s. Evict deletes the deployment's own user and no other. Nothing was changed", d.Token(), ui.Visible(rec.User), in.User)
	case rec == nil && in.User != d.Prefix():
		return nil, fmt.Errorf("this host holds no admission record for %s, so evict deletes only %s, the deployment's own default user, and not %s, which may be someone else's. Nothing was changed", d.Token(), d.Prefix(), in.User)
	}
	ain := Input{Request: admission.Request{Deployment: in.Deployment, User: in.User}}
	if rec != nil && rec.DataDir != "" {
		ain.Flags.DataDir = path.Dir(rec.DataDir)
		if !plainDir(ain.Flags.DataDir) {
			return nil, fmt.Errorf("this deployment's admission record names data_dir %s, which admit never gives. Nothing was changed", ui.Visible(rec.DataDir))
		}
	}
	f, err := probe(t, d, ain)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", t.Describe(), shown(err))
	}
	// Where Docker's data would go is admit's question, not evict's.
	checked := f
	checked.Data.Unsafe = false
	if err := refuseUnsafe(checked, "evict"); err != nil {
		return nil, err
	}
	if u := f.User; u.Exists {
		var privileged []string
		if u.UID == 0 {
			privileged = append(privileged, "uid 0")
		}
		for _, g := range append([]string{u.Group}, u.Groups...) {
			if slices.Contains(PrivilegedGroups, g) && !slices.Contains(privileged, g) {
				privileged = append(privileged, g)
			}
		}
		if len(privileged) > 0 {
			return nil, fmt.Errorf("%s has %s, which admit never gives a deployment's user, so it is not this deployment's to delete. Nothing was changed", in.User, strings.Join(privileged, ", "))
		}
	}
	e := &evictor{rec: rec, planner: &planner{t: t, in: ain, d: d, f: f, profile: profile, host: host,
		plan: &Plan{Deployment: in.Deployment, User: in.User, Profile: profile.ID() + " " + profile.Version(), Host: t.Describe()}}}
	if e.others, e.unreadable, err = others(e.planner); err != nil {
		return nil, err
	}
	for _, pt := range evictParts {
		if err := pt.plan(e); err != nil {
			return nil, fmt.Errorf("%s: %w", pt.name, err)
		}
	}
	return e.plan, nil
}

// remove plans one of evict's steps.
func (e *evictor) remove(title, describe, command string) {
	e.step(Step{Title: title, Describe: "evict: " + describe, Command: command, Kinds: []string{"evict"}})
}

// manager stops the user's manager, which stops its Docker daemon and
// every container, and ends its sessions. The account is expired first, so
// no login, by key or otherwise, starts the manager again before userdel.
func (e *evictor) manager() error {
	u, user := e.f.User, e.in.Request.User
	if !u.Exists || !u.Linger && !u.ManagerActive {
		return nil
	}
	e.remove("stop "+user+"'s manager", fmt.Sprintf("expire %s so no login starts it again, disable linger, and stop user@%d.service, which stops its Docker daemon and every container", user, u.UID),
		fmt.Sprintf("usermod --expiredate 1 %s && loginctl disable-linger %s && systemctl stop user@%d.service && { loginctl terminate-user %s 2>/dev/null || true; }", user, user, u.UID, user))
	return nil
}

// mesh stops the deployment's interface and removes its conf, which holds
// the site's private key, then the drop-in that ordered the user's manager
// after it. The drop-in's path holds the UID, which the probe reads only
// while the user exists, so it goes before the user does.
func (e *evictor) mesh() error {
	unit, conf := e.d.WireGuardUnit(), e.d.WireGuardInstalled()
	if e.f.MeshUnit.Enabled || e.f.MeshUnit.Active {
		e.remove("stop the mesh", "stop and disable "+unit+", the deployment's mesh interface", "systemctl disable --now "+unit)
	}
	if e.f.Files[conf].Present {
		e.remove("remove "+conf, "the mesh's conf, which holds the site's private key", "rm -f "+conf)
	}
	if !e.f.User.Exists {
		return nil
	}
	dropIn := meshDropInPath(uidOf(e.planner))
	if e.f.Files[dropIn].Present {
		e.remove("remove "+dropIn, "the drop-in that started "+e.in.Request.User+"'s manager after the mesh, and its directory once empty",
			"rm -f "+dropIn+" && rmdir --ignore-fail-on-non-empty "+path.Dir(dropIn)+" && systemctl daemon-reload")
	}
	return nil
}

// egress unloads the egress table and removes its unit and file. The
// whole per-deployment table goes, whatever rules it holds. The user's
// manager required the unit; the directory systemctl made for that is
// removed once empty.
func (e *evictor) egress() error {
	unit, table := e.d.EgressUnit(), e.d.EgressTable()
	unitPath, file := "/etc/systemd/system/"+unit, egressFilePath(e.d)
	if !e.f.Files[unitPath].Present && !e.f.Files[file].Present && !e.f.EgressEnabled && !e.f.EgressTable {
		return nil
	}
	parts := []string{
		"{ systemctl disable --now " + unit + " 2>/dev/null || true; }",
		"{ ! nft list table inet " + table + " >/dev/null 2>&1 || nft delete table inet " + table + "; }",
		"rm -f " + unitPath + " " + file,
	}
	if e.f.User.Exists {
		requires := fmt.Sprintf("/etc/systemd/system/user@%d.service.requires", e.f.User.UID)
		parts = append(parts, "{ [ ! -d "+requires+" ] || rmdir --ignore-fail-on-non-empty "+requires+"; }")
	}
	e.remove("remove the egress table", "unload "+table+" and remove "+unit+" and "+file, strings.Join(append(parts, "systemctl daemon-reload"), " && "))
	return nil
}

// user deletes the user with its home, then any line of its in
// /etc/subuid and /etc/subgid that userdel left.
func (e *evictor) user() error {
	u, user := e.f.User, e.in.Request.User
	if u.Exists {
		e.remove("delete user "+user, "delete "+user+" with its home, "+ui.Visible(u.Home), "userdel --remove "+user)
	}
	if len(e.f.SubUID[user])+len(e.f.SubGID[user]) > 0 {
		e.remove("remove "+user+"'s subordinate ranges", "its lines in /etc/subuid and /etc/subgid, which no other user may then be given by accident",
			"sed -i '/^"+user+":/d' /etc/subuid /etc/subgid")
	}
	return nil
}

// dirs removes the deployment's Docker data on the disk --data-dir named,
// and its directory under /srv/paisans. Each is removed only when it is a
// directory, and without crossing into another filesystem; their parents
// are root's alone, so nothing can swap one for a link between the probe
// and the removal.
func (e *evictor) dirs() error {
	var dirs [][2]string
	if e.rec != nil && e.rec.DataDir != "" {
		dirs = append(dirs, [2]string{e.rec.DataDir, "the deployment's Docker data, on the disk --data-dir named"})
	}
	dirs = append(dirs, [2]string{e.d.Root(), "the deployment's directory"})
	for _, dir := range dirs {
		s := e.f.Dirs[dir[0]]
		if !s.Present {
			continue
		}
		if !strings.HasPrefix(s.Mode, "d") {
			return fmt.Errorf("%s is %q, not a directory, and evict removes only the directory admit made there. Look at what it is, remove it yourself if it is the deployment's, and run host evict again. Nothing was changed", ui.Visible(dir[0]), ui.Visible(s.Mode))
		}
		q := apply.ShellQuote(dir[0])
		e.remove("remove "+ui.Visible(dir[0]), dir[1], "[ ! -L "+q+" ] && rm -rf --one-file-system -- "+q)
	}
	return nil
}

// helper removes the sudoers line, then the WireGuard helper it named.
func (e *evictor) helper() error {
	helper, err := apply.WGHelper{Iface: e.d.Interface()}.Path()
	if err != nil {
		return err
	}
	if sudoers := sudoersPath(e.d); e.f.Files[sudoers].Present {
		e.remove("remove "+sudoers, "the line that let "+e.in.Request.User+" run the WireGuard helper as root", "rm -f "+sudoers+" "+sudoers+".new")
	}
	if e.f.Files[helper].Present {
		e.remove("remove "+helper, "the WireGuard helper", "rm -f "+helper)
	}
	return nil
}

// watchdog gives the watchdog back to root, as admit takes it back from a
// site that is no longer a data site, and stops loading its module at boot.
func (e *evictor) watchdog() error {
	rule, unit := watchdogRulePath(e.d), e.d.Prefix()+"-watchdog.service"
	if e.f.Files[rule].Present {
		e.remove("give the watchdog back to root", "remove "+rule+" and make the device root's again", "rm -f "+rule+" && "+udevTrigger+" && "+giveBack)
	}
	if e.f.Files["/etc/systemd/system/"+unit].Present {
		e.remove("stop loading the watchdog at boot", "remove /etc/systemd/system/"+unit,
			"systemctl disable "+unit+"; rm -f /etc/systemd/system/"+unit+" && systemctl daemon-reload")
	}
	return nil
}

// firewall removes every ufw rule tagged as the deployment's but its SSH
// allow, which may be what the owner's own login arrives by: that one is
// left, with a warning, for the owner to delete. The profile is given no
// rule to keep, so it plans no allow and refuses nothing; it keeps the SSH
// allow by its comment. The default policy and whether ufw is on are never
// changed.
func (e *evictor) firewall() error {
	s, err := e.profile.Firewall(e.t, e.d, nil, false, "host admit")
	if err != nil {
		return err
	}
	for _, st := range s.Steps {
		if st.Label == "remove" {
			e.remove(st.Title, "delete `"+strings.TrimPrefix(st.Command, "ufw delete ")+"`, which host admit added for the deployment", st.Command)
		}
	}
	for _, w := range s.Warnings {
		if what, ok := strings.CutPrefix(w.Hint, "firewall: old SSH allow "); ok {
			what, _, _ = strings.Cut(what, " kept;")
			e.warn("firewall: the deployment's SSH allow "+what+" is kept",
				"It may be the rule your own login arrives by. Delete it yourself once nothing else needs it.")
		}
	}
	return nil
}

// shared reports whether another deployment is admitted here, readable or
// not.
func (e *evictor) shared() bool { return len(e.others)+len(e.unreadable) > 0 }

// hostWide removes delegate.conf and modules-load.d/paisans.conf, each only
// when it is as admit wrote it and no other deployment is admitted.
// br_netfilter stays loaded until the next boot: unloading it would change
// what the host's FORWARD chain sees under whatever runs now.
func (e *evictor) hostWide() error {
	if e.shared() {
		for _, r := range e.others {
			e.present("host wide: delegate.conf and br_netfilter kept for deployment " + ui.Visible(r.Deployment))
		}
		for _, file := range e.unreadable {
			e.present("host wide: delegate.conf and br_netfilter kept, since " + ui.Visible(file) + " cannot be read")
		}
		return nil
	}
	if e.f.Files[delegatePath].Sum == sha(delegateConf) {
		e.remove("remove "+delegatePath, "no other deployment is admitted", "rm -f "+delegatePath+" && systemctl daemon-reload")
	}
	if sum := e.f.Files[modulesLoadPath].Sum; sum != "" && slices.ContainsFunc([]string{"nf_tables", "ip_tables"}, func(m string) bool {
		g := *e.planner
		g.f.IptablesModule = m
		return sum == sha(modulesLoadConf(&g))
	}) {
		e.remove("remove "+modulesLoadPath, "no other deployment is admitted; br_netfilter stays loaded until the next boot", "rm -f "+modulesLoadPath)
	}
	return nil
}

// sysctls restores each sysctl no other admitted deployment needs to the
// value admit found before it first changed it. A key 60-paisans.conf sets
// is kept beside a record evict cannot read, as admit keeps it.
func (e *evictor) sysctls() error {
	current, _, err := e.t.ReadFile(sysctlPath)
	if err != nil {
		return shown(err)
	}
	content, _, err := e.t.ReadFile(sysctlBeforePath)
	if err != nil {
		return shown(err)
	}
	was := parseBefore(content)
	if err := was.check("evict"); err != nil {
		return err
	}
	keep := len(e.unreadable) > 0
	setLow, setFwd := holds(current, keyLowPorts), holds(current, keyForward)
	var lowFor, fwdFor []string
	for _, r := range e.others {
		if r.Grants.PublicPorts {
			lowFor = append(lowFor, ui.Visible(r.Deployment))
		}
		if r.Forwarding {
			fwdFor = append(fwdFor, ui.Visible(r.Deployment))
		}
	}
	wantLow, wantFwd := len(lowFor) > 0 || keep && setLow, len(fwdFor) > 0 || keep && setFwd
	b := before{}
	for k, v := range was {
		b[k] = v
	}
	var restore []string
	for _, k := range []struct {
		key       string
		want, set bool
	}{{keyLowPorts, wantLow, setLow}, {keyForward, wantFwd, setFwd}} {
		v, ok := b[k.key]
		switch {
		case k.want:
		case ok:
			restore = append(restore, k.key+"="+v)
			delete(b, k.key)
		case k.set:
			e.warn("sysctls: "+k.key+" has no value recorded from before admit",
				sysctlBeforePath+" does not hold it, so it keeps its live value until the next boot, when the host's own setting applies.")
		}
	}
	for _, d := range lowFor {
		e.present("sysctls: low ports kept for deployment " + d)
	}
	for _, d := range fwdFor {
		e.present("sysctls: forwarding kept for deployment " + d)
	}
	command := ""
	if len(restore) > 0 {
		command = "sysctl -w " + strings.Join(restore, " ") + " >/dev/null"
	}
	file, before := e.f.Files[sysctlPath], e.f.Files[sysctlBeforePath]
	if !wantLow && !wantFwd {
		if !file.Present && !before.Present && command == "" {
			return nil
		}
		e.beforeGone = before.Present
		rm := "rm -f " + sysctlPath + " " + sysctlBeforePath
		if command != "" {
			rm = command + " && " + rm
		}
		e.remove("remove the host's paisans sysctls", "no admitted deployment needs them; set each back to what it was before admit", rm)
		return nil
	}
	want := SysctlFile(wantLow, wantFwd)
	var write *hostprep.File
	if file.Sum != sha(want) {
		write = &hostprep.File{Path: sysctlPath, Content: want, Mode: 0o644}
	}
	switch {
	case command != "":
		e.step(Step{Title: "restore " + strings.Join(restore, ", "), Describe: "evict: no other admitted deployment needs them; set each back to what it was before admit",
			File: write, Command: command, Kinds: []string{"evict"}})
	case write != nil:
		e.step(Step{Title: "rewrite " + sysctlPath, Describe: "evict: only what the other admitted deployments need", File: write, Kinds: []string{"evict"}})
	}
	switch {
	case b.text() == was.text():
	case len(b) == 0:
		e.beforeGone = true
		e.remove("remove "+sysctlBeforePath, "nothing left to restore", "rm -f "+sysctlBeforePath)
	default:
		e.step(Step{Title: "record the sysctls' values before", Describe: "evict: what is left to restore", File: &hostprep.File{Path: sysctlBeforePath, Content: b.text(), Mode: 0o644}, Kinds: []string{"evict"}})
	}
	return nil
}

// admissionRecord removes the admission record.
func (e *evictor) admissionRecord() error {
	file, err := admission.Path(e.d.Token())
	if err != nil {
		return err
	}
	if e.f.Files[file].Present {
		e.remove("remove the admission record", file, "rm -f "+file)
	}
	return nil
}

// emptyDirs removes each directory admit creates for every deployment,
// /srv/paisans, the helpers' directory and /etc/paisans, once it holds
// nothing but what this plan removed from it. One that holds anything
// else, another deployment's record or directory among them, is kept.
func (e *evictor) emptyDirs() error {
	helper, err := apply.WGHelper{Iface: e.d.Interface()}.Path()
	if err != nil {
		return err
	}
	record, err := admission.Path(e.d.Token())
	if err != nil {
		return err
	}
	ours := map[string]int{}
	if e.f.Dirs[e.d.Root()].Present {
		ours[deployment.Base]++
	}
	if e.f.Files[helper].Present {
		ours[apply.HelperDir]++
	}
	for _, f := range []string{record, egressFilePath(e.d)} {
		if e.f.Files[f].Present {
			ours[admission.Dir]++
		}
	}
	if e.beforeGone {
		ours[admission.Dir]++
	}
	for _, dir := range countedDirs() {
		n, ok := e.f.Entries[dir]
		switch {
		case !ok:
		case n == ours[dir]:
			e.remove("remove "+dir, "it holds nothing once this deployment's files are gone", "rmdir "+dir)
		case dir == admission.Dir:
			e.present("evict: " + dir + " kept, since it holds what is not this deployment's")
		}
	}
	return nil
}

// registry takes the deployment's entry out of the host's registry, last:
// until then the host still says the deployment is here.
func (e *evictor) registry() error {
	r, err := registry.Read(e.t)
	if err != nil {
		return shown(err)
	}
	if _, ok := r.Deployments[e.in.Request.Deployment]; ok {
		e.remove("remove the registry entry", "deployment "+e.d.Token()+" from "+registry.Path, registry.RemoveCommand(e.in.Request.Deployment))
	}
	return nil
}

// RunSteps runs plan's steps in order and stops at the first failure,
// naming it. Evict's steps never need a rebuild; each runs through the
// same run admit's Execute uses.
func RunSteps(t apply.Transport, plan *Plan, r ui.Reporter) error {
	for _, s := range plan.Steps {
		if err := run(t, s, r); err != nil {
			return err
		}
	}
	return nil
}

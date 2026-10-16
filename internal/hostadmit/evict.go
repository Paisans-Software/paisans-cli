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

// EvictInput is what evict is given: the deployment, the user the owner
// names, from a request or typed, and DataDir, the --data-dir admit was
// given, which evict needs only when the host holds no admission record.
type EvictInput struct{ Deployment, User, DataDir string }

// evictor is one BuildEvict: the planner admit's parts use, the
// deployment's record (nil when the host holds none), every other admitted
// deployment's record and the names of those that cannot be read, and
// whether the plan removes sysctl.before, which decides whether
// /etc/paisans is left empty.
type evictor struct {
	*planner
	rec *admission.Record
	// dataDir is the deployment's Docker data directory on the disk
	// --data-dir named, "" for none; fromFlag when --data-dir gave it,
	// with no record to say it is the deployment's.
	dataDir    string
	fromFlag   bool
	others     []admission.Record
	unreadable []string
	// recordless are the tokens of the other deployments the registry
	// claims the host for that have no admission record here.
	recordless []string
	beforeGone bool
}

// claimedWithoutRecord is the token of every other deployment the host's
// registry claims it for whose admission record is not here, an admit in
// flight or one that failed before its record, or a deployment host
// prepare put here. Evict keeps for them what it keeps beside a record it
// cannot read.
func claimedWithoutRecord(e *evictor) ([]string, error) {
	r, err := registry.Read(e.t)
	if err != nil {
		return nil, shown(err)
	}
	recorded := map[string]bool{}
	for _, file := range e.f.Records {
		recorded[strings.TrimSuffix(strings.TrimPrefix(path.Base(file), "admitted."), ".json")] = true
	}
	var out []string
	for _, id := range sortedKeys(r.Deployments) {
		if t := r.Deployments[id].Token; id != e.in.Request.Deployment && !recorded[t] {
			out = append(out, ui.Visible(t))
		}
	}
	return out, nil
}

// evictParts are evict's stages in the order they run. The user's manager
// stops first, so nothing of the deployment runs while what fences it (the
// mesh, the egress table) goes; the egress table goes before the user, so
// on a re-run whose user is gone there is no table left that needed its
// UID; the record and the registry entry go last, so a failed evict is
// found and run again. Every process of the user is killed before
// anything that fences it goes, and the data directories go while the user
// still exists, so a re-run can still tell they are its.
var evictParts = []struct {
	name string
	plan func(*evictor) error
}{
	{"manager", (*evictor).manager},
	{"processes", (*evictor).processes},
	{"mesh", (*evictor).mesh},
	{"egress", (*evictor).egress},
	{"directories", (*evictor).dirs},
	{"user", (*evictor).user},
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
		return nil, fmt.Errorf("this deployment's admission record: %w. Move it aside and run host evict again, and if admit was given --data-dir, give evict the same --data-dir, since the record said where the Docker data went. Nothing was changed", shown(err))
	default:
		rec = &r
	}
	switch {
	case rec != nil && rec.User != in.User:
		return nil, fmt.Errorf("this host's admission record for %s names the user %s, not %s. Evict deletes the deployment's own user and no other. Nothing was changed", d.Token(), ui.Visible(rec.User), in.User)
	case rec == nil && in.User != d.Prefix():
		return nil, fmt.Errorf("this host holds no admission record for %s, so evict deletes only %s, the deployment's own default user, and not %s, which may be someone else's. Nothing was changed", d.Token(), d.Prefix(), in.User)
	}
	// The record says where admit put the Docker data, and a --data-dir
	// that says otherwise is refused rather than ignored: one of them is
	// wrong, and the owner decides which.
	dataDir, fromFlag := "", false
	switch {
	case rec != nil && in.DataDir != "" && (rec.DataDir == "" || path.Dir(rec.DataDir) != in.DataDir):
		where := "under the user's home"
		if rec.DataDir != "" {
			where = "in " + ui.Visible(rec.DataDir)
		}
		return nil, fmt.Errorf("--data-dir %s is not where this deployment's admission record says admit put its Docker data (%s). Drop --data-dir: the record answers it. Nothing was changed", ui.Visible(in.DataDir), where)
	case rec != nil:
		dataDir = rec.DataDir
	case in.DataDir != "":
		if !plainDir(in.DataDir) {
			return nil, fmt.Errorf("--data-dir must be an absolute path in its plain form, as admit was given it; %q is not. Nothing was changed", ui.Visible(in.DataDir))
		}
		dataDir, fromFlag = in.DataDir+"/"+d.Prefix(), true
	}
	ain := Input{Request: admission.Request{Deployment: in.Deployment, User: in.User}}
	if dataDir != "" {
		ain.Flags.DataDir = path.Dir(dataDir)
		if !plainDir(ain.Flags.DataDir) {
			return nil, fmt.Errorf("this deployment's admission record names data_dir %s, which admit never gives. Nothing was changed", ui.Visible(dataDir))
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
		switch {
		case u.UID == 0:
			privileged = append(privileged, "uid 0")
		case u.UID < uidMin:
			privileged = append(privileged, fmt.Sprintf("uid %d, a system account's", u.UID))
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
	e := &evictor{rec: rec, dataDir: dataDir, fromFlag: fromFlag, planner: &planner{t: t, in: ain, d: d, f: f, profile: profile, host: host,
		plan: &Plan{Deployment: in.Deployment, User: in.User, Profile: profile.ID() + " " + profile.Version(), Host: t.Describe()}}}
	if e.others, e.unreadable, err = others(e.planner); err != nil {
		return nil, err
	}
	if e.recordless, err = claimedWithoutRecord(e); err != nil {
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
// On a data site that stops Patroni, which Docker kills if it outlasts its
// stop timeout, with the watchdog still armed.
func (e *evictor) manager() error {
	u, user := e.f.User, e.in.Request.User
	if !u.Exists || !u.Linger && !u.ManagerActive {
		return nil
	}
	if e.f.Files[watchdogRulePath(e.d)].Present {
		e.warn("watchdog: stopping "+user+"'s manager stops its Patroni, which holds the watchdog",
			"A Patroni that does not stop within Docker's stop timeout is killed with the watchdog armed, and the host reboots. Have the operators stop the deployment's stacks first, or expect a reboot.")
	}
	e.remove("stop "+user+"'s manager", fmt.Sprintf("expire %s so no login starts it again, disable linger, and stop user@%d.service, which stops its Docker daemon and every container", user, u.UID),
		fmt.Sprintf("usermod --expiredate 1 %s && loginctl disable-linger %s && systemctl stop user@%d.service && { loginctl terminate-user %s 2>/dev/null || true; }", user, user, u.UID, user))
	return nil
}

// processes kills every process of the user and checks that none is
// left, whenever the user exists: one the manager's stop missed, started
// by runuser or cron outside any session, or running while the manager
// was already stopped, would otherwise run on unfenced once the egress
// table goes, and keep userdel from deleting the user. A process is the
// user's by its real, effective or saved uid, which userdel refuses on,
// and by a uid in its subordinate range: a container's process that
// outlived its daemon would otherwise share IDs with whichever user is
// later given the range. The account is expired first, so no login starts
// another. A process that survives SIGKILL fails the step, naming it, and
// nothing after it runs.
func (e *evictor) processes() error {
	if !e.f.User.Exists {
		return nil
	}
	user := e.in.Request.User
	e.remove("kill "+user+"'s processes", "expire "+user+", kill every process of uid "+fmt.Sprint(e.f.User.UID)+" or of its subordinate range and check that none is left, before anything that fences it goes",
		e.killCommand())
	return nil
}

// killCommand expires the user, kills every process of its uid or of its
// subordinate range, by real, effective or saved uid, and fails, naming
// them, if any is left after ten tries.
func (e *evictor) killCommand() string {
	uid := fmt.Sprint(e.f.User.UID)
	cond := []string{"$2 == " + uid + " || $3 == " + uid + " || $4 == " + uid}
	for _, r := range e.f.SubUID[e.in.Request.User] {
		for _, field := range []string{"$2", "$3", "$4"} {
			cond = append(cond, fmt.Sprintf("%s >= %d && %s < %d", field, r.Start, field, r.Start+r.Count))
		}
	}
	return "usermod --expiredate 1 " + e.in.Request.User + " && { procs() { ps -eo pid=,ruid=,euid=,suid= | awk '" + strings.Join(cond, " || ") + " { print $1 }'; }; " +
		"n=0; while [ -n \"$(procs)\" ] && [ $n -lt 10 ]; do procs | xargs -r kill -KILL 2>/dev/null; n=$((n+1)); sleep 0.5; done; " +
		"[ -z \"$(procs)\" ] || { echo 'processes of uid " + uid + " or its subordinate range survived SIGKILL:'; procs | xargs -r ps -o pid=,uid=,args= -p; false; }; }"
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
		// A user deleted by hand leaves the drop-in, whose path holds a UID
		// evict cannot read; the probe found it by the deployment it names.
		for _, dropIn := range e.f.OrphanDropIns {
			e.remove("remove "+dropIn, "the drop-in that started the deleted user's manager after the mesh, and its directory once empty",
				"rm -f "+dropIn+" && rmdir --ignore-fail-on-non-empty "+path.Dir(dropIn)+" && systemctl daemon-reload")
		}
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
// /etc/subuid and /etc/subgid that userdel left. userdel --remove deletes
// whatever home passwd names, so the home is checked as admit checks it:
// a real directory of the user's, or already gone, when the user alone is
// deleted. The processes are killed and checked again first, since the
// data directories' removal before it can take minutes.
func (e *evictor) user() error {
	u, user := e.f.User, e.in.Request.User
	if u.Exists {
		switch {
		case u.HomeKind == "absent":
			e.remove("delete user "+user, "delete "+user+"; its home, "+ui.Visible(u.HomePath)+", is gone already", e.killCommand()+" && userdel "+user)
		default:
			if err := ownHome(user, u); err != nil {
				return err
			}
			e.remove("delete user "+user, "delete "+user+" with its home, "+ui.Visible(u.Home), e.killCommand()+" && userdel --remove "+user)
		}
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
	if e.dataDir != "" {
		if err := e.checkDataDir(); err != nil {
			return err
		}
		dirs = append(dirs, [2]string{e.dataDir, "the deployment's Docker data, on the disk --data-dir named"})
	} else if e.rec == nil && (e.f.User.Exists || len(e.f.SubUID[e.in.Request.User]) > 0) {
		e.warn("data: without an admission record, a data directory outside "+e.in.Request.User+"'s home cannot be found",
			"If admit was run with --data-dir, run host evict again with the same --data-dir, so its "+e.d.Prefix()+" directory is removed. Otherwise those files stay, owned by IDs in the subordinate range evict frees, and map onto whichever user is later given that range.")
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

// checkDataDir refuses a --data-dir directory that, with no record to say
// it is the deployment's, is not plainly the user's Docker data: a real
// directory owned by the user or by an ID in its subordinate range.
func (e *evictor) checkDataDir() error {
	s := e.f.Dirs[e.dataDir]
	if !e.fromFlag || !s.Present {
		return nil
	}
	u, user := e.f.User, e.in.Request.User
	owned := u.Exists && s.UID == u.UID
	for _, r := range e.f.SubUID[user] {
		owned = owned || u.Exists && int64(s.UID) >= r.Start && int64(s.UID) < r.Start+r.Count
	}
	switch {
	case !strings.HasPrefix(s.Mode, "d"):
		return fmt.Errorf("%s is %q, not a directory, and evict removes only the directory admit made there. Look at what it is, remove it yourself if it is the deployment's, and run host evict again. Nothing was changed", ui.Visible(e.dataDir), ui.Visible(s.Mode))
	case !owned:
		return fmt.Errorf("%s is owned by uid %d, which is neither %s's nor in its subordinate range, so with no admission record evict cannot tell it is this deployment's Docker data. Look at it, remove it yourself if it is, and run host evict again without --data-dir. Nothing was changed", ui.Visible(e.dataDir), s.UID, user)
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
		if w.KeptSSH != "" {
			e.warn("firewall: the deployment's SSH allow "+w.KeptSSH+" is kept",
				"It may be the rule your own login arrives by. Delete it yourself once nothing else needs it.")
		}
	}
	return nil
}

// shared reports whether another deployment is admitted here, readable or
// not, or claims the host with no record.
func (e *evictor) shared() bool { return len(e.others)+len(e.unreadable)+len(e.recordless) > 0 }

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
		for _, token := range e.recordless {
			e.present("host wide: delegate.conf and br_netfilter kept, since deployment " + token + " is claimed here and has no admission record")
		}
		return nil
	}
	switch f := e.f.Files[delegatePath]; {
	case f.Sum == sha(delegateConf):
		e.remove("remove "+delegatePath, "no other deployment is admitted", "rm -f "+delegatePath+" && systemctl daemon-reload")
	case f.Present:
		e.present("host wide: " + delegatePath + " kept: it is not as admit wrote it")
	}
	switch sum := e.f.Files[modulesLoadPath].Sum; {
	case sum != "" && slices.ContainsFunc([]string{"nf_tables", "ip_tables"}, func(m string) bool {
		g := *e.planner
		g.f.IptablesModule = m
		return sum == sha(modulesLoadConf(&g))
	}):
		e.remove("remove "+modulesLoadPath, "no other deployment is admitted; br_netfilter stays loaded until the next boot", "rm -f "+modulesLoadPath)
	case e.f.Files[modulesLoadPath].Present:
		e.present("host wide: " + modulesLoadPath + " kept: it is not as admit wrote it")
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
	keep := len(e.unreadable)+len(e.recordless) > 0
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

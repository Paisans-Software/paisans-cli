package hostadmit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// PrivilegedGroups are the groups a deployment's user must not be in: each
// reaches past the user, to root (sudo, wheel, admin, lxd), to every user's
// logs (adm, systemd-journal), or to the owner's system Docker daemon,
// which is root on the host (docker).
//
// Each is root-equivalent or reads root's secrets: disk writes the raw
// block devices, kmem reads the kernel's memory, shadow reads every
// password hash, and libvirt, incus-admin and lxd start machines and
// containers as root with the host's disks in reach.
var PrivilegedGroups = []string{"root", "sudo", "wheel", "admin", "adm", "docker", "lxd", "systemd-journal",
	"disk", "shadow", "kmem", "libvirt", "incus-admin"}

// minSubIDs is the subordinate range a rootless daemon needs (Docker's
// rootless page: "at least 65,536").
const minSubIDs = 65536

// subIDMin is the lowest ID a subordinate range may hold, Ubuntu's
// SUB_UID_MIN and SUB_GID_MIN: below it are root, the system accounts,
// nobody (65534) and the host's own users.
const subIDMin = 100000

// asUser is command run as the deployment's user, with an environment of
// its own and nothing of root's: its home, its runtime directory and bus,
// and its own Docker socket, so a DOCKER_HOST root has set never reaches
// it. Everything admit reads or writes in the user's home goes through it,
// so a link the user planted cannot redirect root.
func asUser(p *planner, command string) string {
	home := p.f.User.Home
	if home == "" {
		home = "/home/" + p.in.Request.User
	}
	uid := uidOf(p)
	return "runuser -u " + p.in.Request.User + " -- env -i HOME=" + apply.ShellQuote(home) +
		" PATH=/usr/bin:/bin:/usr/sbin:/sbin XDG_RUNTIME_DIR=/run/user/" + uid +
		" DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + uid + "/bus" +
		" DOCKER_HOST=unix:///run/user/" + uid + "/docker.sock sh -c " + apply.ShellQuote(command)
}

// planUser creates the user, or checks the one that exists: never root,
// in no privileged group, with one subordinate range of each kind, at least
// 65,536 long and overlapping no other user's, since the egress table
// trusts the whole range as the user's. Then it orders the user's manager
// after the mesh.
func planUser(p *planner) error {
	req := p.in.Request
	if prob := config.DeploymentUserProblem(req.User, req.Deployment); prob != "" {
		return fmt.Errorf("user %s", prob)
	}
	u := p.f.User
	if !u.Exists {
		if u.HomeKind != "absent" {
			return homeExists(req.User, u)
		}
		p.step(Step{
			Title:    "create user " + req.User,
			Describe: "user: create " + req.User + " with a home, whose subordinate ID ranges useradd allocates",
			Command:  "useradd --create-home --shell /bin/bash " + req.User,
			Kinds:    []string{"user"},
		})
		return planMeshDropIn(p)
	}
	if u.UID == 0 {
		return fmt.Errorf("%s has uid 0, which is root, and a deployment's user is never root. Nothing was changed", req.User)
	}
	var privileged []string
	for _, g := range append([]string{u.Group}, u.Groups...) {
		if slices.Contains(PrivilegedGroups, g) && !slices.Contains(privileged, g) {
			privileged = append(privileged, g)
		}
	}
	if len(privileged) > 0 {
		return fmt.Errorf("%s is in %s, which reaches past the user (docker is your system daemon, which is root; sudo, wheel, admin and lxd reach root; adm and systemd-journal read every user's logs). Remove it from them (gpasswd -d %s <group>), or name another user in the request, and admit again. Nothing was changed", req.User, strings.Join(privileged, ", "), req.User)
	}
	if err := sudoRules(p); err != nil {
		return err
	}
	if err := ownHome(req.User, u); err != nil {
		return err
	}
	p.present(fmt.Sprintf("user: %s exists, uid %d, in no privileged group, and sudo grants it nothing beyond admit's helper", req.User, u.UID))
	if req.User != p.d.Prefix() {
		p.warn("user: "+req.User+" already exists and is not "+p.d.Prefix(),
			"admit adds the operators' keys to "+req.User+"'s authorized_keys and runs the deployment as "+req.User+": check that this account is meant for the deployment and nothing else.")
	}
	for _, kind := range []struct {
		name   string
		ranges map[string][]IDRange
		flag   string
	}{{"subuid", p.f.SubUID, "--add-subuids"}, {"subgid", p.f.SubGID, "--add-subgids"}} {
		mine := kind.ranges[req.User]
		free := freeRange(kind.ranges)
		fix := fmt.Sprintf("usermod %s %d-%d %s", kind.flag, free, free+minSubIDs-1, req.User)
		switch {
		case len(mine) == 0:
			return fmt.Errorf("%s has no range in /etc/%s, and its rootless daemon needs one of at least %d. Give it one (%s, a range no other user holds) and admit again. Nothing was changed", req.User, kind.name, minSubIDs, fix)
		case len(mine) > 1 || mine[0].Count < minSubIDs:
			return fmt.Errorf("%s needs one range of at least %d in /etc/%s, and has %s. Remove its lines there and give it one (%s), then admit again. Nothing was changed", req.User, minSubIDs, kind.name, spans(mine), fix)
		}
		if bad := misplaced(mine[0], u); bad != "" {
			return fmt.Errorf("%s's range in /etc/%s, %s, %s, so a container's IDs would map onto real ones. Remove its line there and give it one of its own (%s), then admit again. Nothing was changed", req.User, kind.name, spans(mine), bad, fix)
		}
		for _, name := range sortedKeys(kind.ranges) {
			for _, r := range kind.ranges[name] {
				if name != req.User && r.Start < mine[0].Start+mine[0].Count && mine[0].Start < r.Start+r.Count {
					other := ui.Visible(name)
					return fmt.Errorf("%s's range in /etc/%s overlaps %s's (%s), so the egress table, which trusts the whole range as %s's, would trust %s's processes too. Give %s a range of its own (%s) and admit again. Nothing was changed", req.User, kind.name, other, spans([]IDRange{r}), req.User, other, req.User, fix)
				}
			}
		}
		p.present(fmt.Sprintf("user: %s holds %s in /etc/%s", req.User, spans(mine), kind.name))
	}
	return planMeshDropIn(p)
}

// homeExists refuses a home already there for a user admit is about to
// create: useradd takes it over with only a warning, and with it whatever
// an earlier user left in it, its keys and its Docker data; a link there
// makes the new user's home wherever it points.
func homeExists(user string, u UserFacts) error {
	if u.HomeKind == "" {
		return fmt.Errorf("the home useradd would give %s could not be read, so admit cannot tell whether an earlier user left one there. Nothing was changed", user)
	}
	what := map[string]string{"dir": fmt.Sprintf("a directory of uid %d", u.HomeUID), "link": "a link", "other": "a file"}[u.HomeKind]
	return fmt.Errorf("%s already exists (%s), and useradd would hand it to %s with whatever is in it: an earlier user's keys and data, or, through a link, wherever it points. Move it aside or remove it, then admit again. Nothing was changed", ui.Visible(u.HomePath), what, user)
}

// ownHome refuses an existing user whose home is not a directory of its
// own: admit writes there as the user, and a link or another's directory
// would put the operators' keys and the daemon's data somewhere else.
func ownHome(user string, u UserFacts) error {
	home := ui.Visible(u.HomePath)
	switch {
	case u.HomeKind == "":
		return fmt.Errorf("%s's home could not be read. Nothing was changed", user)
	case u.HomeKind == "link":
		return fmt.Errorf("%s's home %s is a link, so what admit writes there as the user lands wherever it points. Make it a real directory of %s's and admit again. Nothing was changed", user, home, user)
	case u.HomeKind != "dir":
		return fmt.Errorf("%s's home %s is not a directory. Make it a directory of %s's and admit again. Nothing was changed", user, home, user)
	case u.HomeUID != u.UID:
		return fmt.Errorf("%s's home %s is not %s's (it is uid %d's), so another account can change what admit writes there. Give it to %s (chown -R %s: %s) and admit again. Nothing was changed", user, home, user, u.HomeUID, user, user, home)
	}
	return nil
}

// sudoRules refuses a user sudo grants any rule to but admit's own line
// for the WireGuard helper, which a re-run finds: a rule by name, or by a
// group PrivilegedGroups does not list, reaches past the user as surely as
// sudo's own group does. It refuses too a Defaults entry that lets the
// user hand the helper an environment or another root (sudoDefaultProblem),
// and any entry set for a runas user or a command alone: sudo -l lists
// only admit's line while setenv lets the user pass LD_PRELOAD to the
// helper, which runs as root.
func sudoRules(p *planner) error {
	user := p.in.Request.User
	u := p.f.User
	if !u.SudoRead {
		return fmt.Errorf("sudo's rules for %s could not be read (sudo -l -U %s), so admit cannot tell what they grant. Nothing was changed", user, user)
	}
	helper, _ := apply.WGHelper{Iface: p.d.Interface()}.Path()
	var rules, defaults []string
	for _, r := range u.SudoRules {
		if r != "(root) NOPASSWD: "+helper {
			rules = append(rules, "- "+ui.Visible(r))
		}
	}
	for _, d := range u.SudoDefaults {
		if why := sudoDefaultProblem(d); why != "" {
			defaults = append(defaults, "- "+ui.Visible(d)+": "+why)
		}
	}
	for _, d := range u.SudoSpecific {
		defaults = append(defaults, "- "+ui.Visible(d)+": set for one runas user or command alone")
	}
	switch {
	case len(rules) > 0:
		return fmt.Errorf("sudo grants %s a rule beyond admit's own, which reaches past the user:\n%s\nRemove them (sudo -l -U %s names them; look in /etc/sudoers and /etc/sudoers.d), or name another user in the request, and admit again. Nothing was changed", user, strings.Join(rules, "\n"), user)
	case len(defaults) > 0:
		return fmt.Errorf("sudo's Defaults for %s would let it pass its own environment, or another root, to admit's helper, which runs as root:\n%s\nRemove them (sudo -l -U %s names them; look in /etc/sudoers and /etc/sudoers.d), or name another user in the request, and admit again. Nothing was changed", user, strings.Join(defaults, "\n"), user)
	}
	return nil
}

// stockSecurePath is the directories of Ubuntu's secure_path, each
// root's alone; a secure_path of any of them passes.
var stockSecurePath = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin", "/snap/bin"}

// sudoDefaultProblem says why one entry of the Defaults sudo applies to
// the user lets it reach past the helper's fixed environment, or "":
// keeping or setting variables (env_keep, env_check, env_file, setenv,
// restricted_env_file, !env_reset), a PATH of its choosing (!secure_path,
// a secure_path outside root's directories, exempt_group), a root
// directory or working directory of its choosing (runchroot, runcwd), or
// open descriptors (closefrom_override). Stock Ubuntu's env_reset,
// mail_badpass, secure_path and use_pty pass, as does anything else.
func sudoDefaultProblem(entry string) string {
	name := strings.TrimPrefix(entry, "!")
	value := ""
	if i := strings.IndexAny(name, "+-="); i >= 0 {
		name, value = name[:i], strings.TrimLeft(name[i:], "+-=")
	}
	negated := strings.HasPrefix(entry, "!")
	switch name {
	case "env_reset":
		if negated {
			return "the user's whole environment reaches the helper"
		}
	case "env_keep", "env_check", "env_file", "setenv", "restricted_env_file", "env_delete":
		return "the user's variables reach the helper"
	case "secure_path":
		if negated {
			return "the user's PATH reaches the helper"
		}
		for _, d := range strings.Split(strings.ReplaceAll(value, `\:`, ":"), ":") {
			if !slices.Contains(stockSecurePath, d) {
				return "the helper's PATH holds " + ui.Visible(d) + ", which is not root's alone"
			}
		}
	case "exempt_group":
		return "its members' PATH reaches the helper"
	case "runchroot", "runcwd":
		return "the user chooses the helper's root or working directory"
	case "closefrom_override":
		return "the user's open files reach the helper"
	}
	return ""
}

// misplaced says why r cannot be a deployment's subordinate range, or "":
// below subIDMin it holds root, nobody or a real user, and it must never
// hold the user's own IDs.
func misplaced(r IDRange, u UserFacts) string {
	holds := func(id int64) bool { return r.Start <= id && id < r.Start+r.Count }
	var why []string
	if r.Start < subIDMin {
		why = append(why, fmt.Sprintf("starts below %d", subIDMin))
	}
	if holds(0) {
		why = append(why, "holds 0, which is root")
	}
	if holds(65534) {
		why = append(why, "holds 65534, which is nobody")
	}
	for _, id := range []int{u.UID, u.GID} {
		if holds(int64(id)) {
			why = append(why, fmt.Sprintf("holds %s's own ID %d", u.Name, id))
			break
		}
	}
	return strings.Join(why, "; ")
}

// spans is ranges as first-last, comma separated.
func spans(ranges []IDRange) string {
	var out []string
	for _, r := range ranges {
		out = append(out, fmt.Sprintf("%d-%d", r.Start, r.Start+r.Count-1))
	}
	return strings.Join(out, ", ")
}

// freeRange is the first ID above every range listed, for a fix to name.
func freeRange(ranges map[string][]IDRange) int64 {
	next := int64(100000)
	for _, rs := range ranges {
		for _, r := range rs {
			if end := r.Start + r.Count; end > next {
				next = end
			}
		}
	}
	return next
}

// meshConf is the user's drop-in that starts its manager after the
// deployment's mesh at boot: an app container that publishes on the mesh
// address otherwise loses the race to wg-quick, and Docker does not retry
// it. It is the deployment's own, and evict removes it.
func meshConf(p *planner) string {
	unit := "wg-quick@" + p.d.Interface() + ".service"
	return "# Written by paisans host admit for deployment " + p.d.Token() + "; paisans host evict\n" +
		"# removes it. The user's manager, its Docker daemon and its containers\n" +
		"# start after the mesh at boot.\n" +
		"[Unit]\nWants=" + unit + "\nAfter=" + unit + "\n"
}

// ours reports whether f is content, as admit writes it: root's, mode 644.
func ours(f FileState, content string) bool {
	return f.Present && !f.Link && f.Sum == sha(content) && f.Mode == "644" && f.UID == 0 && f.GID == 0
}

// planMeshDropIn writes the user's mesh drop-in and reloads systemd. It
// never restarts the user's manager: the drop-in orders its next start,
// which for a manager running now is the host's next boot.
func planMeshDropIn(p *planner) error {
	path, content := meshDropInPath(uidOf(p)), meshConf(p)
	f := p.f.Files[path]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", ui.Visible(path))
	case ours(f, content):
		p.present("user: " + path + " starts " + p.in.Request.User + "'s manager after the mesh")
		return nil
	}
	p.step(Step{
		Title:       "start " + p.in.Request.User + "'s manager after the mesh",
		Describe:    "user: order user@" + uidOf(p) + ".service after wg-quick@" + p.d.Interface() + " at boot, so a container that publishes on the mesh address finds it; a manager running now is not restarted, and takes it at its next start",
		File:        &hostprep.File{Path: path, Content: content, Mode: 0o644},
		Command:     "systemctl daemon-reload",
		Kinds:       []string{"user"},
		Provisional: !p.f.User.Exists,
	})
	return nil
}

// delegateConf is the drop-in that lets every user's manager hand cpuset
// and io, besides what Ubuntu delegates already, to the containers below
// it (spec, Observed R5). It is host wide, shared by every admitted
// deployment.
const delegateConf = "# Written by paisans host admit. Shared by every admitted deployment;\n" +
	"# paisans host evict removes it with the last of them.\n" +
	"[Service]\nDelegate=cpu cpuset io memory pids\n"

// planDelegation writes the drop-in before the user's manager first starts.
// A manager already running gets it at its next start: restarting it would
// restart the user's daemon and every container in it, so admit never does.
// A drop-in with admit's content and another mode or owner is written
// again; one with other content is the host's own and is left as it is.
func planDelegation(p *planner) error {
	f := p.f.Files[delegatePath]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", delegatePath)
	case ours(f, delegateConf):
		p.present("delegation: " + delegatePath + " delegates cpu cpuset io memory pids")
		return nil
	case f.Present && f.Sum != sha(delegateConf):
		p.plan.Foreign = append(p.plan.Foreign, "delegation: "+delegatePath)
		p.warn("delegation: "+delegatePath+" is not paisans', and is left as it is",
			"It is the host's own drop-in for every user's manager. Compose cpuset and io limits work only if it delegates cpuset and io.")
		return nil
	}
	when := "written before " + p.in.Request.User + "'s manager first starts"
	if p.f.User.ManagerActive {
		when = p.in.Request.User + "'s manager is running, and takes it at its next start"
	}
	p.step(Step{
		Title:    "delegate cgroups to users",
		Describe: "delegation: let every user's manager delegate cpu cpuset io memory pids, for compose's limits; " + when,
		File:     &hostprep.File{Path: delegatePath, Content: delegateConf, Mode: 0o644},
		Command:  "systemctl daemon-reload",
		Kinds:    []string{"user"},
	})
	if p.f.User.ManagerActive {
		p.warn("delegation reaches "+p.in.Request.User+"'s manager at its next start",
			"Its manager is running, and restarting it would restart its Docker daemon and every container in it, so admit does not. cpuset and io limits apply after the host's next reboot.")
	}
	return nil
}

// planLinger enables linger, so the user's manager, its daemon and its
// timers run with no login and start at boot, then starts the manager if
// it is not running. start never restarts a running unit.
func planLinger(p *planner) error {
	u, user := p.f.User, p.in.Request.User
	if u.Exists && u.Linger {
		p.present("linger: on for " + user)
	} else {
		p.step(Step{Title: "enable linger for " + user, Describe: "linger: run " + user + "'s manager, its Docker daemon and its timers without a login, and at boot", Command: "loginctl enable-linger " + user, Kinds: []string{"user"}})
	}
	if u.Exists && u.ManagerActive {
		p.present("linger: " + user + "'s manager is running")
		return nil
	}
	p.step(Step{
		Title:       "start " + user + "'s manager",
		Describe:    "linger: start user@" + uidOf(p) + ".service and wait for it, which does nothing to a manager already running",
		Command:     "systemctl start user@" + uidOf(p) + ".service",
		Kinds:       []string{"user"},
		Provisional: !u.Exists,
	})
	return nil
}

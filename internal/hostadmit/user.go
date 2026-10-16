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
var PrivilegedGroups = []string{"root", "sudo", "wheel", "admin", "adm", "docker", "lxd", "systemd-journal"}

// minSubIDs is the subordinate range a rootless daemon needs (Docker's
// rootless page: "at least 65,536").
const minSubIDs = 65536

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
	p.present(fmt.Sprintf("user: %s exists, uid %d, in no privileged group", req.User, u.UID))
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

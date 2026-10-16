package hostadmit

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// planDeploymentDir creates /srv/paisans, root's and 755, and the
// deployment's directory in it, the user's and 750. A deployment directory
// that exists and is not the user's is an earlier deployment run as root,
// which is redeployed, not converted.
func planDeploymentDir(p *planner) error {
	base, root, user := p.f.Dirs[deployment.Base], p.f.Dirs[p.d.Root()], p.in.Request.User
	switch {
	case !base.Present:
		p.step(Step{Title: "create " + deployment.Base, Describe: "directory: every deployment's directory lives in it, root's, mode 755", Command: "install -d -m 755 -o root -g root " + deployment.Base, Kinds: []string{"directory"}})
	case !rootsAlone(base):
		return fmt.Errorf("%s is %q, owner %d: it must be a directory owned by root that only root may write. Fix it and admit again. Nothing was changed", deployment.Base, base.Mode, base.UID)
	}
	switch {
	case !root.Present:
		p.step(Step{Title: "create " + p.d.Root(), Describe: "directory: the deployment's own, " + user + "'s, mode 750",
			Command: "install -d -m 750 -o " + user + " -g " + groupOf(p) + " " + p.d.Root(), Kinds: []string{"directory"}, Provisional: !p.f.User.Exists})
	case !strings.HasPrefix(root.Mode, "d") || !p.f.User.Exists || root.UID != p.f.User.UID:
		return fmt.Errorf("%s exists and is not a directory of %s's (%q, owner %d). It is an earlier deployment run as root, which is redeployed rather than taken over: remove it with paisans site remove, or by hand once nothing in it is wanted, and admit again. Nothing was changed", p.d.Root(), ui.Visible(user), root.Mode, root.UID)
	default:
		p.present("directory: " + p.d.Root() + " is " + user + "'s")
	}
	return nil
}

// planMeshSubnet refuses a mesh subnet that overlaps a route, an address,
// a Docker network or a Docker pool on the host, read as root: a request
// cannot know them, and two networks on overlapping ranges route each
// other's traffic. It is the check host prepare runs, on the same probe.
func planMeshSubnet(p *planner) error {
	subnet, err := mesh.ParsePrefix(p.in.Request.MeshSubnet)
	if err != nil {
		return err
	}
	probed, err := mesh.Probe(p.t)
	if err != nil {
		return shown(err)
	}
	taken, err := probed.Taken(p.d.Interface(), "")
	if err != nil {
		return shown(err)
	}
	if clash := mesh.Clashes(subnet, taken); len(clash) > 0 {
		var what []string
		for _, c := range clash {
			what = append(what, ui.Visible(c.What))
		}
		return fmt.Errorf("the deployment's mesh subnet %s overlaps %s on this host, so its mesh would route that network's traffic. The operators pick another subnet (paisans init picks one clear of every host) and send a new request. Nothing was changed", p.in.Request.MeshSubnet, strings.Join(what, ", "))
	}
	p.present("mesh: nothing on this host overlaps " + p.in.Request.MeshSubnet)
	return nil
}

// sudoersLine is the one line that lets the deployment's user run its
// WireGuard helper, and nothing else, as root, with no password.
func sudoersLine(d deployment.Deployment, user string) (string, error) {
	helper, err := apply.WGHelper{Iface: d.Interface()}.Path()
	if err != nil {
		return "", err
	}
	return user + " ALL=(root) NOPASSWD: " + helper + "\n", nil
}

// planWireGuard creates /etc/wireguard root's and 700 when it is absent and
// refuses one that is not root's alone, since a conf planted while another
// could write there would outlive any repair. It installs the helper and
// its sudoers line, and restores either when it finds it changed.
func planWireGuard(p *planner) error {
	const wg = "/etc/wireguard"
	dir := p.f.Dirs[wg]
	switch {
	case !dir.Present:
		p.step(Step{Title: "create " + wg, Describe: "wireguard: root's, mode 700, so the deployment's user cannot plant a conf the helper has not checked", Command: "install -d -m 700 -o root -g root " + wg, Kinds: []string{"wireguard"}})
	case len(dir.Mode) != 10 || dir.Mode[0] != 'd':
		what := "is not a directory"
		switch {
		case strings.HasPrefix(dir.Mode, "l"):
			what = "is a symbolic link"
		case strings.HasPrefix(dir.Mode, "d") && strings.HasSuffix(dir.Mode, "+"):
			what = "carries an ACL (" + ui.Visible(dir.Mode) + ")"
		case strings.HasPrefix(dir.Mode, "d"):
			what = "has a mode admit cannot read (" + ui.Visible(dir.Mode) + ")"
		}
		return fmt.Errorf("%s %s, and admit cannot make it a directory only root may write. A conf the deployment's user could plant there would run its PostUp as root at the next up. Fix it yourself (remove the link, or the ACL with setfacl -b) and admit again. Nothing was changed", wg, what)
	case !rootsAlone(dir):
		return fmt.Errorf("%s is %s, owner %d, group %d: someone other than root owns it or may write it, so a conf planted there while it was so would survive any repair admit made, and wg-quick runs a conf's PostUp as root. Make it root's with no group or other write bit (chown root:root %s && chmod go-w %s), check that every file in it is one you put there, and admit again. Nothing was changed", wg, ui.Visible(dir.Mode), dir.UID, dir.GID, wg, wg)
	default:
		p.present("wireguard: " + wg + " is root's alone")
	}

	libexec := p.f.Dirs[apply.HelperDir]
	switch {
	case !libexec.Present:
		p.step(Step{Title: "create " + apply.HelperDir, Describe: "wireguard: where each deployment's helper lives, root's, mode 755", Command: "install -d -m 755 -o root -g root " + apply.HelperDir, Kinds: []string{"wireguard"}})
	case !rootsAlone(libexec):
		return fmt.Errorf("%s is %q, owner %d: it must be a directory owned by root that only root may write. Fix it and admit again. Nothing was changed", apply.HelperDir, libexec.Mode, libexec.UID)
	}
	helper, err := apply.WGHelper{Iface: p.d.Interface()}.Path()
	if err != nil {
		return err
	}
	script, err := hostprep.WGHelperScript(p.d, p.in.Request.ListenPort, p.in.Request.MeshSubnet)
	if err != nil {
		return err
	}
	if err := ensureRootFile(p, helper, script, 0o755, "install the WireGuard helper", "wireguard: the one command "+p.in.Request.User+" may run as root: it checks each conf before wg-quick sees it, and adds the mesh's mark"); err != nil {
		return err
	}
	line, err := sudoersLine(p.d, p.in.Request.User)
	if err != nil {
		return err
	}
	final := sudoersPath(p.d)
	if f := p.f.Files[final]; f.Present && !f.Link && f.Sum == sha(line) && f.Mode == "440" && f.UID == 0 {
		p.present("wireguard: " + final + " lets " + p.in.Request.User + " run the helper alone")
		return nil
	}
	if p.f.Files[final].Link {
		return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", final)
	}
	tmp := final + ".new"
	title := "install the helper's sudoers line"
	if p.f.Files[final].Present {
		title = "restore the helper's sudoers line"
	}
	p.step(Step{Title: title, Describe: "wireguard: " + final + ", mode 440, checked by visudo under a name sudo skips before it is moved into place",
		File:    &hostprep.File{Path: tmp, Content: line, Mode: 0o440},
		Command: "visudo -cqf " + tmp + " && chown root:root " + tmp + " && mv -f " + tmp + " " + final + " || { rm -f " + tmp + "; echo 'visudo refused the line; nothing reached sudo'; exit 1; }",
		Kinds:   []string{"wireguard"}})
	return nil
}

// ensureRootFile plans writing content at path, root's, with mode, unless
// it is there as written; a link is refused, and a changed file restored.
func ensureRootFile(p *planner, path, content string, mode uint32, title, describe string) error {
	f := p.f.Files[path]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", path)
	case f.Present && f.Sum == sha(content) && f.Mode == fmt.Sprintf("%o", mode) && f.UID == 0:
		p.present(describe)
		return nil
	case f.Present:
		var changed []string
		if f.Sum != sha(content) {
			changed = append(changed, "its content")
		}
		if want := fmt.Sprintf("%o", mode); f.Mode != want {
			changed = append(changed, fmt.Sprintf("mode %s, not %s", ui.Visible(f.Mode), want))
		}
		if f.UID != 0 {
			changed = append(changed, fmt.Sprintf("owner %d, not root", f.UID))
		}
		title = "restore: " + title
		describe += " (found changed: " + strings.Join(changed, "; ") + ")"
	}
	p.step(Step{Title: title, Describe: describe, File: &hostprep.File{Path: path, Content: content, Mode: mode}, Kinds: []string{"wireguard"}})
	return nil
}

package hostadmit

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// modulesLoadConf loads br_netfilter at every boot, without which the
// user's daemon will not start with its userland proxy off (spec, Observed
// R6), and the module the host's iptables uses, without which
// dockerd-rootless-setuptool.sh refuses to install it.
func modulesLoadConf(p *planner) string {
	return "# Written by paisans host admit. The rootless Docker daemons of admitted\n" +
		"# deployments need br_netfilter to start with their userland proxy off,\n" +
		"# and the module their iptables uses for the setup tool to install them.\n" +
		"br_netfilter\n" + iptablesModule(p) + "\n"
}

// iptablesModule is the module the host's iptables uses: ip_tables for
// legacy iptables, nf_tables otherwise.
func iptablesModule(p *planner) string {
	if p.f.IptablesModule == "ip_tables" {
		return "ip_tables"
	}
	return "nf_tables"
}

// dockerBridgeName is a bridge a Docker daemon made: docker0, or a
// network's br-<12 hex>, as Docker names them.
var dockerBridgeName = regexp.MustCompile(`^(docker0|br-[0-9a-f]{12})$`)

// planBrNetfilter loads br_netfilter, which is host wide: once it is
// loaded, traffic crossing any of the host's bridges passes the FORWARD
// chain, which it skipped before. Already loaded, nothing changes in the
// running kernel; not loaded with only Docker's bridges, it is loaded; not
// loaded with other bridges, admit names them and the chain's policy and
// stops unless the owner passed --bridge-netfilter. The boot entry is
// written either way: a system Docker that loads the module may start
// after the user's daemon at boot.
func planBrNetfilter(p *planner) error {
	p.record.BridgeNetfilter = p.in.Flags.BridgeNetfilter
	var others []string
	for _, b := range p.f.Bridges {
		if !dockerBridgeName.MatchString(b) {
			others = append(others, ui.Visible(b))
		}
	}
	policy := policyOr(p.f.ForwardPolicy)
	switch {
	case p.f.BrNetfilter:
		p.present("br_netfilter: loaded already")
	case len(others) > 0 && !p.in.Flags.BridgeNetfilter:
		return fmt.Errorf("br_netfilter is not loaded, and this host has bridges of its own (%s). The deployment's daemon needs it, and once it is loaded their traffic passes the FORWARD chain, whose policy is %s, which it skips today. Check that the chain lets their traffic through, then admit again with --bridge-netfilter. Nothing was changed", strings.Join(others, ", "), policy)
	default:
		if len(others) > 0 {
			p.warn("br_netfilter: traffic over "+strings.Join(others, ", ")+" passes the FORWARD chain from now on",
				"The chain's policy is "+policy+". --bridge-netfilter was given, and is recorded.")
		}
		p.step(Step{Title: "load br_netfilter", Describe: "daemon: load br_netfilter now, which the user's daemon needs with its userland proxy off",
			Command: "modprobe br_netfilter", Kinds: []string{"docker"}})
	}
	module := iptablesModule(p)
	if p.f.IptablesModuleLoaded {
		p.present("br_netfilter: " + module + ", which the host's iptables uses, is loaded")
	} else {
		p.step(Step{Title: "load " + module, Describe: "daemon: load " + module + " now, which the host's iptables uses and dockerd-rootless-setuptool.sh needs loaded before it installs the user's daemon",
			Command: "modprobe " + module, Kinds: []string{"docker"}})
	}
	f, content := p.f.Files[modulesLoadPath], modulesLoadConf(p)
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", modulesLoadPath)
	case ours(f, content):
		p.present("br_netfilter: br_netfilter and " + module + " loaded at boot by " + modulesLoadPath)
	default:
		p.step(Step{Title: "load br_netfilter at boot", Describe: "daemon: list br_netfilter and " + module + " in " + modulesLoadPath + ", shared by every admitted deployment",
			File: &hostprep.File{Path: modulesLoadPath, Content: content, Mode: 0o644}, Kinds: []string{"docker"}})
	}
	return nil
}

// policyOr is the FORWARD chain's policy as the probe read it, visible.
func policyOr(policy string) string {
	if policy == "" {
		return "not readable"
	}
	return ui.Visible(policy)
}

// UserState is what admit reads as the deployment's user, in its own home.
type UserState struct {
	DaemonActive, UnitPresent bool
	// DaemonJSON is the checksum of ~/.config/docker/daemon.json, "" when
	// there is none.
	DaemonJSON string
	// Counted is the daemon answering, so Containers, Images and Volumes
	// are its own count and Root its DockerRootDir. A daemon that does not
	// answer counts nothing: OldRootHeld then says whether the data root
	// admit last gave it holds anything.
	Counted bool
	Root    string
	// JSONRoot is the data-root the user's daemon.json sets, "" for none:
	// where a daemon that does not answer keeps its data.
	JSONRoot                    string
	Containers, Images, Volumes int
	OldRootHeld                 bool
}

// holdsData is whether the daemon's data root holds anything admit would
// hide by moving it.
func (s UserState) holdsData() bool {
	if s.Counted {
		return s.Containers+s.Images+s.Volumes > 0
	}
	return s.OldRootHeld
}

// userStateProbe runs as the user, so a link in its home is followed with
// the user's rights alone. Its first line carries the marker a test's fake
// host answers it by; old is the data root to look in when the daemon does
// not answer and its daemon.json sets none. data-root is read from the
// line admit writes it on.
const userStateProbe = `# paisans host admit: as the user
old=%s
if systemctl --user is-active -q docker.service 2>/dev/null; then echo "daemon active"; else echo "daemon inactive"; fi
if [ -f "$HOME/.config/systemd/user/docker.service" ]; then echo "unit present"; else echo "unit absent"; fi
if [ -f "$HOME/.config/docker/daemon.json" ]; then
  echo "daemon-json $(sha256sum < "$HOME/.config/docker/daemon.json" | cut -c1-64)"
  set -- "$(sed -n 's/^ *"data-root": *"\([^"]*\)",\{0,1\} *$/\1/p' "$HOME/.config/docker/daemon.json" | head -n 1)"
  if [ -n "$1" ]; then echo "json-root $1"; old=$1; fi
else
  echo "daemon-json absent"
fi
if root=$(docker info --format '{{.DockerRootDir}}' 2>/dev/null) && [ -n "$root" ]; then
  echo "root $root"
  echo "containers $(docker ps -aq | wc -l)"
  echo "images $(docker images -aq | wc -l)"
  echo "volumes $(docker volume ls -q | wc -l)"
else
  echo "containers unknown"; echo "images unknown"; echo "volumes unknown"
  if [ -n "$(ls -A "$old" 2>/dev/null)" ]; then echo "old-root held"; else echo "old-root empty"; fi
fi
echo end`

// userState reads UserState as the user; a user not made yet has none.
func userState(p *planner) (UserState, error) {
	var s UserState
	if !p.f.User.Exists {
		return s, nil
	}
	old, err := p.oldRoot()
	if err != nil {
		return s, err
	}
	user := ui.Visible(p.in.Request.User)
	out, err := p.t.Run(asUser(p, fmt.Sprintf(userStateProbe, apply.ShellQuote(old))))
	if err != nil {
		return s, fmt.Errorf("reading %s's daemon as %s: %w", user, user, shown(err))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[len(lines)-1] != "end" {
		return s, fmt.Errorf("reading %s's daemon: the answer was cut short", user)
	}
	counted := 0
	for _, line := range lines[:len(lines)-1] {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "daemon":
			s.DaemonActive = v == "active"
		case "unit":
			s.UnitPresent = v == "present"
		case "daemon-json":
			if v != "absent" {
				s.DaemonJSON = v
			}
		case "root":
			s.Root = v
		case "json-root":
			s.JSONRoot = v
		case "containers", "images", "volumes":
			if v == "unknown" {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return s, fmt.Errorf("reading %s's daemon: unreadable %s count %q", user, k, ui.Visible(v))
			}
			counted++
			switch k {
			case "containers":
				s.Containers = n
			case "images":
				s.Images = n
			default:
				s.Volumes = n
			}
		case "old-root":
			s.OldRootHeld = v != "empty"
		}
	}
	s.Counted = counted == 3
	return s, nil
}

// previous is this deployment's admission record as the host holds it,
// nil when it has none, read once per Build.
func (p *planner) previous() (*admission.Record, error) {
	if p.prev.read {
		return p.prev.rec, p.prev.err
	}
	p.prev.read = true
	rec, err := admission.Read(p.t, p.d.Token())
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
	case err != nil:
		p.prev.err = fmt.Errorf("this deployment's admission record: %w. Move it aside and admit again", shown(err))
	default:
		p.prev.rec = &rec
	}
	return p.prev.rec, p.prev.err
}

// homeRoot is where rootless Docker keeps its data with no data-root set.
func homeRoot(p *planner) string { return p.f.User.Home + "/.local/share/docker" }

// oldRoot is the data root admit last gave the user's daemon: the record's
// data_dir, or the home's default.
func (p *planner) oldRoot() (string, error) {
	prev, err := p.previous()
	if err != nil {
		return "", err
	}
	if prev != nil && prev.DataDir != "" {
		return prev.DataDir, nil
	}
	return homeRoot(p), nil
}

// daemonConfig is the user's daemon.json, in a fixed field order.
type daemonConfig struct {
	UserlandProxy bool          `json:"userland-proxy"`
	Pools         []addressPool `json:"default-address-pools"`
	DataRoot      string        `json:"data-root,omitempty"`
}

type addressPool struct {
	Base string `json:"base"`
	Size int    `json:"size"`
}

// daemonJSON is the user's daemon.json: the userland proxy off, so a
// published port carries the client's own address (spec, Observed R6);
// Docker's default pools less any overlapping the mesh, each cut in /24s,
// so no network of the user's takes the mesh's range; and the data root
// --data-dir gives.
func daemonJSON(p *planner) (string, error) {
	subnet, err := mesh.ParsePrefix(p.in.Request.MeshSubnet)
	if err != nil {
		return "", err
	}
	cfg := daemonConfig{UserlandProxy: false, DataRoot: p.record.DataDir}
	for _, b := range mesh.DefaultPools {
		pool, err := mesh.ParsePrefix(b)
		if err != nil {
			return "", err
		}
		if !mesh.Overlap(pool, subnet) {
			cfg.Pools = append(cfg.Pools, addressPool{Base: b, Size: 24})
		}
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// refusedFS are filesystems Docker's overlay storage cannot keep its data
// on, as stat -f names them: network filesystems (Docker's rootless tips:
// not on NFS), FUSE, another overlay, and those without the file types
// overlay needs. A type the probe could not read is refused too.
func refusedFS(fs string) bool {
	return fs == "" || strings.HasPrefix(fs, "nfs") || strings.HasPrefix(fs, "fuse") ||
		slices.Contains([]string{"cifs", "smb", "smb2", "smb3", "overlayfs", "overlay", "msdos", "vfat", "exfat", "ntfs", "ntfs3"}, fs)
}

// rootsAlone reports whether a directory, as ls -ldn printed it, is a
// directory and not a link, owned by root, with no group or other write
// bit and no ACL.
func rootsAlone(d DirState) bool {
	m := d.Mode
	return d.Present && !d.Unsafe && len(m) == 10 && m[0] == 'd' && m[5] != 'w' && m[8] != 'w' && d.UID == 0
}

// needsRoom is the free space admit warns below, and why: a data site
// keeps its database and WAL archive under Docker's data root, a Garage
// site its stored objects.
func needsRoom(req admission.Request) (int64, string) {
	switch {
	case slices.Contains(req.Roles, string(config.RoleData)):
		return 20e9, "20 GB for a data site"
	case slices.Contains(req.Roles, string(config.RoleStorage)):
		return 20e9, "20 GB for a Garage site"
	}
	return 5e9, "5 GB"
}

// planDaemon puts the user's rootless daemon in place: the data directory
// with --data-dir, daemon.json, written as the user, then the setup tool,
// run as the user, which starts the daemon reading it. A changed
// daemon.json restarts a daemon only when it runs no container, and the
// data root never moves under data that exists.
func planDaemon(p *planner) error {
	user, flags := p.in.Request.User, p.in.Flags
	if flags.DataDir != "" {
		parent := p.f.Dirs[flags.DataDir]
		if !rootsAlone(parent) {
			return fmt.Errorf("--data-dir %s must be a directory, not a link, owned by root and writable by nobody else (its listing is %q, owner %d), so that no one but root can replace what admit creates in it. Fix it and admit again. Nothing was changed", ui.Visible(flags.DataDir), ui.Visible(parent.Mode), parent.UID)
		}
		p.record.DataDir = flags.DataDir + "/" + p.d.Prefix()
	}
	state, err := userState(p)
	if err != nil {
		return err
	}
	if p.f.User.Exists {
		if err := keepDataRoot(p, state); err != nil {
			return err
		}
	}
	where, fs := ui.Visible(p.f.Data.Path), ui.Visible(p.f.Data.FS)
	if refusedFS(p.f.Data.FS) {
		if fs == "" {
			fs = "a filesystem the probe could not name"
		}
		return fmt.Errorf("Docker's data would live on %s, which is on %s, and Docker's overlay storage cannot keep its data there. Give --data-dir a directory on a local filesystem (ext4, xfs or btrfs) and admit again. Nothing was changed", where, fs)
	}
	floor, why := needsRoom(p.in.Request)
	free := fmt.Sprintf("%.1f GB free", float64(p.f.Data.Avail)/1e9)
	if p.f.Data.Avail < floor {
		p.warn("daemon: "+where+" has "+free+", under "+why,
			"Docker's images, layers and volumes live there, and on a data site its database and WAL archive, on a Garage site its objects. Give --data-dir a larger disk, or free space before the deployment fills it.")
	} else {
		p.present("daemon: " + where + " (" + fs + ") has " + free)
	}
	if err := planDataDir(p); err != nil {
		return err
	}
	content, err := daemonJSON(p)
	if err != nil {
		return err
	}
	changed := state.DaemonJSON != sha(content)
	if changed {
		what := "userland proxy off, address pools clear of the mesh"
		if p.record.DataDir != "" {
			what += ", data root " + ui.Visible(p.record.DataDir)
		}
		p.step(Step{Title: "write " + user + "'s daemon.json", Describe: "daemon: " + what + "; written as " + user,
			Command: asUser(p, `umask 077 && mkdir -p "$HOME/.config/docker" && cat > "$HOME/.config/docker/.daemon.json.paisans-tmp" && mv -f "$HOME/.config/docker/.daemon.json.paisans-tmp" "$HOME/.config/docker/daemon.json"`),
			Input:   content, Kinds: []string{"docker"}, Provisional: !p.f.User.Exists})
	} else {
		p.present("daemon: " + user + "'s daemon.json is as admit writes it")
	}
	switch {
	case !state.UnitPresent:
		p.step(Step{Title: "install " + user + "'s rootless Docker", Describe: "daemon: dockerd-rootless-setuptool.sh install --force, as " + user + ", which starts the daemon reading its daemon.json; --force because a system daemon may run beside it",
			Command: asUser(p, "dockerd-rootless-setuptool.sh install --force"), Kinds: []string{"docker"}, Provisional: !p.f.User.Exists})
	case changed && state.DaemonActive && (!state.Counted || state.Containers > 0):
		runs := fmt.Sprintf("It runs %d %s", state.Containers, plural(state.Containers, "container"))
		if !state.Counted {
			runs = "It is running and did not answer, so admit cannot tell whether it runs containers"
		}
		p.warn("daemon: "+user+"'s daemon picks up its new daemon.json at its next restart",
			runs+", and admit never restarts a daemon under them. The operators restart it, or the host's next reboot does.")
	case changed:
		p.step(Step{Title: "restart " + user + "'s Docker", Describe: "daemon: restart the daemon, which runs no container, to read its new daemon.json",
			Command: asUser(p, "systemctl --user restart docker.service"), Kinds: []string{"docker"}})
	case !state.DaemonActive:
		p.step(Step{Title: "start " + user + "'s Docker", Describe: "daemon: start the daemon, which is installed and stopped",
			Command: asUser(p, "systemctl --user start docker.service"), Kinds: []string{"docker"}})
	default:
		p.present("daemon: " + user + "'s rootless Docker is installed and running")
	}
	return nil
}

// keepDataRoot refuses to move the daemon's data root, by adding,
// changing or dropping --data-dir, while the root it uses now holds any
// image, container or volume: the daemon would start on an empty root and
// hide all of them. The root it uses now is what the daemon answers, else
// the data-root its daemon.json sets, else the record's data_dir, else the
// home's default.
func keepDataRoot(p *planner, s UserState) error {
	was, err := p.oldRoot()
	if err != nil {
		return err
	}
	switch {
	case s.Root != "":
		was = s.Root
	case s.JSONRoot != "":
		was = s.JSONRoot
	}
	now := p.record.DataDir
	if now == "" {
		now = homeRoot(p)
	}
	if was == now || !s.holdsData() {
		return nil
	}
	held := fmt.Sprintf("%d %s, %d %s and %d %s", s.Images, plural(s.Images, "image"), s.Containers, plural(s.Containers, "container"), s.Volumes, plural(s.Volumes, "volume"))
	if !s.Counted {
		held = "data, though its daemon did not answer to count it"
	}
	return fmt.Errorf("%s's Docker keeps its data in %s, which holds %s, and this admit would move its data root to %s, hiding all of it. Keep the --data-dir it was admitted with, or have the operators remove the deployment's stacks first. Nothing was changed", ui.Visible(p.in.Request.User), ui.Visible(was), held, ui.Visible(now))
}

// planDataDir creates --data-dir's paisans-<token>, the user's, mode 700,
// or checks the one there: a directory of the user's, not a link and with
// no ACL, mode 700 or the 710 the user's dockerd gives its data root when
// it starts. Its parent is root's alone, so the user cannot swap it
// between this read and the change.
func planDataDir(p *planner) error {
	dir := p.record.DataDir
	if dir == "" {
		return nil
	}
	user, shown := p.in.Request.User, ui.Visible(dir)
	child := p.f.Dirs[dir]
	switch {
	case dir == p.f.User.HomePath:
		return fmt.Errorf("--data-dir %s would put %s's Docker data in %s, which is its home. Give --data-dir another directory and admit again. Nothing was changed", ui.Visible(p.in.Flags.DataDir), user, shown)
	case !child.Present:
		p.step(Step{Title: "create " + shown, Describe: "daemon: " + user + "'s Docker data, on the disk --data-dir names, " + user + "'s, mode 700",
			Command: "install -d -m 700 -o " + user + " -g " + apply.ShellQuote(groupOf(p)) + " " + apply.ShellQuote(dir), Kinds: []string{"docker"}, Provisional: !p.f.User.Exists})
	case child.Unsafe || len(child.Mode) != 10 || child.Mode[0] != 'd' || !p.f.User.Exists || child.UID != p.f.User.UID:
		return fmt.Errorf("%s exists and is not a directory of %s's (its listing is %q, owner %d). Remove it, or give another --data-dir, and admit again. Nothing was changed", shown, user, ui.Visible(child.Mode), child.UID)
	case child.Mode != "drwx------" && child.Mode != "drwx--x---":
		p.step(Step{Title: "make " + shown + " mode 700", Describe: "daemon: " + user + "'s Docker data is " + user + "'s alone; changed as " + user,
			Command: asUser(p, "chmod 700 "+apply.ShellQuote(dir)), Kinds: []string{"docker"}})
	default:
		p.present("daemon: " + shown + " is " + user + "'s alone")
	}
	return nil
}

// plural is noun as n of it reads: one image, two images.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// groupOf is the user's primary group, its own name for a user not made
// yet (useradd's default on Debian and Ubuntu); a step using it then is
// provisional, and rebuilt with the group read.
func groupOf(p *planner) string {
	if p.f.User.Exists {
		return p.f.User.Group
	}
	return p.in.Request.User
}

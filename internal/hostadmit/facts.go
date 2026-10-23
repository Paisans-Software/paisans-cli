package hostadmit

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostmem"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

//go:embed snippets
var snippets embed.FS

// Facts are what the probe found, read once per Build.
//
// A probed file or directory below a link, or below a directory that is
// not root's or that its group or others can write, is read as Unsafe: its
// FileState or DirState says so and carries nothing else of it. What is
// below such a directory is not what its path names, or can be swapped
// between this read and admit's change, so Build refuses any Unsafe file,
// directory or data path before a part plans (refuseUnsafe), and no part
// needs to check Unsafe itself.
//
// Data.Unsafe covers only the directories above where Docker's data would
// go, and that path being a link. It says nothing of the directory itself:
// whose it is and its mode are its Dirs entry. Task 7 refuses a --data-dir,
// and its paisans-<token> child, whose Dirs entry is not a directory of
// root's (or, for the child, of the user's) with no group or other write.
//
// IptablesModule is the module the host's iptables uses, nf_tables or
// ip_tables (legacy), and IptablesModuleLoaded whether it is loaded or
// built in, as dockerd-rootless-setuptool.sh judges it before it installs.
//
// UFWInstalled is ufw's binary being on the host, and UFWActive is the
// first line of `ufw status`, read as root, being "Status: active"; an
// answer the probe cannot read is not active. Admit works only on a host
// where ufw is installed, and enables it only with --enable-ufw
// (planEnableUFW).
type Facts struct {
	Docker   DockerFacts
	Packages map[string]bool
	// OnDisk is, for each package /etc/paisans/installed names, whether
	// dpkg has its files: installed, or unpacked or half-configured by an
	// install that failed.
	OnDisk map[string]bool
	// AppArmor is whether unprivileged user namespaces are restricted,
	// whether rootlesskit's profile is loaded, whether the apparmor package
	// is installed, and whether it ships that profile.
	AppArmor struct{ Restricted, Profile, Package, Shipped bool }
	User     UserFacts
	// SubUID and SubGID are every user's ranges, by name.
	SubUID, SubGID             map[string][]IDRange
	Files                      map[string]FileState
	Dirs                       map[string]DirState
	EgressEnabled, EgressTable bool
	// EgressRules is how many rules the loaded egress table holds, -1 when
	// the probe did not say.
	EgressRules             int
	BrNetfilter             bool
	IptablesModule          string
	IptablesModuleLoaded    bool
	Bridges                 []string
	ForwardPolicy           string
	UFWInstalled, UFWActive bool
	PortStart, IPForward    string
	DefaultDev, LAN         string
	// OnLink6 are the IPv6 routes of every routing table with no gateway,
	// each a destination the host reaches directly on Dev. Admit drops the
	// global ones for the deployment (onLink6).
	OnLink6       []Route6
	WatchdogRules []string
	// WatchdogDevs is each watchdog node's owner, by path.
	WatchdogDevs map[string]int
	// WatchdogState is /sys/class/watchdog/watchdog0/state, "none" when the
	// driver reports none there, as a legacy one does.
	WatchdogState string
	// WatchdogNowayout is whether watchdog0's driver has nowayout set, so
	// nothing stops its timer once it runs.
	WatchdogNowayout bool
	Records          []string
	// OrphanDropIns are the deployment's mesh drop-ins the probe found by
	// the deployment they name while the user does not exist: a user
	// deleted by hand leaves its own.
	OrphanDropIns []string
	// MeshUnit is the deployment's wg-quick unit: enabled, at boot or at
	// runtime, and active, or on its way up.
	MeshUnit struct{ Enabled, Active bool }
	// Entries is how many entries each directory evict may remove holds,
	// by path, for a directory that exists and is not a link.
	Entries map[string]int
	// Data is where Docker's data would go: the nearest directory that
	// exists. FS and Avail are not read when Unsafe; Above is the probe's
	// data-above, safe, link-above or unsafe-above.
	Data struct {
		Path, FS, Above string
		Avail           int64
		Unsafe          bool
	}
	// Swap is the host's RAM and swap, in any form (hostmem.Facts), and
	// what a swapfile would need. RootFS and RootAvail are the filesystem
	// of / and its free bytes, counting what a swapfile step that stopped
	// part way left at /swapfile.paisans-tmp, which the next one removes
	// first.
	Swap struct {
		hostmem.Facts
		RootAvail int64
		RootFS    string
	}
	// raw is every line, by key, for what no field above carries.
	raw map[string][]string
}

// DockerFacts are the system Docker, if any, as the probe found it.
type DockerFacts struct {
	// Engine is dockerd's version, "" when Docker is absent. Unreadable is
	// a dockerd whose version the probe could not read.
	Engine     string
	Unreadable bool
	Setuptool  bool
	Snap       bool
	SystemSock bool
	// SystemUnits are Docker's system units (docker.socket,
	// docker.service, containerd.service), each enabled, or active, by
	// name.
	SystemUnits map[string]UnitState
	// Held is what a run that stopped while admit held Docker's daemons
	// left: admit's /usr/sbin/policy-rc.d, the copy of the owner's, or a
	// temporary file of admit's. HeldPolicy is admit's policy in place,
	// under which apt may have run; without it the run stopped before
	// apt.
	Held, HeldPolicy bool
	// NetworkStore is the system Docker's network store being there, under
	// DataRoot, which only a daemon that has run leaves.
	NetworkStore bool
	// DataRoot is the data-root /etc/docker/daemon.json sets, "" for
	// Docker's default; DataRootUnread is one set that the probe cannot
	// read, which may hold a network store.
	DataRoot       string
	DataRootUnread bool
	// ContainerdRan is containerd's root directory being there, which
	// containerd creates as it first starts and no package ships, or a
	// root in /etc/containerd/config.toml the probe cannot read, which may
	// be: the system containerd may have run.
	ContainerdRan bool
}

// defaultDataRoot is where the system Docker keeps its data with no
// data-root set.
const defaultDataRoot = "/var/lib/docker"

// NetworkStorePath is the system Docker's network store under its
// data-root.
func (d DockerFacts) NetworkStorePath() string {
	root := d.DataRoot
	if root == "" {
		root = defaultDataRoot
	}
	return root + "/network/files/local-kv.db"
}

// Ran reports whether the system Docker may have run: its network store
// is there, or the data-root that would hold it cannot be read.
func (d DockerFacts) Ran() bool { return d.NetworkStore || d.DataRootUnread }

// UnitState is whether a system unit is enabled, and active.
type UnitState struct{ Enabled, Active bool }

// UserFacts are the deployment's user, Exists false while it does not exist.
type UserFacts struct {
	Exists      bool
	Name        string
	UID, GID    int
	Home, Group string
	Groups      []string
	// HomeKind is how the probe found the user's home, or, for a user not
	// made yet, the home useradd would give it, at HomePath: dir (owned by
	// HomeUID), link, other or absent; "" when no line was read.
	HomeKind, HomePath string
	HomeUID            int
	// HomeAbove is how the directories above HomePath stand, as the probe's
	// above prints it: safe, link or unsafe; "" when no line was read.
	HomeAbove string
	// GIDs are every group the user is in, by number, its primary first.
	GIDs []int
	// GroupTaken is a group of the user's name, for a user not made yet;
	// SudoNames each sudoers file, admit's own aside, that names it then.
	GroupTaken bool
	SudoNames  []string
	// SudoRS is sudo being sudo-rs, whose sudo -l lists no Defaults;
	// SudoWS the original sudo installed as /usr/bin/sudo.ws, whose -l
	// the probe reads in sudo-rs's place.
	SudoRS        bool
	SudoWS        bool
	Linger        bool
	ManagerActive bool
	// SudoRead is sudo -l -U read for the user, or sudo absent. From
	// what it printed: SudoRules are the user's rules, SudoDefaults each
	// entry of its matching Defaults, and SudoSpecific each runas or
	// command-specific Defaults line, as sudo printed them.
	SudoRead                              bool
	SudoRules, SudoDefaults, SudoSpecific []string
}

// Route6 is one on-link IPv6 route: a masked prefix and its interface.
type Route6 struct {
	Prefix netip.Prefix
	Dev    string
}

// IDRange is one line of /etc/subuid or /etc/subgid: Count IDs from Start.
type IDRange struct{ Start, Count int64 }

// FileState is one file admit writes, as the probe saw it. Unsafe is a
// file below a link or another's directory, or one that is neither a
// regular file nor a link: Present, with Mode link-above, unsafe-above or
// other, UID and GID -1, and nothing else read.
type FileState struct {
	Present, Link, Unsafe bool
	Sum, Mode             string
	UID, GID              int
}

// DirState is one directory as ls -ldn printed it. Unsafe is one below a
// link or another's directory: Present, with Mode link-above or
// unsafe-above, which no check for a directory's d accepts, and UID and
// GID -1, which no check for an owner accepts.
type DirState struct {
	Present, Unsafe bool
	Mode            string
	UID, GID        int
}

// probeData is what probe.sh.tmpl is rendered with.
type probeData struct {
	User, Token, Prefix, EgressUnit, EgressTable, DataParent, MeshUnit, Sudoers, HoldMarker string
	Files, Dirs, Counted                                                                    []string
}

// probedFiles are the files admit writes as root, whose state the probe
// reads; each task that writes one adds its path here.
func probedFiles(d deployment.Deployment, in Input) ([]string, error) {
	helper, err := apply.WGHelper{Iface: d.Interface()}.Path()
	if err != nil {
		return nil, err
	}
	record, err := admission.Path(d.Token())
	if err != nil {
		return nil, err
	}
	return []string{
		delegatePath, modulesLoadPath, sysctlPath, sysctlBeforePath, installedPath,
		helper, sudoersPath(d), egressFilePath(d), egressFilePath(d) + ".new", "/etc/systemd/system/" + d.EgressUnit(),
		watchdogRulePath(d), "/etc/systemd/system/" + d.Prefix() + "-watchdog.service",
		record, d.WireGuardInstalled(), meshCondPath(d.WireGuardUnit()),
	}, nil
}

// countedDirs are the directories admit creates for every deployment,
// which evict removes once they hold nothing but what it removes.
func countedDirs() []string { return []string{admission.Dir, deployment.Base, apply.HelperDir} }

// probedDirs are the directories admit creates or checks.
func probedDirs(d deployment.Deployment, in Input) []string {
	dirs := []string{"/etc/wireguard", admission.Dir, deployment.Base, d.Root(), apply.HelperDir, delegateDir}
	if in.Flags.DataDir != "" {
		dirs = append(dirs, in.Flags.DataDir, in.Flags.DataDir+"/"+d.Prefix())
	}
	return dirs
}

// probe runs the probe on t and reads what it prints.
func probe(t apply.Transport, d deployment.Deployment, in Input) (Facts, error) {
	script, err := probeScript(d, in)
	if err != nil {
		return Facts{}, err
	}
	out, err := t.Run(script)
	if err != nil {
		return Facts{}, err
	}
	return parseFacts(out, in.Request.User)
}

// sha is content's SHA-256 in hex, as the probe prints a file's sum.
func sha(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

// probeScript is the probe rendered for d and in. Every value rendered in
// is quoted for the shell with q, so a --data-dir that is absolute and clean
// but holds a space or a $( is a path and nothing else.
func probeScript(d deployment.Deployment, in Input) (string, error) {
	tmpl, err := template.New("probe.sh.tmpl").Funcs(template.FuncMap{"q": apply.ShellQuote, "memProbe": func() string { return hostmem.Probe }}).ParseFS(snippets, "snippets/probe.sh.tmpl")
	if err != nil {
		return "", err
	}
	files, err := probedFiles(d, in)
	if err != nil {
		return "", err
	}
	var script strings.Builder
	if err := tmpl.Execute(&script, probeData{
		User: in.Request.User, Token: d.Token(), Prefix: d.Prefix(),
		EgressUnit: d.EgressUnit(), EgressTable: d.EgressTable(), DataParent: in.Flags.DataDir,
		MeshUnit: d.WireGuardUnit(), Sudoers: sudoersPath(d), Files: files, Dirs: probedDirs(d, in), Counted: countedDirs(),
		HoldMarker: hostprep.HoldDockerMarker,
	}); err != nil {
		return "", err
	}
	return script.String(), nil
}

// parseFacts reads the probe's lines. Values the probe prints are paths,
// numbers and names; a line it cannot read is refused rather than skipped.
func parseFacts(out, user string) (Facts, error) {
	f := Facts{Packages: map[string]bool{}, OnDisk: map[string]bool{}, SubUID: map[string][]IDRange{}, SubGID: map[string][]IDRange{},
		Files: map[string]FileState{}, Dirs: map[string]DirState{}, Entries: map[string]int{}, WatchdogDevs: map[string]int{}, raw: map[string][]string{}}
	f.User.Name = user
	f.Docker.SystemUnits = map[string]UnitState{}
	var sudoL []string
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "end" {
		return f, fmt.Errorf("the probe's answer was cut short")
	}
	for _, line := range lines[:len(lines)-1] {
		key, v, _ := strings.Cut(line, " ")
		f.raw[key] = append(f.raw[key], v)
		switch key {
		case "engine":
			switch v {
			case "absent":
			case "":
				f.Docker.Unreadable = true
			default:
				f.Docker.Engine = v
			}
		case "ondisk":
			name, state, _ := strings.Cut(v, " ")
			f.OnDisk[name] = state == "present"
		case "pkg":
			name, state, _ := strings.Cut(v, " ")
			f.Packages[name] = state == "installed"
		case "setuptool":
			f.Docker.Setuptool = v == "present"
		case "snap":
			f.Docker.Snap = v == "docker"
		case "system-socket":
			f.Docker.SystemSock = v == "present"
		case "system-unit":
			fields := strings.Fields(v)
			if len(fields) != 3 {
				return f, fmt.Errorf("unreadable system-unit line %q", v)
			}
			f.Docker.SystemUnits[fields[0]] = UnitState{Enabled: fields[1] == "enabled" || fields[1] == "enabled-runtime",
				Active: slices.Contains([]string{"active", "activating", "reloading"}, fields[2])}
		case "docker-networks":
			f.Docker.NetworkStore = v == "present"
		case "docker-root":
			switch {
			case v == "unreadable":
				f.Docker.DataRootUnread = true
			case v != defaultDataRoot && path.IsAbs(v) && path.Clean(v) == v && !strings.ContainsFunc(v, unicode.IsControl):
				f.Docker.DataRoot = v
			case v != defaultDataRoot:
				f.Docker.DataRootUnread = true
			}
		case "containerd-ran":
			f.Docker.ContainerdRan = v != "absent"
		case "docker-hold":
			f.Docker.Held, f.Docker.HeldPolicy = v == "policy" || v == "leftover", v == "policy"
		case "userns-restrict":
			f.AppArmor.Restricted = v == "1"
		case "rootlesskit-profile":
			f.AppArmor.Profile = v == "loaded"
		case "rootlesskit-shipped":
			f.AppArmor.Package = v != "no-package"
			f.AppArmor.Shipped = v == "yes"
		case "passwd":
			if v == "absent" {
				continue
			}
			p := strings.Split(v, ":")
			if len(p) != 7 || p[0] != user {
				return f, fmt.Errorf("unreadable passwd entry %q", v)
			}
			uid, err1 := strconv.Atoi(p[2])
			gid, err2 := strconv.Atoi(p[3])
			if err1 != nil || err2 != nil {
				return f, fmt.Errorf("unreadable passwd entry %q", v)
			}
			f.User.Exists, f.User.UID, f.User.GID, f.User.Home = true, uid, gid, p[5]
		case "home":
			p := strings.SplitN(v, " ", 3)
			uid, err := strconv.Atoi(p[min(1, len(p)-1)])
			if len(p) != 3 || err != nil || !slices.Contains([]string{"dir", "link", "other", "absent"}, p[0]) {
				return f, fmt.Errorf("unreadable home line %q", v)
			}
			f.User.HomeKind, f.User.HomeUID, f.User.HomePath = p[0], uid, p[2]
		case "sudo":
			f.User.SudoRead = v == "absent"
		case "sudo-l":
			sudoL = append(sudoL, v)
		case "groups":
			f.User.Groups = strings.Fields(v)
		case "gids":
			for _, g := range strings.Fields(v) {
				n, ok := id(g)
				if !ok {
					return f, fmt.Errorf("unreadable gids line %q", v)
				}
				f.User.GIDs = append(f.User.GIDs, n)
			}
		case "group-taken":
			f.User.GroupTaken = v == "yes"
		case "sudo-names":
			f.User.SudoNames = append(f.User.SudoNames, v)
		case "sudo-kind":
			f.User.SudoRS = v == "rs"
		case "sudo-ws":
			f.User.SudoWS = v == "yes"
		case "home-above":
			if !slices.Contains([]string{"safe", "link", "unsafe"}, v) {
				return f, fmt.Errorf("unreadable home-above line %q", v)
			}
			f.User.HomeAbove = v
		case "group":
			f.User.Group = v
		case "linger":
			f.User.Linger = v == "yes"
		case "manager":
			f.User.ManagerActive = v == "active"
		case "subuid", "subgid":
			p := strings.Split(v, ":")
			if len(p) != 3 {
				return f, fmt.Errorf("unreadable %s line %q", key, v)
			}
			start, err1 := strconv.ParseInt(p[1], 10, 64)
			count, err2 := strconv.ParseInt(p[2], 10, 64)
			if err1 != nil || err2 != nil {
				return f, fmt.Errorf("unreadable %s line %q", key, v)
			}
			m := f.SubUID
			if key == "subgid" {
				m = f.SubGID
			}
			m[p[0]] = append(m[p[0]], IDRange{start, count})
		case "file":
			fields := strings.Fields(v)
			switch {
			case len(fields) == 2 && fields[1] == "link":
				f.Files[fields[0]] = FileState{Present: true, Link: true}
			case len(fields) == 2 && fields[1] == "absent":
				f.Files[fields[0]] = FileState{}
			case len(fields) == 2 && (fields[1] == "link-above" || fields[1] == "unsafe-above" || fields[1] == "other"):
				f.Files[fields[0]] = FileState{Present: true, Unsafe: true, Mode: fields[1], UID: -1, GID: -1}
			case len(fields) == 5:
				uid, ok1 := id(fields[3])
				gid, ok2 := id(fields[4])
				if !ok1 || !ok2 || !fileSum.MatchString(fields[1]) || !fileMode.MatchString(fields[2]) {
					return f, fmt.Errorf("unreadable file line %q", v)
				}
				f.Files[fields[0]] = FileState{Present: true, Sum: fields[1], Mode: fields[2], UID: uid, GID: gid}
			default:
				return f, fmt.Errorf("unreadable file line %q", v)
			}
		case "dir":
			// The path is everything before the last three fields, so a
			// --data-dir with a space in it is read whole.
			fields := strings.Split(v, " ")
			if len(fields) < 4 {
				return f, fmt.Errorf("unreadable dir line %q", v)
			}
			n := len(fields)
			uid, err1 := strconv.Atoi(fields[n-2])
			gid, err2 := strconv.Atoi(fields[n-1])
			dir := strings.Join(fields[:n-3], " ")
			if err1 != nil || err2 != nil || dir == "" {
				return f, fmt.Errorf("unreadable dir line %q", v)
			}
			// ls marks an SELinux context with a trailing dot, which grants
			// nothing; an ACL's + is kept.
			mode := fields[n-3]
			if len(mode) == 11 && mode[10] == '.' {
				mode = mode[:10]
			}
			if mode == "link-above" || mode == "unsafe-above" {
				f.Dirs[dir] = DirState{Present: true, Unsafe: true, Mode: mode, UID: -1, GID: -1}
				continue
			}
			f.Dirs[dir] = DirState{Present: mode != "absent", Mode: mode, UID: uid, GID: gid}
		case "egress-enabled":
			f.EgressEnabled = v == "yes"
		case "egress-table":
			// present, with how many rules the loaded table holds.
			state, count, _ := strings.Cut(v, " ")
			f.EgressTable, f.EgressRules = state == "present", -1
			if n, ok := id(count); ok {
				f.EgressRules = n
			}
		case "br_netfilter":
			f.BrNetfilter = v == "loaded"
		case "iptables-module":
			name, state, _ := strings.Cut(v, " ")
			if name != "nf_tables" && name != "ip_tables" {
				return f, fmt.Errorf("unreadable iptables-module line %q", v)
			}
			f.IptablesModule, f.IptablesModuleLoaded = name, state == "loaded"
		case "bridge":
			f.Bridges = append(f.Bridges, v)
		case "forward-policy":
			f.ForwardPolicy = v
		case "ufw":
			f.UFWInstalled = v == "installed"
		case "ufw-active":
			f.UFWActive = v == "yes"
		case "port-start", "ip-forward":
			// Both reach sysctl.before and a sysctl -w run as root.
			if _, ok := id(v); !ok {
				return f, fmt.Errorf("unreadable %s line %q: the probe could not read the sysctl", key, v)
			}
			if key == "port-start" {
				f.PortStart = v
			} else {
				f.IPForward = v
			}
		case "default-dev":
			f.DefaultDev = v
		case "lan":
			f.LAN = v
		case "lan6":
			r, err := parseRoute6(v)
			if err != nil {
				return f, err
			}
			f.OnLink6 = append(f.OnLink6, r)
		case "orphan-dropin":
			if !orphanDropIn.MatchString(v) {
				return f, fmt.Errorf("unreadable orphan-dropin line %q", v)
			}
			f.OrphanDropIns = append(f.OrphanDropIns, v)
		case "watchdog-rule":
			f.WatchdogRules = append(f.WatchdogRules, v)
		case "watchdog-dev":
			owner, dev, ok := strings.Cut(v, " ")
			uid, okUID := id(owner)
			if !ok || !okUID || !strings.HasPrefix(dev, "/dev/watchdog") {
				return f, fmt.Errorf("unreadable watchdog-dev line %q", v)
			}
			f.WatchdogDevs[dev] = uid
		case "watchdog-state":
			f.WatchdogState = v
		case "watchdog-nowayout":
			f.WatchdogNowayout = v == "yes"
		case "record":
			f.Records = append(f.Records, v)
		case "mesh-unit":
			enabled, active, ok := strings.Cut(v, " ")
			if !ok {
				return f, fmt.Errorf("unreadable mesh-unit line %q", v)
			}
			f.MeshUnit.Enabled = enabled == "enabled" || enabled == "enabled-runtime"
			f.MeshUnit.Active = slices.Contains([]string{"active", "activating", "reloading"}, active)
		case "entries":
			count, dir, ok := strings.Cut(v, " ")
			n, err := strconv.Atoi(count)
			if !ok || err != nil || n < 0 || dir == "" {
				return f, fmt.Errorf("unreadable entries line %q", v)
			}
			f.Entries[dir] = n
		case "data-above":
			f.Data.Above = v
			switch v {
			case "safe":
			case "link-above", "unsafe-above":
				f.Data.Unsafe = true
			default:
				return f, fmt.Errorf("unreadable data-above line %q", v)
			}
		case "data-path":
			f.Data.Path = v
		case "data-fs":
			f.Data.FS = v
		case "data-avail":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return f, fmt.Errorf("unreadable data-avail line %q", v)
			}
			f.Data.Avail = n
		case "mem-total", "swap-total", "zram-on", "swap-active", "swap-partition", "swap-fstab", "swap-zram", "swapfile":
			if _, err := f.Swap.Read(key, v); err != nil {
				return f, err
			}
		case "root-avail", "swap-tmp":
			if v != "" {
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 0 {
					return f, fmt.Errorf("unreadable %s line %q", key, v)
				}
				f.Swap.RootAvail += n
			}
		case "root-fs":
			f.Swap.RootFS = v
		}
	}
	if f.raw["sudo"] != nil && f.raw["sudo"][len(f.raw["sudo"])-1] == "listed" {
		if list, ok := admitrules.ReadSudoListAsRoot(user, sudoL); ok {
			f.User.SudoRules, f.User.SudoDefaults, f.User.SudoSpecific, f.User.SudoRead = list.Rules, list.Defaults, list.Specific, true
		}
	}
	if f.User.Exists && f.User.Group == "" {
		return f, fmt.Errorf("no primary group read for %s", user)
	}
	return f, nil
}

// fileSum is a checksum as the probe prints one, fileMode a mode as stat's
// %a does, and orphanDropIn the path of a mesh drop-in, MeshDropInPath's.
var (
	fileSum      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileMode     = regexp.MustCompile(`^[0-7]{3,4}$`)
	orphanDropIn = regexp.MustCompile(`^/etc/systemd/system/user@[0-9]+\.service\.d/paisans-mesh\.conf$`)
)

// id is v read as a UID, a GID or a sysctl's value: digits alone.
func id(v string) (int, bool) {
	if v == "" || strings.Trim(v, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// parseRoute6 reads a lan6 line, "<destination> <interface>", as ip -6
// route prints a destination: a prefix, or an address for a route to one
// host. Anything but a masked IPv6 prefix with no zone is refused, so the
// table is rendered from a parsed prefix and never from the probe's text.
func parseRoute6(v string) (Route6, error) {
	fields := strings.Fields(v)
	if len(fields) != 2 {
		return Route6{}, fmt.Errorf("unreadable lan6 line %q", v)
	}
	dest := fields[0]
	if !strings.Contains(dest, "/") {
		dest += "/128"
	}
	p, err := netip.ParsePrefix(dest)
	if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() || p != p.Masked() {
		return Route6{}, fmt.Errorf("unreadable lan6 line %q", v)
	}
	return Route6{Prefix: p, Dev: fields[1]}, nil
}

// The files admit writes as root that the probe reads. Each is what the
// task that writes it writes; they live here, beside the probe, because the
// probe is what reads them.
const (
	delegateDir      = "/etc/systemd/system/user@.service.d"
	delegatePath     = delegateDir + "/delegate.conf"
	modulesLoadPath  = "/etc/modules-load.d/paisans.conf"
	sysctlPath       = "/etc/sysctl.d/60-paisans.conf"
	sysctlBeforePath = admission.Dir + "/sysctl.before"
	installedPath    = admission.Dir + "/installed"
)

// sudoersPath is the deployment's sudoers line, egressFilePath its egress
// table's file, watchdogRulePath its watchdog's udev rule.
func sudoersPath(d deployment.Deployment) string { return "/etc/sudoers.d/" + d.Prefix() }
func egressFilePath(d deployment.Deployment) string {
	return admission.Dir + "/egress." + d.Token() + ".nft"
}
func watchdogRulePath(d deployment.Deployment) string {
	return "/etc/udev/rules.d/60-" + d.Prefix() + "-watchdog.rules"
}

// onDisk reports whether dpkg has package name's files, as the probe read
// it for a package the installed list names; for one it does not, whether
// dpkg shows it installed.
func (f Facts) onDisk(name string) bool {
	if v, ok := f.OnDisk[name]; ok {
		return v
	}
	return f.Packages[name]
}

package hostadmit

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
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
// where ufw is installed and active, and never enables it or sets its
// defaults, so Task 9 refuses unless both are true.
type Facts struct {
	Docker   DockerFacts
	Packages map[string]bool
	AppArmor struct{ Restricted, Profile bool }
	User     UserFacts
	// SubUID and SubGID are every user's ranges, by name.
	SubUID, SubGID             map[string][]IDRange
	Files                      map[string]FileState
	Dirs                       map[string]DirState
	EgressEnabled, EgressTable bool
	BrNetfilter                bool
	IptablesModule             string
	IptablesModuleLoaded       bool
	Bridges                    []string
	ForwardPolicy              string
	UFWInstalled, UFWActive    bool
	PortStart, IPForward       string
	DefaultDev, LAN            string
	// OnLink6 are the IPv6 routes of the main table with no gateway, each
	// a destination the host reaches directly on Dev. Admit drops the
	// global ones for the deployment (onLink6).
	OnLink6       []Route6
	WatchdogRules []string
	Records       []string
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
	// raw is every line, by key, for what no field above carries.
	raw map[string][]string
}

// DockerFacts are the system Docker, if any, as the probe found it.
type DockerFacts struct {
	// Engine is dockerd's version, "" when Docker is absent.
	Engine     string
	Setuptool  bool
	Snap       bool
	SystemSock bool
}

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
	// SudoRS is sudo being sudo-rs, whose sudo -l lists no Defaults.
	SudoRS bool
	Linger             bool
	ManagerActive      bool
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
	User, Token, Prefix, EgressUnit, EgressTable, DataParent, MeshUnit, Sudoers string
	Files, Dirs, Counted                                               []string
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
		delegatePath, modulesLoadPath, sysctlPath, sysctlBeforePath,
		helper, sudoersPath(d), egressFilePath(d), "/etc/systemd/system/" + d.EgressUnit(),
		watchdogRulePath(d), "/etc/systemd/system/" + d.Prefix() + "-watchdog.service",
		record, d.WireGuardInstalled(),
	}, nil
}

// countedDirs are the directories admit creates for every deployment,
// which evict removes once they hold nothing but what it removes.
func countedDirs() []string { return []string{admission.Dir, deployment.Base, apply.HelperDir} }

// probedDirs are the directories admit creates or checks.
func probedDirs(d deployment.Deployment, in Input) []string {
	dirs := []string{"/etc/wireguard", admission.Dir, deployment.Base, d.Root(), apply.HelperDir}
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
	tmpl, err := template.New("probe.sh.tmpl").Funcs(template.FuncMap{"q": apply.ShellQuote}).ParseFS(snippets, "snippets/probe.sh.tmpl")
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
	}); err != nil {
		return "", err
	}
	return script.String(), nil
}

// parseFacts reads the probe's lines. Values the probe prints are paths,
// numbers and names; a line it cannot read is refused rather than skipped.
func parseFacts(out, user string) (Facts, error) {
	f := Facts{Packages: map[string]bool{}, SubUID: map[string][]IDRange{}, SubGID: map[string][]IDRange{},
		Files: map[string]FileState{}, Dirs: map[string]DirState{}, Entries: map[string]int{}, raw: map[string][]string{}}
	f.User.Name = user
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
			if v != "absent" {
				f.Docker.Engine = v
			}
		case "pkg":
			name, state, _ := strings.Cut(v, " ")
			f.Packages[name] = state == "installed"
		case "setuptool":
			f.Docker.Setuptool = v == "present"
		case "snap":
			f.Docker.Snap = v == "docker"
		case "system-socket":
			f.Docker.SystemSock = v == "present"
		case "userns-restrict":
			f.AppArmor.Restricted = v == "1"
		case "rootlesskit-profile":
			f.AppArmor.Profile = v == "loaded"
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
			f.EgressTable = v == "present"
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
		case "watchdog-rule":
			f.WatchdogRules = append(f.WatchdogRules, v)
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
		}
	}
	if f.raw["sudo"] != nil && f.raw["sudo"][len(f.raw["sudo"])-1] == "listed" {
		readSudo(&f.User, sudoL)
	}
	if f.User.Exists && f.User.Group == "" {
		return f, fmt.Errorf("no primary group read for %s", user)
	}
	return f, nil
}

// fileSum is a checksum as the probe prints one, fileMode a mode as stat's
// %a does.
var (
	fileSum  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileMode = regexp.MustCompile(`^[0-7]{3,4}$`)
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

// readSudo reads what sudo -n -l -U printed for the user under LC_ALL=C:
// a header line per section, each entry indented below it, long lines
// wrapped onto more indented lines. A line it does not know, or a listing
// with neither the user's rules nor sudo's "is not allowed", leaves
// SudoRead false, so admit refuses rather than guess.
func readSudo(u *UserFacts, lines []string) {
	section, understood := "", false
	var defaults []string
	for _, l := range lines {
		indented := strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")
		entry := strings.TrimSpace(l)
		switch {
		case entry == "":
			section = ""
		case !indented && strings.HasPrefix(l, "Matching Defaults entries for "):
			section = "defaults"
		case !indented && strings.HasPrefix(l, "Runas and Command-specific defaults for "):
			section = "specific"
		case !indented && strings.Contains(l, " may run the following commands on "):
			section, understood = "rules", true
		case !indented && strings.Contains(l, " is not allowed to run sudo on "):
			section, understood = "", true
		case indented && section == "defaults":
			defaults = append(defaults, entry)
		case indented && section == "specific":
			u.SudoSpecific = append(u.SudoSpecific, entry)
		case indented && section == "rules":
			u.SudoRules = append(u.SudoRules, entry)
		default:
			return
		}
	}
	// Defaults wrap after a comma, and a comma inside a value is escaped.
	for _, e := range splitUnescaped(strings.Join(defaults, " "), ',') {
		if e = strings.TrimSpace(e); e != "" {
			u.SudoDefaults = append(u.SudoDefaults, e)
		}
	}
	u.SudoRead = understood
}

// splitUnescaped splits s at each sep that no backslash escapes.
func splitUnescaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// The files admit writes as root that the probe reads. Each is what the
// task that writes it writes; they live here, beside the probe, because the
// probe is what reads them.
const (
	delegatePath     = "/etc/systemd/system/user@.service.d/delegate.conf"
	modulesLoadPath  = "/etc/modules-load.d/paisans.conf"
	sysctlPath       = "/etc/sysctl.d/60-paisans.conf"
	sysctlBeforePath = admission.Dir + "/sysctl.before"
)

// meshDropInPath is the user's drop-in that orders its manager after the
// mesh. Its path holds the user's UID, which the probe learns, so the probe
// reads it from its own list (userfiles) rather than from probedFiles.
func meshDropInPath(uid string) string {
	return "/etc/systemd/system/user@" + uid + ".service.d/paisans-mesh.conf"
}

// sudoersPath is the deployment's sudoers line, egressFilePath its egress
// table's file, watchdogRulePath its watchdog's udev rule.
func sudoersPath(d deployment.Deployment) string { return "/etc/sudoers.d/" + d.Prefix() }
func egressFilePath(d deployment.Deployment) string {
	return admission.Dir + "/egress." + d.Token() + ".nft"
}
func watchdogRulePath(d deployment.Deployment) string {
	return "/etc/udev/rules.d/60-" + d.Prefix() + "-watchdog.rules"
}

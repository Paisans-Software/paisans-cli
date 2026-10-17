// Package registry is a host's record of every paisans deployment on it.
//
// Several deployments may share one host. Each lays out under its own root,
// /srv/paisans/<token>, and names everything it owns paisans-<token>-..., so
// two deployments with the same token would share a root and every name: each
// would read the other's files as its own and replace the other's containers.
// The token is only four hex digits of a random id, so a collision is
// unlikely and not impossible, and nothing about the id alone can see one.
// The registry can: /var/lib/paisans/registry.json lists every deployment
// that has claimed the host, by id, with its token and root, and site admit
// claims it, as root, before it changes anything. A claim by an id whose token or root
// is already another id's is refused, and the host is left as it was.
//
// The same record keeps two deployments' meshes apart. Each entry carries the
// deployment's WireGuard interface, the port it listens on and its mesh
// subnet, and a claim is refused when another deployment on the host holds
// the same interface, the same port, or a subnet overlapping this one's:
// two interfaces cannot bind one port, and two meshes on overlapping ranges
// route each other's traffic. See internal/mesh for the checks against
// everything else on the host.
//
// It also keeps the gateway and the data role to one deployment per host.
// The gateway owns ports 80 and 443 and the Caddy that imports its deployment's caddy.d;
// a data site owns the Postgres and etcd ports and the storage under
// them. Each entry carries the site's roles as one string, sorted and joined
// with commas (Eg: "apps,data,gateway"), so the awk merge tests a role with
// one index() on the list wrapped in commas, and a claim is refused when
// another deployment holds gateway and this one claims gateway, or data and
// data. An entry without roles holds none.
//
// The claim runs as one remote shell command under flock, so two operators
// claiming one host at once are serialised rather than both reading the old
// file. The merge itself is awk, which a host has before anything is
// installed on it. The file therefore has a fixed layout, one deployment per line, which Encode
// writes and the awk program reads; a file in any other layout is refused
// rather than guessed at. The layout is also plain JSON.
//
// Everything that decides is a pure function here (Parse, Conflicts, Merge,
// Encode), tested without a host. ClaimCommand is the same decision as a
// shell command, and its awk is run against Encode's output in the tests.
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// Dir holds the registry and its lock. It is root's and 0711: others may
// enter it but not list it. The registry names every community a host
// carries, which is nobody else's business, so it is 0600 and the listing,
// which would name each deployment's files, is root's. Entering is allowed
// so that a gateway's deployment record, which holds only names and is
// 0644, can be read at its known path without sudo (see deployrecord).
const Dir = "/var/lib/paisans"

// Path is the registry, and LockPath the file flock holds while it changes.
const (
	Path     = Dir + "/registry.json"
	LockPath = Dir + "/registry.lock"
)

// Version is the registry layout this toolkit reads and writes.
const Version = 1

// lockWait is how long a claim waits for another claim to finish, in seconds.
const lockWait = 60

// Entry is one deployment on the host.
type Entry struct {
	// Token and Root are what must be unique on a host: every name a
	// deployment owns carries the token, and everything it renders lands
	// under the root.
	Token string `json:"token"`
	Root  string `json:"root"`
	// Domain is community.domain, so that a refusal can say which community
	// holds the token in words a person recognises.
	Domain string `json:"domain"`
	// Site is this host's site name in that deployment.
	Site string `json:"site"`
	// ClaimedAt is when the host was last claimed for it, RFC 3339.
	ClaimedAt string `json:"claimed_at"`
	// Interface is the deployment's WireGuard interface, psns-<token>.
	Interface string `json:"interface,omitempty"`
	// ListenPort is the UDP port that interface listens on here, zero when
	// nothing dials the site and WireGuard picks one.
	ListenPort int `json:"listen_port,omitempty"`
	// Subnet is the deployment's mesh subnet, and Address this site's
	// address in it.
	Subnet  string `json:"subnet,omitempty"`
	Address string `json:"address,omitempty"`
	// Roles is the site's roles, sorted and joined with commas, one string
	// because the awk merge matches a role in it with index().
	Roles string `json:"roles,omitempty"`
	// Build is the paisans that made the claim, refreshed by every claim:
	// toolkit_version, toolkit_commit and toolkit_modified. Parse refuses
	// an entry without it.
	version.Build
}

// exclusiveRoles are the roles one host gives to one deployment: the gateway
// binds 80 and 443 and runs the Caddy that imports its deployment's caddy.d, and a data
// site binds the Postgres and etcd ports and holds the cluster's storage.
var exclusiveRoles = []config.Role{config.RoleData, config.RoleGateway}

// JoinRoles is roles in an entry's form: sorted, joined with commas.
func JoinRoles(roles []config.Role) string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// hasRole reports whether an entry's role list holds role.
func hasRole(list string, role config.Role) bool {
	return list != "" && strings.Contains(","+list+",", ","+string(role)+",")
}

// Registry is the whole file.
type Registry struct {
	Version     int              `json:"version"`
	Deployments map[string]Entry `json:"deployments"`
}

// ForRequest is the entry site admit claims a host with, from the request
// alone, so a plan from a request and one from the configuration claim
// alike. It carries the request's site and roles, and this build of
// paisans; Domain stays empty, since a request names no domain. A request
// granted the public ports also holds the gateway role, whatever its roles:
// 80 and 443 are one deployment's per host, and a re-run without the grant
// deletes the deployment's 80 and 443 rules outright, which a second holder
// would lose.
func ForRequest(req admission.Request, now time.Time) (string, Entry) {
	d := deployment.Deployment{ID: req.Deployment}
	roles := make([]config.Role, 0, len(req.Roles)+1)
	for _, r := range req.Roles {
		roles = append(roles, config.Role(r))
	}
	if req.Grants.PublicPorts && !hasRole(JoinRoles(roles), config.RoleGateway) {
		roles = append(roles, config.RoleGateway)
	}
	return req.Deployment, Entry{
		Token:      d.Token(),
		Root:       d.Root(),
		Site:       req.Site,
		ClaimedAt:  now.UTC().Format(time.RFC3339),
		Interface:  d.Interface(),
		ListenPort: req.ListenPort,
		Subnet:     req.MeshSubnet,
		Address:    req.Address,
		Roles:      JoinRoles(roles),
		Build:      version.Current(),
	}
}

// Refused is the refusal a claim of e meets for why, for site admit's
// read-only check.
func Refused(t Runner, e Entry, why error) error { return refused(t, e, why) }

// Parse reads a registry. An empty file is an empty registry, which is what a
// host no deployment has claimed holds. A registry with an entry that does
// not say which build of paisans claimed the host is refused with a
// *NoBuildError, and with the registry as read beside it, for site admit,
// which claims its own deployment's entry anew.
func Parse(data []byte) (Registry, error) {
	r := Registry{Version: Version, Deployments: map[string]Entry{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Registry{}, fmt.Errorf("%s is not readable: %w", Path, err)
	}
	if r.Version != Version {
		return Registry{}, fmt.Errorf("%s is version %d, and this toolkit reads version %d", Path, r.Version, Version)
	}
	if r.Deployments == nil {
		r.Deployments = map[string]Entry{}
	}
	var raw struct {
		Deployments map[string]map[string]json.RawMessage `json:"deployments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Registry{}, fmt.Errorf("%s is not readable: %w", Path, err)
	}
	refused := &NoBuildError{}
	for id, entry := range raw.Deployments {
		var noBuild *version.NoBuildError
		if _, err := version.CheckBuild(entry); errors.As(err, &noBuild) {
			refused.IDs = append(refused.IDs, id)
			refused.Why = append(refused.Why, id+": "+noBuild.Detail())
		}
	}
	if len(refused.IDs) > 0 {
		sort.Strings(refused.IDs)
		sort.Strings(refused.Why)
		return r, refused
	}
	return r, nil
}

// NoBuildError is a registry whose entries for IDs do not say which build of
// paisans claimed the host for them, with why for each, as
// version.CheckBuild says it.
type NoBuildError struct {
	IDs []string
	Why []string
}

func (e *NoBuildError) Error() string {
	which := "the entry of deployment " + e.IDs[0] + " does"
	if len(e.IDs) > 1 {
		which = "the entries of deployments " + strings.Join(e.IDs, ", ") + " do"
	}
	return fmt.Sprintf("%s: %s not say which build of paisans claimed the host (%s). The host's owner admits each again: `paisans site request`, then `paisans site admit`", Path, which, strings.Join(e.Why, "; "))
}

// Conflict is another deployment holding what a claim needs.
type Conflict struct {
	ID    string
	Entry Entry
	// Clashes is each thing the other deployment holds that the claim
	// needs, in words: Eg: "WireGuard listen port 51820/udp".
	Clashes []string
}

func (c Conflict) Error() string {
	what := strings.Join(c.Clashes, ", ")
	if what == "" {
		what = "token " + c.Entry.Token
		if c.Entry.Root != "" {
			what += " and root " + c.Entry.Root
		}
	}
	who := c.ID
	if c.Entry.Domain != "" {
		who += " (" + c.Entry.Domain + ")"
	}
	return fmt.Sprintf("deployment %s already holds %s on this host", who, what)
}

// clashes is everything theirs holds that ours needs: the same token, root,
// interface or listen port, a mesh subnet overlapping ours either way, or the
// gateway or data role when ours claims it too. A listen port of zero is a
// site nothing dials, which WireGuard gives a port of its own, and holds
// none.
func clashes(theirs, ours Entry) []string {
	var out []string
	if theirs.Token == ours.Token {
		out = append(out, "token "+theirs.Token)
	}
	if theirs.Root == ours.Root {
		out = append(out, "root "+theirs.Root)
	}
	if theirs.Interface == ours.Interface {
		out = append(out, "WireGuard interface "+theirs.Interface)
	}
	if theirs.ListenPort != 0 && theirs.ListenPort == ours.ListenPort {
		out = append(out, fmt.Sprintf("WireGuard listen port %d/udp", theirs.ListenPort))
	}
	if subnetsOverlap(theirs.Subnet, ours.Subnet) {
		if theirs.Subnet == ours.Subnet {
			out = append(out, "mesh subnet "+theirs.Subnet)
		} else {
			out = append(out, fmt.Sprintf("mesh subnet %s, which overlaps this deployment's %s", theirs.Subnet, ours.Subnet))
		}
	}
	for _, role := range exclusiveRoles {
		if hasRole(theirs.Roles, role) && hasRole(ours.Roles, role) {
			out = append(out, fmt.Sprintf("the %s role", role))
		}
	}
	return out
}

func subnetsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	pa, errA := mesh.ParsePrefix(a)
	pb, errB := mesh.ParsePrefix(b)
	return errA == nil && errB == nil && mesh.Overlap(pa, pb)
}

// Conflicts lists every other deployment holding something id's claim
// needs, sorted by id. The same id is never a conflict: that is a refresh.
func Conflicts(r Registry, id string, e Entry) []Conflict {
	var out []Conflict
	for other, entry := range r.Deployments {
		if other == id {
			continue
		}
		if c := clashes(entry, e); len(c) > 0 {
			out = append(out, Conflict{ID: other, Entry: entry, Clashes: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Merge adds or refreshes id's entry, or refuses with the first conflict and
// returns r unchanged. id's own entry for another site (AnotherSite) is
// refused as the awk merge refuses it, with a Conflict naming id itself.
func Merge(r Registry, id string, e Entry) (Registry, error) {
	if mine, ok := r.Deployments[id]; ok && AnotherSite(mine, e) {
		return r, Conflict{ID: id, Entry: mine}
	}
	if c := Conflicts(r, id, e); len(c) > 0 {
		return r, c[0]
	}
	out := Registry{Version: Version, Deployments: map[string]Entry{}}
	for k, v := range r.Deployments {
		out.Deployments[k] = v
	}
	out.Deployments[id] = e
	return out, nil
}

// AnotherSite reports whether a claim e by the deployment that holds mine
// is for another site of it. Where both name a site, the names decide, so a
// site whose mesh address changed is still itself; where either names none,
// another mesh address is another site. The host is one site's machine.
func AnotherSite(mine, e Entry) bool {
	if mine.Site != "" && e.Site != "" {
		return mine.Site != e.Site
	}
	return mine.Address != "" && e.Address != "" && mine.Address != e.Address
}

// header and footer are the first and last lines of the layout, and an entry
// sits on each line between them.
const (
	header = `{"version":1,"deployments":{`
	footer = `}}`
)

// entryLine is one deployment as a line of the layout, without the comma
// that separates it from the next. json.Marshal escapes every quote inside a
// value, so a value can never close the string it sits in and look like a
// key to the awk program.
func entryLine(id string, e Entry) (string, error) {
	key, err := json.Marshal(id)
	if err != nil {
		return "", err
	}
	value, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(key) + ":" + string(value), nil
}

// Encode writes the registry in its layout, deployments sorted by id. The
// result is JSON, and also exactly the line layout ClaimCommand reads.
func Encode(r Registry) ([]byte, error) {
	ids := make([]string, 0, len(r.Deployments))
	for id := range r.Deployments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString(header + "\n")
	for i, id := range ids {
		line, err := entryLine(id, r.Deployments[id])
		if err != nil {
			return nil, err
		}
		b.WriteString(line)
		if i < len(ids)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(footer + "\n")
	return []byte(b.String()), nil
}

// Markers the claim prints, so the caller can tell a refusal from a failure.
const (
	conflictMarker   = "paisans-registry-conflict "
	unreadableMarker = "paisans-registry-unreadable"
)

// mergeProgram is the claim's merge, in awk. It reads the registry in
// Encode's layout, keeps every other deployment's line, drops id's own line
// (the refresh), and prints the result with entry last. id's own line naming
// another site is a conflict too, and so, where either line names no site,
// is one at another mesh address: the host is that site's machine (see
// AnotherSite). A line under another
// id holding the claimed token, root, interface or listen port, a mesh
// subnet overlapping the claimed one, or the gateway or data role the claim
// also holds, is a conflict: it is printed to stderr
// after conflictMarker and the program exits 3 without printing a registry.
// A file in any other layout exits 4.
//
// A value is found by its key with both quotes, "token":", which no value
// can contain since json.Marshal escapes every quote inside one. The overlap
// test is integer arithmetic on each network's first address and size,
// since POSIX awk has no bitwise operators; awk's numbers are doubles, exact
// far beyond 2^32.
const mergeProgram = `
function str(line, name,   key, i, rest) {
	key = "\"" name "\":\""
	i = index(line, key)
	if (!i) return ""
	rest = substr(line, i + length(key))
	return substr(rest, 1, index(rest, "\"") - 1)
}
function num(line, name,   key, i, rest) {
	key = "\"" name "\":"
	i = index(line, key)
	if (!i) return ""
	rest = substr(line, i + length(key))
	if (!match(rest, /^[0-9]+/)) return ""
	return substr(rest, 1, RLENGTH)
}
function hasrole(list, r) {
	return list != "" && index("," list ",", "," r ",") > 0
}
function ipnum(a,   p) {
	split(a, p, ".")
	return ((p[1] * 256 + p[2]) * 256 + p[3]) * 256 + p[4]
}
function overlaps(a, b,   pa, pb, la, lb, sa, sb) {
	if (a == "" || b == "") return 0
	split(a, pa, "/"); split(b, pb, "/")
	la = 2 ^ (32 - pa[2]); lb = 2 ^ (32 - pb[2])
	sa = ipnum(pa[1]); sa -= sa % la
	sb = ipnum(pb[1]); sb -= sb % lb
	return sa < sb + lb && sb < sa + la
}
NR == 1 { if ($0 != header) bad = 1; next }
done { if ($0 != "") bad = 1; next }
$0 == footer { done = 1; next }
{
	line = $0
	sub(/,$/, "", line)
	if (line !~ /^"[^"]+":\{.*\}$/) { bad = 1; next }
	key = substr(line, 2, index(line, "\":") - 2)
	if (key == id) {
		s = str(line, "site"); a = str(line, "address")
		if ((s != "" && site != "") ? (s != site) : (a != "" && address != "" && a != address)) {
			print marker line > "/dev/stderr"
			conflict = 1
		}
		next
	}
	if (index(line, "\"token\":\"" token "\"") || index(line, "\"root\":\"" root "\"") ||
		str(line, "interface") == iface ||
		(port != "" && port != "0" && num(line, "listen_port") == port) ||
		(subnet != "" && overlaps(subnet, str(line, "subnet"))) ||
		(hasrole(roles, "gateway") && hasrole(str(line, "roles"), "gateway")) ||
		(hasrole(roles, "data") && hasrole(str(line, "roles"), "data"))) {
		print marker line > "/dev/stderr"
		conflict = 1
	}
	kept[++n] = line
}
END {
	if (NR > 0 && !done) bad = 1
	if (bad) { print unreadable > "/dev/stderr"; exit 4 }
	if (conflict) exit 3
	print header
	for (i = 1; i <= n; i++) print kept[i] ","
	print entry
	print footer
}
`

// awkArgs is the merge's awk argument list, unquoted: every -v assignment,
// then the program. ClaimCommand quotes it for the shell; a test runs it
// directly.
func awkArgs(id string, e Entry) ([]string, error) {
	entry, err := entryLine(id, e)
	if err != nil {
		return nil, err
	}
	port := ""
	if e.ListenPort != 0 {
		port = fmt.Sprint(e.ListenPort)
	}
	vars := map[string]string{
		"id": id, "token": e.Token, "root": e.Root, "entry": entry,
		"iface": e.Interface, "port": port, "subnet": e.Subnet, "roles": e.Roles, "site": e.Site, "address": e.Address,
		"header": header, "footer": footer, "marker": conflictMarker, "unreadable": unreadableMarker,
	}
	var args []string
	for _, name := range []string{"id", "token", "root", "iface", "port", "subnet", "roles", "site", "address", "entry", "header", "footer", "marker", "unreadable"} {
		args = append(args, "-v", name+"="+awkString(vars[name]))
	}
	return append(args, mergeProgram), nil
}

// ClaimCommand is the one remote command that claims a host for id: under
// flock, read the registry, refuse on a conflict, otherwise write it with
// id's entry added or refreshed to a temporary file beside it and move that
// over it. A refusal or a failure leaves the registry as it was.
func ClaimCommand(id string, e Entry) (string, error) {
	args, err := awkArgs(id, e)
	if err != nil {
		return "", err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join([]string{
		"set -e",
		"umask 077",
		"mkdir -p " + Dir,
		"chmod 711 " + Dir,
		"exec 9>>" + LockPath,
		fmt.Sprintf("flock -w %d 9 || { echo 'paisans: another command holds %s'; exit 1; }", lockWait, LockPath),
		"tmp=$(mktemp " + Dir + "/.registry.XXXXXX)",
		`trap 'rm -f "$tmp"' EXIT`,
		"src=" + Path,
		`[ -f "$src" ] || src=/dev/null`,
		"awk " + strings.Join(quoted, " ") + ` "$src" > "$tmp"`,
		`chmod 600 "$tmp"`,
		`mv "$tmp" ` + Path,
	}, "; "), nil
}

// ParseClaim turns a failed claim's output into the refusal it stands for:
// a Conflict naming the other deployment, or the registry being unreadable.
// Any other output is not a refusal and is returned as nil.
func ParseClaim(out string) error {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, conflictMarker); ok {
			var one map[string]Entry
			if err := json.Unmarshal([]byte("{"+rest+"}"), &one); err == nil {
				for id, e := range one {
					return Conflict{ID: id, Entry: e}
				}
			}
			return fmt.Errorf("another deployment holds what this one needs on the host: %s", rest)
		}
		if line == unreadableMarker {
			return fmt.Errorf("%s is not in the layout this toolkit writes, so it was not changed. Restore it, or move it aside if no deployment on this host is running", Path)
		}
	}
	return nil
}

// Runner is what a claim needs of a transport.
type Runner interface {
	Run(command string) (string, error)
	ReadFile(path string) (content string, found bool, err error)
	Describe() string
}

// Read reads the registry on the host t reaches. A host no deployment has
// claimed has none, which is an empty registry. Parse's *NoBuildError comes
// with the registry as read, as Parse returns it.
func Read(t Runner) (Registry, error) {
	content, _, err := t.ReadFile(Path)
	if err != nil {
		return Registry{}, fmt.Errorf("%s: reading %s: %w", t.Describe(), Path, err)
	}
	r, err := Parse([]byte(content))
	if err != nil {
		return r, fmt.Errorf("%s: %w", t.Describe(), err)
	}
	return r, nil
}

func refused(t Runner, e Entry, why error) error {
	roles := e.Roles
	if roles == "" {
		roles = "none"
	}
	advice := "Two deployments with one token would share every name, directory and WireGuard interface. Use another host for one of them"
	var c Conflict
	if errors.As(why, &c) && len(c.Clashes) > 0 && !strings.HasPrefix(c.Clashes[0], "token") && !strings.HasPrefix(c.Clashes[0], "root") && !strings.HasPrefix(c.Clashes[0], "WireGuard interface") {
		switch {
		case strings.HasPrefix(c.Clashes[0], "the gateway role"):
			advice = "One host gives ports 80 and 443 to one deployment: its gateway binds them and its Caddy imports its deployment's caddy.d, and a site admitted with --public-ports holds them too. Use another host for one of them"
		case strings.HasPrefix(c.Clashes[0], "the data role"):
			advice = "One host runs one deployment's data site: it binds the Postgres and etcd ports and holds the cluster's storage. Use another host for one of the data sites"
		case strings.HasPrefix(c.Clashes[0], "WireGuard listen port"):
			advice = "Two WireGuard interfaces cannot listen on one port. Give this site's endpoint another port in " + config.FileName + ", and forward that port where the host is behind NAT"
		default:
			advice = "Two meshes on overlapping subnets would route each other's traffic. A deployed mesh subnet never changes, so the deployment not yet applied here moves: if this one has never been applied, run `paisans init`, which picks a subnet clear of every host"
		}
	}
	return fmt.Errorf("%s: %v, so this deployment (token %s, interface %s, mesh %s, roles %s) cannot share the host with it, and nothing was changed. %s", t.Describe(), why, e.Token, e.Interface, e.Subnet, roles, advice)
}

// awkString escapes a value for an awk -v assignment, which processes
// backslash escapes: a backslash would otherwise be eaten.
func awkString(s string) string { return strings.ReplaceAll(s, `\`, `\\`) }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Remove is the registry without id's entry, which is what `site remove`
// leaves on a host once this deployment is gone from it. Every other entry is
// kept as it was, and an id with no entry changes nothing.
func Remove(r Registry, id string) Registry {
	out := Registry{Version: Version, Deployments: map[string]Entry{}}
	for k, v := range r.Deployments {
		if k != id {
			out.Deployments[k] = v
		}
	}
	return out
}

// removeProgram is Remove in awk, the counterpart of mergeProgram: it reads
// the registry in Encode's layout, drops the line whose key is id, and prints
// every other line in the order it read them, the last without its comma. A
// file in any other layout exits 4 and prints nothing, so it is never
// rewritten.
const removeProgram = `
NR == 1 { if ($0 != header) bad = 1; next }
done { if ($0 != "") bad = 1; next }
$0 == footer { done = 1; next }
{
	line = $0
	sub(/,$/, "", line)
	if (line !~ /^"[^"]+":\{.*\}$/) { bad = 1; next }
	key = substr(line, 2, index(line, "\":") - 2)
	if (key == id) next
	kept[++n] = line
}
END {
	if (NR > 0 && !done) bad = 1
	if (bad) { print unreadable > "/dev/stderr"; exit 4 }
	print header
	for (i = 1; i <= n; i++) {
		if (i < n) print kept[i] ","
		else print kept[i]
	}
	print footer
}
`

// removeArgs is the removal's awk argument list, unquoted.
func removeArgs(id string) []string {
	var args []string
	for _, kv := range [][2]string{{"id", id}, {"header", header}, {"footer", footer}, {"unreadable", unreadableMarker}} {
		args = append(args, "-v", kv[0]+"="+awkString(kv[1]))
	}
	return append(args, removeProgram)
}

// RemoveCommand is the one remote command that takes id's entry out of the
// host's registry, under the same flock as a claim, through a temporary file
// moved over it. A host with no registry is left without one, and a registry
// in another layout is left as it was.
func RemoveCommand(id string) string {
	quoted := make([]string, 0, 10)
	for _, a := range removeArgs(id) {
		quoted = append(quoted, shellQuote(a))
	}
	return strings.Join([]string{
		"set -e",
		"umask 077",
		"[ -f " + Path + " ] || exit 0",
		"exec 9>>" + LockPath,
		fmt.Sprintf("flock -w %d 9 || { echo 'paisans: another command holds %s'; exit 1; }", lockWait, LockPath),
		"tmp=$(mktemp " + Dir + "/.registry.XXXXXX)",
		`trap 'rm -f "$tmp"' EXIT`,
		"awk " + strings.Join(quoted, " ") + " " + Path + ` > "$tmp"`,
		`chmod 600 "$tmp"`,
		`mv "$tmp" ` + Path,
	}, "; ")
}

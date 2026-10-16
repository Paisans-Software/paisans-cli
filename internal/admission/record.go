package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// RecordVersion is the record's layout. A record of another version is
// refused rather than read in part.
const RecordVersion = 1

// Dir is where admission records live on a host: root's, mode DirMode, so
// every deployment's user reads its own record and none can write one.
// host admit creates it root-owned, and refuses to write if it is a link or
// not root's.
const Dir = "/etc/paisans"

// DirMode is Dir's mode: the deployment's user lists and reads it, only root
// writes it.
const DirMode = 0o755

// Mode is the record's mode. It is world readable because the deployment's
// own user reads it without sudo, and it holds nothing secret. The writer
// makes the file root-owned with this mode, writes it to a temporary file
// in Dir, fsyncs it and renames it over the record, so a reader never sees
// half a record, and never follows a link at Dir or at the target.
const Mode = 0o644

// ValidToken reports whether s is a deployment token: exactly
// deployment.TokenLength lower-case hex digits, which names no directory.
func ValidToken(s string) bool {
	if len(s) != deployment.TokenLength {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Path is the record of the deployment with token on a host,
// /etc/paisans/admitted.<token>.json. It refuses a token that is not a
// deployment token, so the writer, which runs as root, cannot be made to
// write elsewhere; it is the only way to build the path.
func Path(token string) (string, error) {
	if !ValidToken(token) {
		return "", fmt.Errorf("%q is not a deployment token: a token is %d lower-case hex digits", token, deployment.TokenLength)
	}
	return Dir + "/admitted." + token + ".json", nil
}

// MaxSize is the largest record ParseRecord reads: a record is under 2 KiB,
// so a larger file is not one.
const MaxSize = 64 * 1024

// ErrNotAdmitted is a host with no record for the deployment: its owner has
// not admitted it, or has evicted it.
var ErrNotAdmitted = errors.New("no admission record")

// Record is what host admit leaves on the host for one deployment: who it
// runs as and what the owner granted, so the deployment's commands, which
// cannot read root's files, learn it, and host deployments lists it.
type Record struct {
	Version    int    `json:"version"`
	Deployment string `json:"deployment"`
	User       string `json:"user"`
	// Site is the site's name in the configuration and Roles its roles,
	// sorted, as the request named them: what a command with no
	// configuration learns the host was admitted as.
	Site  string   `json:"site"`
	Roles []string `json:"roles"`
	// Grants are what the owner gave, which may be less than the site's
	// roles need: Needs(site).Missing(record.Grants) is what is lacking.
	Grants Grants `json:"grants"`
	// Forwarding is whether admit turned on net.ipv4.ip_forward for the
	// site, which it does for a site with an endpoint.
	Forwarding bool `json:"forwarding"`
	// Ports are what the firewall opened for the deployment.
	Ports Ports `json:"ports"`
	// Digest is the request's digest when admit ran from one, "" when it
	// ran from the configuration with --site.
	Digest string `json:"digest"`
	// AdmittedAt is when admit last ran with --execute, RFC 3339 in UTC.
	AdmittedAt string `json:"admitted_at"`
	// DataDir is the deployment's Docker data-root, <path>/paisans-<token>,
	// when the owner gave --data-dir; "" when it is under the user's home.
	DataDir string `json:"data_dir"`
	// BridgeNetfilter is whether the owner passed --bridge-netfilter, which
	// lets admit load br_netfilter on a host with bridges of its own.
	BridgeNetfilter bool `json:"bridge_netfilter"`
	// MeshLAN is the host's LAN prefix the owner let the mesh dial with
	// --mesh-lan, "" without it.
	MeshLAN string `json:"mesh_lan"`
	// WebServer is what admit found in front of a site with
	// web_server.managed_by: external, nil for every other site.
	WebServer *WebServer `json:"web_server"`
	// SystemDockerDisabled is whether Docker's system daemon is one admit
	// installed and disabled once, so that the deployments' own rootless
	// daemons were the only Docker running. It says what admit did, not
	// how the daemon stands now: nothing of paisans enables it again, and
	// an owner who has enabled it since keeps it so. It is false when the
	// owner had Docker before admit, which admit never touches.
	SystemDockerDisabled bool `json:"system_docker_disabled"`
}

// Ports are the ports the firewall opened: the SSH port, the WireGuard port
// (0 when it opened none), and 80 and 443 with public_ports.
type Ports struct {
	SSH       int   `json:"ssh"`
	WireGuard int   `json:"wireguard"`
	Public    []int `json:"public"`
}

// WebServer is the owner's web server as admit detected it, as root, since
// the deployment's user cannot: the system Docker socket, ss -p, nginx -T
// and /etc/letsencrypt are root's. web-server show fills its snippet from
// it. Every field is a string, so the record reads the same to any toolkit of
// its version.
type WebServer struct {
	// Server is caddy, nginx or apache, "" when not determined (Why says).
	Server string `json:"server"`
	// Container is the Docker container it runs in, Process the process
	// listening on 443 or 80 on the host; one is set.
	Container string `json:"container"`
	Process   string `json:"process"`
	// Config is its configuration's path on the host, Main the same as the
	// server reads it (inside the container, for one in a container).
	Config string `json:"config"`
	Main   string `json:"main"`
	// Certs is automatic, files or folder, "" when not determined.
	Certs    string `json:"certs"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	Folder   string `json:"folder"`
	Certbot  string `json:"certbot"`
	// AdminTCP is the TCP address a Caddy answers its admin API on, "" for
	// a unix socket, admin off, or a server that is not Caddy. Admit warns
	// when it is set: any local user can replace that Caddy's configuration.
	AdminTCP string `json:"admin_tcp"`
	Why      string `json:"why"`
}

// NewRecord is the record admit writes for req, with the grants the owner
// gave, at at. The ports follow from req and the grants; forwarding follows
// the endpoint. Admit sets DataDir, BridgeNetfilter, MeshLAN and WebServer
// itself.
func NewRecord(req Request, granted Grants, digest string, at time.Time) Record {
	public := publicPorts(granted)
	return Record{
		Version:    RecordVersion,
		Deployment: req.Deployment,
		User:       req.User,
		Site:       req.Site,
		Roles:      append([]string{}, req.Roles...),
		Grants:     granted,
		Forwarding: req.Endpoint,
		Ports:      Ports{SSH: req.SSHPort, WireGuard: req.ListenPort, Public: public},
		Digest:     digest,
		AdmittedAt: at.UTC().Format(time.RFC3339),
	}
}

// publicPorts is what the firewall opens for the public_ports grant: 80
// and 443 with it, nothing without it.
func publicPorts(g Grants) []int {
	if g.PublicPorts {
		return []int{80, 443}
	}
	return []int{}
}

// dataDirFor reports whether dir is a Docker data-root host admit sets for
// the deployment with token: an absolute path in its clean form, with no
// control character, ending in /paisans-<token>. The deployment's daemon is
// started with it, so nothing else is read as one.
func dataDirFor(dir, token string) bool {
	if _, bad := config.ControlRune(dir); bad {
		return false
	}
	return strings.HasPrefix(dir, "/") && path.Clean(dir) == dir && strings.HasSuffix(dir, "/paisans-"+token)
}

// privateRanges are the networks a home or office LAN is numbered from.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// MeshLAN reports whether s is a network --mesh-lan may open: an IPv4
// prefix in its masked form, a /16 or narrower, inside 10.0.0.0/8,
// 172.16.0.0/12 or 192.168.0.0/16. Nothing wider is the owner's own LAN.
func MeshLAN(s string) bool {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() || p.Masked() != p || p.String() != s || p.Bits() < 16 {
		return false
	}
	for _, r := range privateRanges {
		if r.Contains(p.Addr()) && p.Bits() >= r.Bits() {
			return true
		}
	}
	return false
}

// EncodeRecord is the record's file: indented JSON with a final newline, the
// only bytes ParseRecord accepts. The writer writes exactly these, with Mode,
// to Path(token).
func EncodeRecord(r Record) ([]byte, error) {
	if r.Roles == nil {
		r.Roles = []string{}
	}
	if r.Ports.Public == nil {
		r.Ports.Public = []int{}
	}
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the admission record: %w", err)
	}
	return append(out, '\n'), nil
}

// recordFields is every key a record holds; a record missing one was not
// written by a toolkit of its version.
var recordFields = []string{"version", "deployment", "user", "site", "roles", "grants", "forwarding", "ports", "digest", "admitted_at", "data_dir", "bridge_netfilter", "mesh_lan", "web_server", "system_docker_disabled"}

// ParseRecord reads a record strictly: its version, every field present and
// none unknown, a deployment whose token is token, the user a request for it
// may name, the public ports its grant opens, and a data_dir that is empty
// or this deployment's, and a mesh_lan that is empty or a LAN --mesh-lan
// may open.
func ParseRecord(data []byte, token string) (Record, error) {
	refuse := func(problems ...string) (Record, error) {
		return Record{}, &Error{What: "the admission record for " + strconv.Quote(token), Problems: problems}
	}
	if !ValidToken(token) {
		return refuse(fmt.Sprintf("%q is not a deployment token", token))
	}
	if len(data) > MaxSize {
		return refuse(fmt.Sprintf("it is %d bytes, and a record is at most %d", len(data), MaxSize))
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return refuse(fmt.Sprintf("it is not JSON (%s)", strconv.Quote(err.Error())))
	}
	raw, ok := keys["version"]
	if !ok {
		return refuse("version: missing")
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version != RecordVersion {
		return refuse(fmt.Sprintf("version: %s, and this paisans reads version %d records. Run the paisans release the host's owner admitted it with", strconv.Quote(string(raw)), RecordVersion))
	}
	var problems []string
	for _, k := range recordFields {
		if _, ok := keys[k]; !ok {
			problems = append(problems, k+": missing")
		}
	}
	if len(problems) > 0 {
		return refuse(problems...)
	}
	var r Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return refuse(fmt.Sprintf("a field is unknown or holds the wrong kind of value (%s)", strconv.Quote(err.Error())))
	}
	if !deployment.ValidID(r.Deployment) || (deployment.Deployment{ID: r.Deployment}).Token() != token {
		problems = append(problems, fmt.Sprintf("deployment: %q is not a deployment whose token is %q", r.Deployment, token))
	}
	if p := config.DeploymentUserProblem(r.User, r.Deployment); p != "" {
		problems = append(problems, "user: "+p)
	}
	if !config.ValidSiteName(r.Site) {
		problems = append(problems, fmt.Sprintf("site: %q is not a site name", r.Site))
	}
	roles := make([]config.Role, len(r.Roles))
	for i, role := range r.Roles {
		roles[i] = config.Role(role)
	}
	for i, role := range r.Roles {
		switch {
		case !config.KnownRole(roles[i]):
			problems = append(problems, fmt.Sprintf("roles[%d]: %q is not a site role", i, role))
		case config.RoleRepeated(roles, i):
			problems = append(problems, fmt.Sprintf("roles[%d]: %q is listed twice", i, role))
		case i > 0 && role < r.Roles[i-1]:
			problems = append(problems, fmt.Sprintf("roles[%d]: %q comes after %q, and roles are written sorted", i, role, r.Roles[i-1]))
		}
	}
	if r.Digest != "" {
		if d, err := ParseDigest(r.Digest); err != nil || d != r.Digest {
			problems = append(problems, fmt.Sprintf("digest: %q is not a digest as paisans writes one, sixteen lower-case characters in four groups of four", r.Digest))
		}
	}
	if at, err := time.Parse(time.RFC3339, r.AdmittedAt); err != nil || at.UTC().Format(time.RFC3339) != r.AdmittedAt {
		problems = append(problems, fmt.Sprintf("admitted_at: %q is not an RFC 3339 time in UTC", r.AdmittedAt))
	}
	if r.Ports.SSH < 1 || r.Ports.SSH > 65535 {
		problems = append(problems, fmt.Sprintf("ports.ssh: %d is not a port", r.Ports.SSH))
	}
	if r.Ports.WireGuard < 0 || r.Ports.WireGuard > 65535 {
		problems = append(problems, fmt.Sprintf("ports.wireguard: %d is not a port, or 0 for none", r.Ports.WireGuard))
	}
	// The firewall opens 80 and 443 with public_ports and nothing without
	// it, so the record holds exactly what NewRecord derives from the grant.
	if want := publicPorts(r.Grants); !slices.Equal(r.Ports.Public, want) {
		granted := "was granted"
		if !r.Grants.PublicPorts {
			granted = "was not granted"
		}
		problems = append(problems, fmt.Sprintf("ports.public: %v, and public_ports %s, so it is %v", r.Ports.Public, granted, want))
	}
	if r.DataDir != "" && !dataDirFor(r.DataDir, token) {
		problems = append(problems, fmt.Sprintf("data_dir: %q is not an absolute path in its clean form ending in /paisans-%s, or empty", r.DataDir, token))
	}
	if r.MeshLAN != "" && !MeshLAN(r.MeshLAN) {
		problems = append(problems, fmt.Sprintf("mesh_lan: %q is not an IPv4 network in its masked form, a /16 or narrower inside 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16, or empty", r.MeshLAN))
	}
	if len(problems) > 0 {
		return refuse(problems...)
	}
	// The same bytes EncodeRecord writes, so a repeated key, a key in
	// another case, a null, a missing nested field or a changed layout,
	// which the decoder above reads differently from jq or a person, is
	// refused rather than read.
	if want, err := EncodeRecord(r); err != nil || !bytes.Equal(want, data) {
		return refuse("it is not exactly as host admit wrote it: something changed it after it was written (a repeated or differently cased key, a null, spacing or line endings). Run host admit again")
	}
	return r, nil
}

// Reader reads a file on a host, as apply.Transport does.
type Reader interface {
	ReadFile(path string) (content string, found bool, err error)
}

// Read is the deployment's admission record on the host t reaches, read as
// whatever user t logs in as: the record is mode 644, so the deployment's
// own user reads it without sudo. ErrNotAdmitted when there is none.
func Read(t Reader, token string) (Record, error) {
	path, err := Path(token)
	if err != nil {
		return Record{}, err
	}
	content, found, err := t.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("reading the admission record %s: %w", path, err)
	}
	if !found {
		return Record{}, fmt.Errorf("%s: %w", path, ErrNotAdmitted)
	}
	return ParseRecord([]byte(content), token)
}

// ErrNotMine is a login with no admission record of its own on the host.
var ErrNotMine = errors.New("no admission record names this login")

// Runner is what Mine needs of a host: a command, and a file read.
type Runner interface {
	Reader
	Run(command string) (string, error)
}

// mineCommand prints the login's name, then every record's path. /etc/paisans
// is 755 and every record 644, so the deployment's user lists and reads them.
const mineCommand = `echo "user $(id -un)"; for f in ` + Dir + `/admitted.*.json; do [ -f "$f" ] && echo "$f"; done; true`

// Mine is the admission record of the deployment the login belongs to: the
// one whose user is the login's own name. A deployment's commands that run
// without its configuration (config recover) learn the deployment from it.
// A record that does not parse is skipped, since it cannot name the login.
func Mine(t Runner) (Record, error) {
	out, err := t.Run(mineCommand)
	if err != nil {
		return Record{}, fmt.Errorf("listing %s: %w", Dir, err)
	}
	var user string
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "user "); ok {
			user = name
		} else if line != "" {
			paths = append(paths, line)
		}
	}
	for _, p := range paths {
		token := strings.TrimSuffix(strings.TrimPrefix(p, Dir+"/admitted."), ".json")
		if !ValidToken(token) {
			continue
		}
		content, found, err := t.ReadFile(p)
		if err != nil || !found {
			continue
		}
		if rec, err := ParseRecord([]byte(content), token); err == nil && rec.User == user {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%s: %w (%s)", Dir, ErrNotMine, user)
}

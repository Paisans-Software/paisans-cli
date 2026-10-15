package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// RecordVersion is the record's layout. A record of another version is
// refused rather than read in part.
const RecordVersion = 1

// Dir is where admission records live on a host: root's, mode 755, so
// every deployment's user reads its own record and none can write one.
const Dir = "/etc/paisans"

// Mode is the record's mode. It is world readable because the deployment's
// own user reads it without sudo, and it holds nothing secret.
const Mode = 0o644

// Path is the record of the deployment with token on a host.
func Path(token string) string { return Dir + "/admitted." + token + ".json" }

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
	// WebServer is what admit found in front of a site with
	// web_server.managed_by: external, nil for every other site.
	WebServer *WebServer `json:"web_server"`
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
// it. Every field is a string or a list of strings, so the record reads the
// same to any toolkit of its version.
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
// the endpoint. Admit sets DataDir, BridgeNetfilter and WebServer itself.
func NewRecord(req Request, granted Grants, digest string, at time.Time) Record {
	public := []int{}
	if granted.PublicPorts {
		public = []int{80, 443}
	}
	return Record{
		Version:    RecordVersion,
		Deployment: req.Deployment,
		User:       req.User,
		Grants:     granted,
		Forwarding: req.Endpoint,
		Ports:      Ports{SSH: req.SSHPort, WireGuard: req.ListenPort, Public: public},
		Digest:     digest,
		AdmittedAt: at.UTC().Format(time.RFC3339),
	}
}

// EncodeRecord is the record's file: indented JSON with a final newline.
func EncodeRecord(r Record) []byte {
	if r.Ports.Public == nil {
		r.Ports.Public = []int{}
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	return append(out, '\n')
}

// recordFields is every key a record holds; a record missing one was not
// written by a toolkit of its version.
var recordFields = []string{"version", "deployment", "user", "grants", "forwarding", "ports", "digest", "admitted_at", "data_dir", "bridge_netfilter", "web_server"}

// ParseRecord reads a record strictly: its version, every field present and
// none unknown, a deployment whose token is token, and a user paisans
// accepts.
func ParseRecord(data []byte, token string) (Record, error) {
	refuse := func(problems ...string) (Record, error) {
		return Record{}, &Error{What: "the admission record " + Path(token), Problems: problems}
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
	if !config.ValidUserName(r.User) {
		problems = append(problems, fmt.Sprintf("user: %q is not a user name paisans accepts", r.User))
	}
	if r.Digest != "" {
		if d, err := ParseDigest(r.Digest); err != nil || d != r.Digest {
			problems = append(problems, fmt.Sprintf("digest: %q is not a digest as paisans writes one, sixteen lower-case characters in four groups of four", r.Digest))
		}
	}
	if at, err := time.Parse(time.RFC3339, r.AdmittedAt); err != nil || at.UTC().Format(time.RFC3339) != r.AdmittedAt {
		problems = append(problems, fmt.Sprintf("admitted_at: %q is not an RFC 3339 time in UTC", r.AdmittedAt))
	}
	if len(problems) > 0 {
		return refuse(problems...)
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
	content, found, err := t.ReadFile(Path(token))
	if err != nil {
		return Record{}, fmt.Errorf("reading the admission record %s: %w", Path(token), err)
	}
	if !found {
		return Record{}, fmt.Errorf("%s: %w", Path(token), ErrNotAdmitted)
	}
	return ParseRecord([]byte(content), token)
}

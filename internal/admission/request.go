package admission

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// RequestVersion is the request format this toolkit writes and reads. A
// request of any other version is refused, so an older CLI never admits a
// newer request on a partial reading of it.
const RequestVersion = 1

// Request is what host admit needs to admit one site of a deployment,
// without its configuration. It holds nothing secret.
type Request struct {
	Version    int    `yaml:"version"`
	Deployment string `yaml:"deployment"`
	User       string `yaml:"user"`
	SSHPort    int    `yaml:"ssh_port"`
	// ListenPort is the site's WireGuard port, 0 when no site dials it and
	// WireGuard picks one, so the firewall opens none.
	ListenPort int    `yaml:"listen_port"`
	MeshSubnet string `yaml:"mesh_subnet"`
	Address    string `yaml:"address"`
	// Endpoint is whether the site has one, so admit plans forwarding: such
	// a site relays mesh traffic between sites that have none.
	Endpoint bool   `yaml:"endpoint"`
	Grants   Grants `yaml:"grants"`
	// WebServer is each app the owner's web server is handed, for a site
	// with web_server.managed_by: external; empty for every other site.
	WebServer []WebApp   `yaml:"web_server"`
	Operators []Operator `yaml:"operators"`
}

// WebApp is one app behind the owner's web server: its public hostname and
// the address the web server proxies to.
type WebApp struct {
	App      string `yaml:"app"`
	Hostname string `yaml:"hostname"`
	Listen   string `yaml:"listen"`
}

// Operator is one operator the site's ssh.keys names, by their name under
// operators, and their public keys, each as type and base64 with no comment.
type Operator struct {
	Name string   `yaml:"name"`
	Keys []string `yaml:"keys"`
}

// Token is the deployment's token, which names everything admit creates.
func (r Request) Token() string { return deployment.Deployment{ID: r.Deployment}.Token() }

// Build is the request for one site of cfg. It refuses a site cfg does not
// declare, and a request Check refuses, so host request never prints one
// that host admit would refuse.
func Build(cfg *config.Config, site string) (Request, error) {
	s, ok := cfg.Sites[site]
	if !ok {
		return Request{}, fmt.Errorf("%s declares no site %q. Declared sites are %s", cfg.Path, site, strings.Join(cfg.SiteNames(), ", "))
	}
	r := Request{
		Version:    RequestVersion,
		Deployment: cfg.ID,
		User:       s.SSHUser(),
		SSHPort:    s.SSH.PortOrDefault(),
		ListenPort: s.ListenPort(),
		MeshSubnet: cfg.Mesh.Subnet,
		Address:    s.Address,
		Endpoint:   s.Endpoint != "",
		Grants:     Needs(s),
		WebServer:  []WebApp{},
		Operators:  []Operator{},
	}
	if s.Has(config.RoleMonitor) && s.WebServerManagedBy() == config.ManagedByExternal && s.WebServer != nil {
		for _, app := range cfg.PinnedTo(site) {
			r.WebServer = append(r.WebServer, WebApp{App: app, Hostname: cfg.Apps[app].Hostname, Listen: s.WebServer.Listen})
		}
	}
	for _, nk := range s.SSH.Keys {
		key, err := canonicalKey(nk.Line)
		if err != nil {
			return Request{}, fmt.Errorf("operators.%s.keys: %w", nk.Name, err)
		}
		if n := len(r.Operators); n == 0 || r.Operators[n-1].Name != nk.Name {
			r.Operators = append(r.Operators, Operator{Name: nk.Name})
		}
		last := &r.Operators[len(r.Operators)-1]
		last.Keys = append(last.Keys, key)
	}
	if problems := r.Check(); len(problems) > 0 {
		return Request{}, &Error{What: "the request for " + site, Problems: problems}
	}
	return r, nil
}

// canonicalKey is a public key line as type and base64 alone: its comment
// dropped, since a request carries nothing the owner does not need and free
// text is what a forger varies to search for a matching digest.
func canonicalKey(line string) (string, error) {
	pub, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	switch {
	case err != nil:
		return "", fmt.Errorf("not an OpenSSH public key (%v)", err)
	case len(options) > 0:
		return "", fmt.Errorf("carries options (%s)", strings.Join(options, ","))
	case len(strings.TrimSpace(string(rest))) > 0:
		return "", fmt.Errorf("more than one key on the line")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

// forbiddenMesh is what a mesh subnet may not overlap. Admit's egress table
// accepts the mesh subnet before it drops loopback and link-local, so a
// subnet over either would let the deployment reach the owner's local
// services and the cloud metadata address; this network, multicast and the
// reserved range route nothing a mesh could use.
var forbiddenMesh = []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"}

// Check is every problem with r, each naming its field. Parse refuses a
// request with any, and Build never returns one.
func (r Request) Check() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if r.Version != RequestVersion {
		add("version: %d, and this paisans reads version %d requests", r.Version, RequestVersion)
	}
	if !deployment.ValidID(r.Deployment) {
		add("deployment: %q is not a deployment id", r.Deployment)
	}
	own := deployment.Deployment{ID: r.Deployment}.Prefix()
	switch {
	case !config.ValidUserName(r.User):
		add("user: %q is not a user name paisans accepts", r.User)
	case r.User == "root":
		add("user: root is never a deployment's user")
	case strings.HasPrefix(r.User, "paisans-") && deployment.ValidID(r.Deployment) && r.User != own:
		add("user: %s is another deployment's name; this deployment's own user is %s", r.User, own)
	}
	if r.SSHPort < 1 || r.SSHPort > 65535 {
		add("ssh_port: %d is not a port", r.SSHPort)
	}
	if r.ListenPort < 0 || r.ListenPort > 65535 {
		add("listen_port: %d is not a port, or 0 for none", r.ListenPort)
	}
	if r.Endpoint && r.ListenPort == 0 {
		add("listen_port: 0, but the site has an endpoint, which other sites dial on a fixed port")
	}
	ip, mesh, err := net.ParseCIDR(r.MeshSubnet)
	switch {
	case err != nil || ip.To4() == nil:
		add("mesh_subnet: %q is not an IPv4 network", r.MeshSubnet)
	case !ip.Equal(mesh.IP):
		add("mesh_subnet: %q is not written as its network, %s", r.MeshSubnet, mesh)
	default:
		for _, f := range forbiddenMesh {
			_, n, _ := net.ParseCIDR(f)
			if n.Contains(mesh.IP) || mesh.Contains(n.IP) {
				add("mesh_subnet: %s overlaps %s, which the deployment must never reach", r.MeshSubnet, f)
			}
		}
		if a := net.ParseIP(r.Address); a == nil || a.To4() == nil || !mesh.Contains(a) {
			add("address: %q is not an IPv4 address inside mesh_subnet %s", r.Address, r.MeshSubnet)
		}
	}
	apps := map[string]bool{}
	for i, w := range r.WebServer {
		switch {
		case !config.ValidAppName(w.App):
			add("web_server[%d].app: %q is not an app name", i, w.App)
		case apps[w.App]:
			add("web_server[%d].app: %s is listed twice", i, w.App)
		}
		apps[w.App] = true
		if !config.ValidHostname(w.Hostname) {
			add("web_server[%d].hostname: %q is not a hostname", i, w.Hostname)
		}
		if _, _, ok := (config.WebServer{Listen: w.Listen}).ListenHostPort(); !ok {
			add("web_server[%d].listen: %q is not an IPv4 address and a port", i, w.Listen)
		}
	}
	if len(r.Operators) == 0 {
		add("operators: none, so nobody could log in as %s", r.User)
	}
	names, keys := map[string]bool{}, map[string]string{}
	for i, o := range r.Operators {
		switch {
		case !config.ValidOperatorName(o.Name):
			add("operators[%d].name: %q is not an operator name", i, o.Name)
		case names[o.Name]:
			add("operators[%d].name: %s is listed twice", i, o.Name)
		}
		names[o.Name] = true
		if len(o.Keys) == 0 {
			add("operators[%d].keys: none for %s", i, o.Name)
		}
		for j, k := range o.Keys {
			canonical, err := canonicalKey(k)
			switch {
			case err != nil:
				add("operators[%d].keys[%d]: %v", i, j, err)
				continue
			case canonical != k:
				add("operators[%d].keys[%d]: not written as type and base64 alone, %s", i, j, canonical)
				continue
			}
			if first, dup := keys[k]; dup {
				add("operators[%d].keys[%d]: the same key as %s's", i, j, first)
			}
			keys[k] = o.Name
		}
	}
	return problems
}

// Error is a request, or a record, refused, with every problem found.
type Error struct {
	What     string
	Problems []string
}

func (e *Error) Error() string {
	return e.What + " is refused:\n- " + strings.Join(e.Problems, "\n- ")
}

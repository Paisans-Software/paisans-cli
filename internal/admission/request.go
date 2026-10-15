package admission

import (
	"errors"
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
		for i, p := range problems {
			problems[i] = r.withConfigKey(p, site)
		}
		return Request{}, &Error{What: "the request for " + site, Problems: problems}
	}
	return r, nil
}

// withConfigKey is a problem Check found, led by the configuration key the
// operator edits to fix it and then the request's field, Eg:
// sites.home-a.web_server.listen (web_server[0].listen): ...
func (r Request) withConfigKey(problem, site string) string {
	field, rest, ok := strings.Cut(problem, ": ")
	if !ok {
		return problem
	}
	var key string
	switch name, _, _ := strings.Cut(field, "["); {
	case field == "deployment":
		key = "id"
	case field == "user":
		key = "sites." + site + ".ssh.user"
	case field == "ssh_port":
		key = "sites." + site + ".ssh.port"
	case field == "listen_port":
		key = "sites." + site + ".endpoint"
	case field == "mesh_subnet":
		key = "mesh.subnet"
	case field == "address":
		key = "sites." + site + ".address"
	case name == "web_server":
		var i int
		if _, err := fmt.Sscanf(field, "web_server[%d]", &i); err == nil && i >= 0 && i < len(r.WebServer) {
			switch {
			case strings.HasSuffix(field, ".listen"):
				key = "sites." + site + ".web_server.listen"
			case strings.HasSuffix(field, ".hostname"):
				key = "apps." + r.WebServer[i].App + ".hostname"
			default:
				key = "apps." + r.WebServer[i].App
			}
		}
	case name == "operators":
		key = "sites." + site + ".ssh.keys"
		var i int
		if _, err := fmt.Sscanf(field, "operators[%d]", &i); err == nil && i >= 0 && i < len(r.Operators) {
			key = "operators." + r.Operators[i].Name
			if strings.Contains(field, ".keys") {
				key += ".keys"
			}
		}
	}
	if key == "" {
		return problem
	}
	return key + " (" + field + "): " + rest
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
		return "", fmt.Errorf("carries options (%q)", strings.Join(options, ","))
	case len(strings.TrimSpace(string(rest))) > 0:
		return "", fmt.Errorf("more than one key on the line")
	}
	if p := config.KeyTypeProblem(pub); p != "" {
		return "", errors.New(p)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

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
	if p := config.DeploymentUserProblem(r.User, r.Deployment); p != "" {
		add("user: %s", p)
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
	case mesh.String() != r.MeshSubnet && ip.Equal(mesh.IP):
		add("mesh_subnet: %q is not written as plain dotted IPv4, %s", r.MeshSubnet, mesh)
	case !ip.Equal(mesh.IP):
		add("mesh_subnet: %q is not written as its network, %s", r.MeshSubnet, mesh)
	default:
		if ones, _ := mesh.Mask.Size(); ones < config.MeshMinPrefix {
			add("mesh_subnet: %q is larger than a /%d, which is as large as a mesh may be", r.MeshSubnet, config.MeshMinPrefix)
		}
		for _, f := range config.MeshReservedOverlaps(mesh) {
			add("mesh_subnet: %s overlaps %s, which the deployment must never reach", r.MeshSubnet, f)
		}
		a := net.ParseIP(r.Address)
		switch {
		case a == nil || a.To4() == nil || !mesh.Contains(a):
			add("address: %q is not an IPv4 address inside mesh_subnet %s", r.Address, r.MeshSubnet)
		case a.String() != r.Address:
			add("address: %q is not written as plain dotted IPv4, %s", r.Address, a)
		case config.IsNetworkOrBroadcast(a, mesh):
			add("address: %s is the subnet's network or broadcast address", r.Address)
		}
	}
	apps, hostnames := map[string]bool{}, map[string]bool{}
	for i, w := range r.WebServer {
		switch {
		case !config.ValidAppName(w.App):
			add("web_server[%d].app: %q is not an app name", i, w.App)
		case apps[w.App]:
			add("web_server[%d].app: %s is listed twice", i, w.App)
		}
		apps[w.App] = true
		switch {
		case !config.ValidHostname(w.Hostname):
			add("web_server[%d].hostname: %q is not a hostname", i, w.Hostname)
		case hostnames[w.Hostname]:
			add("web_server[%d].hostname: %q is listed twice", i, w.Hostname)
		}
		hostnames[w.Hostname] = true
		if !config.ProxiesToThisHost(w.Listen, r.Address) {
			add("web_server[%d].listen: %q is not a loopback address or this site's mesh address, with a port", i, w.Listen)
		}
	}
	if len(r.Operators) == 0 {
		add("operators: none, so nobody could log in as %q", r.User)
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
			add("operators[%d].keys: none for %q", i, o.Name)
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
				add("operators[%d].keys[%d]: the same key as %q's", i, j, first)
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

package hostcheck

import (
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/config"
	"net"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Class is what a host is to the toolkit.
type Class int

const (
	// Clean is a host with nothing foreign on it: the toolkit's alone, or
	// about to be.
	Clean Class = iota
	// Shared is a host where something foreign runs and holds nothing the
	// site claims.
	Shared
	// Conflicted is a host where something foreign holds a claim.
	Conflicted
)

func (c Class) String() string {
	switch c {
	case Shared:
		return "shared"
	case Conflicted:
		return "conflict"
	}
	return "clean"
}

// Conflict is one claim something foreign holds.
type Conflict struct {
	// Resource is what is claimed, Eg: "*:80/tcp (Caddy)" or "interface psns-f2a9".
	Resource string
	// Key is the paisans-stack.yaml key that claims it.
	Key string
	// Holder is what holds it: a container and its compose project, or a
	// listener and its owner.
	Holder string
}

func (c Conflict) String() string {
	return fmt.Sprintf("%s: claimed by %s, held by %s", c.Resource, c.Key, c.Holder)
}

// Report is the host check's finding on one site's host.
type Report struct {
	Site, Host string
	Class      Class
	Conflicts  []Conflict
	// Foreign is everything found that is neither the toolkit's nor the base
	// system's, one line each. It is what makes a host shared.
	Foreign []string
	// Notes are things found that are neither foreign nor a conflict but
	// worth an operator's eye, one line each.
	Notes     []string
	Inventory *Inventory
}

// Shared reports whether the host is shared, so that a command touches only
// what is the toolkit's.
func (r *Report) Shared() bool { return r.Class == Shared }

// defaultNetworks are the networks every Docker install has.
var defaultNetworks = map[string]bool{"bridge": true, "host": true, "none": true}

// holder is who a socket belongs to.
type holder struct {
	ours, base bool
	desc       string
}

// owners decides what is the deployment's. Ownership is the deployment
// label and nothing else, as it is for prune: a compose project's name is
// whoever created it's choice, and two deployments on one host share every
// naming convention.
type owners struct {
	inv *Inventory
	id  string
	// iface and port are this deployment's WireGuard interface and listen
	// port, whose kernel socket is the toolkit's.
	iface string
	port  int
}

func (o owners) ours(label string) bool { return o.id != "" && label == o.id }

// labelled describes an object's compose project and, when it is another
// deployment's, which one.
func (o owners) labelled(project, owner string) string {
	if o.ours(owner) {
		owner = ""
	}
	desc := "no compose project"
	if project != "" {
		desc = "compose project " + project
	}
	if owner != "" {
		desc += ", paisans deployment " + owner
	}
	return desc
}

func (o owners) container(c Container) holder {
	return holder{ours: o.ours(c.Deployment), desc: "container " + c.Name + " (" + o.labelled(c.Project, c.Deployment) + ")"}
}

// socket decides who holds a listener, by its owner. Every process a
// rootless Docker runs, the daemon's port forwarder included, is the user's
// UID or one in its subordinate range, so that is this deployment's. The
// kernel's own WireGuard socket has no owner but root, and is the toolkit's
// when the toolkit wrote the interface it serves. A listener of root's or of
// a system user's (a UID below 1000, as the distribution's own daemons run
// as) is the base system's, and so is any on loopback; one of another user's
// makes the host shared.
func (o owners) socket(s Socket) holder {
	if o.inv.Self.Owns(s.UID) {
		return holder{ours: true, desc: "this deployment's user"}
	}
	if s.UID == 0 && s.Proto == "udp" && o.port != 0 && s.Port == o.port && o.inv.ManifestWireGuard && contains(o.inv.Links, o.iface) {
		return holder{ours: true, desc: "WireGuard (" + o.iface + ")"}
	}
	if s.UID == 0 {
		return holder{base: true, desc: "a listener of root's"}
	}
	if s.UID < firstLoginUID {
		return holder{base: true, desc: fmt.Sprintf("a listener of a system user's (uid %d)", s.UID)}
	}
	return holder{base: loopback(s.Address), desc: fmt.Sprintf("a listener of another user's (uid %d)", s.UID)}
}

// firstLoginUID is the lowest UID Debian and Ubuntu give a login user
// (UID_MIN in /etc/login.defs); every UID below it is a system account.
const firstLoginUID = 1000

// Classify decides who owns what the inventory found and compares it with
// the claims.
//
// A listener conflicts when the ports and protocols match and either side
// binds every address or both bind the same one, which is what stops a bind
// (render.Listener.Overlaps). That holds for the base system too: a
// loopback Postgres does not make a host shared, but the toolkit's own
// loopback bind on its port would fail.
func Classify(claims Claims, inv *Inventory) *Report {
	r := &Report{Site: claims.Site, Host: inv.Host, Inventory: inv}
	o := owners{inv: inv, id: claims.Deployment.ID, iface: claims.Interface, port: claims.ListenPort}
	seen := map[Conflict]bool{}
	conflict := func(c Conflict) {
		if !seen[c] {
			seen[c] = true
			r.Conflicts = append(r.Conflicts, c)
		}
	}

	for _, claim := range claims.Listeners {
		resource := fmt.Sprintf("%s (%s)", claim, claim.Owner)
		for _, s := range inv.Sockets {
			if h := o.socket(s); !h.ours && claim.Overlaps(s.Listener()) {
				conflict(Conflict{Resource: resource, Key: claim.Key, Holder: h.desc})
			}
		}
		for _, c := range inv.Containers {
			if o.ours(c.Deployment) {
				continue
			}
			for _, b := range c.Bindings {
				if claim.Overlaps(b) {
					conflict(Conflict{Resource: resource, Key: claim.Key, Holder: o.container(c).desc})
				}
			}
		}
	}

	if contains(inv.Links, claims.Interface) && !inv.ManifestWireGuard {
		conflict(Conflict{
			Resource: "interface " + claims.Interface,
			Key:      "mesh",
			Holder:   "an interface this deployment did not write (" + inv.ManifestPath + " records no " + claims.Deployment.WireGuardConf() + ")",
		})
	}

	// A Docker network's own bridge route is that network, judged by its
	// subnet below rather than a second time as a route.
	bridges := map[string]bool{"docker0": true}
	for _, n := range inv.Networks {
		if len(n.ID) >= 12 {
			bridges["br-"+n.ID[:12]] = true
		}
		for _, subnet := range n.Subnets {
			if !o.ours(n.Deployment) && overlapsMesh(claims.Mesh, subnet) {
				conflict(Conflict{Resource: "subnet " + subnet, Key: "mesh.subnet", Holder: o.networkDesc(n)})
			}
			// A pinned network may be held by its own project and nothing
			// else. One of the toolkit's other stacks holding it, such as
			// the one an app left behind when it was renamed, makes compose
			// refuse the new network mid apply, so it is a conflict too,
			// naming the stack to take down.
			for _, pinned := range claims.Networks {
				if n.Project == pinned.Project || !overlapsMesh(pinned.Net, subnet) {
					continue
				}
				holder := o.networkDesc(n)
				if o.ours(n.Deployment) {
					holder += fmt.Sprintf(", a stack of this deployment's that %s does not run; if nothing uses it any more, remove it with `docker compose -p %s down`", claims.Site, n.Project)
				}
				conflict(Conflict{Resource: "subnet " + subnet, Key: pinned.Key, Holder: holder})
			}
		}
	}
	// A route equal to the mesh or inside it captures mesh traffic: the
	// kernel picks the longest matching prefix, and it is at least as long
	// as the mesh interface's. A broader one (a provider's 10.0.0.0/8 private
	// network) is shorter than the mesh interface's route, so the mesh still
	// wins, and it is noted.
	// The local table's entries are the host's own addresses, and a throw
	// route only sends a lookup on to the next table: neither holds a
	// network.
	for _, route := range inv.Routes {
		if route.Dst == "default" || route.Dev == claims.Interface || bridges[route.Dev] || route.Type == "local" || route.Type == "broadcast" || route.Type == "throw" {
			continue
		}
		n, ok := network(route.Dst)
		if !ok {
			continue
		}
		resource := "route " + route.Dst + " dev " + route.Dev
		if route.Table != "" && route.Table != "main" {
			resource += " table " + route.Table
		}
		// A pinned network's bridge route is more specific than any broader
		// route, as the mesh interface's is, so only one equal to it or inside it captures
		// its traffic.
		for _, pinned := range claims.Networks {
			if within(n, pinned.Net) {
				conflict(Conflict{Resource: resource, Key: pinned.Key, Holder: "the host's routing table"})
			}
		}
		if !(n.Contains(claims.Mesh.IP) || claims.Mesh.Contains(n.IP)) {
			continue
		}
		if within(n, claims.Mesh) {
			conflict(Conflict{Resource: resource, Key: "mesh.subnet", Holder: "the host's routing table"})
		} else {
			r.Notes = append(r.Notes, fmt.Sprintf("%s contains the mesh subnet %s; %s's route is more specific and takes the mesh's traffic", resource, claims.Mesh, claims.Interface))
		}
	}

	r.Foreign = foreign(o)
	switch {
	case len(r.Conflicts) > 0:
		r.Class = Conflicted
	case len(r.Foreign) > 0:
		r.Class = Shared
	}
	return r
}

// foreign lists what is neither the deployment's nor the base system's. An
// anonymous volume with no labels is neither: it may be a replaced paisans
// container's leftover or somebody else's, and on its own it says nothing
// about who else lives here.
func foreign(o owners) []string {
	var out []string
	for _, c := range o.inv.Containers {
		if h := o.container(c); !h.ours {
			out = append(out, h.desc)
		}
	}
	for _, s := range o.inv.Sockets {
		if h := o.socket(s); !h.ours && !h.base {
			out = append(out, fmt.Sprintf("%s held by %s", s.Listener(), h.desc))
		}
	}
	for _, n := range o.inv.Networks {
		if !o.ours(n.Deployment) && !(defaultNetworks[n.Name] && n.Project == "") {
			out = append(out, o.networkDesc(n))
		}
	}
	for _, v := range o.inv.Volumes {
		switch {
		case o.ours(v.Deployment), v.Project == "" && v.Deployment == "" && v.Anonymous:
		case v.Project != "" || v.Deployment != "":
			out = append(out, fmt.Sprintf("volume %s (%s)", v.Name, o.labelled(v.Project, v.Deployment)))
		default:
			out = append(out, fmt.Sprintf("volume %s (named, no compose project)", v.Name))
		}
	}
	return out
}

func (o owners) networkDesc(n Network) string {
	if n.Project == "" && n.Deployment == "" {
		return "docker network " + n.Name
	}
	return fmt.Sprintf("docker network %s (%s)", n.Name, o.labelled(n.Project, n.Deployment))
}

// overlapsMesh reports whether a network, as CIDR or as a bare address,
// overlaps the mesh subnet at all.
func overlapsMesh(mesh *net.IPNet, cidr string) bool {
	n, ok := network(cidr)
	return ok && (n.Contains(mesh.IP) || mesh.Contains(n.IP))
}

// within reports whether n equals the mesh, or another claimed network, or
// lies inside it.
func within(n, mesh *net.IPNet) bool {
	nOnes, _ := n.Mask.Size()
	meshOnes, _ := mesh.Mask.Size()
	return mesh.Contains(n.IP) && nOnes >= meshOnes
}

// network reads a CIDR, or a bare address as a host route.
func network(cidr string) (*net.IPNet, bool) {
	if !strings.Contains(cidr, "/") {
		if ip := net.ParseIP(cidr); ip != nil && ip.To4() == nil {
			cidr += "/128"
		} else {
			cidr += "/32"
		}
	}
	_, n, err := net.ParseCIDR(cidr)
	return n, err == nil
}

func loopback(address string) bool {
	ip := net.ParseIP(address)
	return ip != nil && ip.IsLoopback()
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// Refusal is the reason a command must not go on, or nil. A conflict is
// always refused, with no override: the operator moves what holds the claim
// or changes the configuration. A shared host is not refused: its firewall
// is host admit's.
//
// Before either, a Docker the toolkit cannot run is refused: a snap, or an
// engine or compose plugin older than MinEngine or MinCompose.
func (r *Report) Refusal() error {
	if err := dockerRefusal(r.Host, r.Inventory.Docker); err != nil {
		return err
	}
	if r.Class == Conflicted {
		// The conflicts are listed here rather than with the report's other
		// lines, which show only with --verbose: a refusal has to say what
		// to move at any verbosity.
		lines := make([]string, len(r.Conflicts))
		for i, c := range r.Conflicts {
			lines[i] = "- " + c.String()
		}
		things := fmt.Sprintf("%d things", len(r.Conflicts))
		if len(r.Conflicts) == 1 {
			things = "1 thing"
		}
		return &ui.Problem{
			Hint:    fmt.Sprintf("%s holds %s %s claims, so nothing was changed", r.Host, things, r.Site),
			Explain: strings.Join(lines, "\n") + "\nMove what holds each one, or change the " + config.FileName + " key it names, and run again.",
		}
	}
	return nil
}

// Show reports what the check found, under the host check's own line and
// only with --verbose, marked the way host prepare marks its plan: the class
// as a detail, then Docker, each note and, on a shared host,
// what sharing changes as done, and each foreign thing as done and not
// paisans. Conflicts are not among them: they are in Refusal's error, which
// prints in full.
func (r *Report) Show(rep ui.Reporter) {
	if !rep.Verbose() {
		return
	}
	rep.Detail("%s (%s) is %s", r.Site, r.Host, r.Class)
	inv := r.Inventory
	docker := "absent"
	if inv.Docker.Present {
		docker = inv.Docker.Version
		switch {
		case inv.Docker.ComposeWorks:
			docker += ", compose " + inv.Docker.Compose
		case inv.Docker.ComposeMissing:
			docker += ", `docker compose` does not work"
		}
	}
	if len(inv.Docker.Packages) > 0 {
		docker += " (" + strings.Join(inv.Docker.Packages, ", ") + ")"
	}
	rep.Step("docker").End(ui.OK, docker)
	for _, f := range r.Foreign {
		rep.Step(f).End(ui.OK, "not paisans")
	}
	for _, n := range r.Notes {
		rep.Step(n).End(ui.OK, "")
	}
	if r.Class == Shared {
		rep.Step("shared").End(ui.OK, "ufw's default policy and enabled state are left alone, superseded images are kept, and prune removes only volumes labelled with this deployment's id, as everywhere")
	}
}

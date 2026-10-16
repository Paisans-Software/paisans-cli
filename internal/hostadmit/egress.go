package hostadmit

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// planEgress writes the egress table and its unit and loads it, before
// linger starts the user's manager, so no container of the user's ever
// runs unfiltered. A changed table is loaded again in place, atomically,
// never by restarting the unit: its stop deletes the table, and a restart
// restarts the user's manager, which requires it.
func planEgress(p *planner) error {
	lan := ""
	if p.in.Flags.MeshLAN {
		var err error
		if lan, err = meshLAN(p.f.DefaultDev, p.f.LAN); err != nil {
			return err
		}
		p.warn("--mesh-lan: the deployment can send UDP to "+lan,
			"Its sites may then dial each other on your LAN, and the deployment's user, which writes each peer's endpoint, can send WireGuard packets to any address in "+lan+".")
	}
	p.record.MeshLAN = lan
	// Under systemd-resolved, /etc/resolv.conf names the stub alone, and a
	// rootless network's DNS may go to the stub's upstream servers instead.
	var resolv string
	for _, file := range []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"} {
		content, _, err := p.t.ReadFile(file)
		if err != nil {
			return shown(err)
		}
		resolv += content + "\n"
	}
	v4, v6 := resolvers(resolv)
	if len(v4)+len(v6) == 0 {
		p.warn("egress: /etc/resolv.conf names no resolver",
			"The deployment's containers will not resolve names until one is set and admit is run again.")
	}
	mark, err := p.d.FwMark()
	if err != nil {
		return err
	}
	subordinate, err := rangeOf(p)
	if err != nil {
		return err
	}
	provisional := !p.f.User.Exists
	user, unitName, tableName := p.in.Request.User, p.d.EgressUnit(), p.d.EgressTable()
	file, unitPath := egressFilePath(p.d), "/etc/systemd/system/"+unitName
	lan6 := onLink6(p.f, p.d.Interface(), p.in.Request.MeshSubnet)
	if len(lan6) > 0 {
		var names []string
		for _, pfx := range lan6 {
			names = append(names, pfx.String())
		}
		p.present("egress: the host's on-link IPv6 prefixes, dropped: " + strings.Join(names, ", "))
	}
	// A full-tunnel VPN routes such prefixes on its interface, and the
	// table then drops most of the deployment's IPv6, the mesh's endpoints
	// among them.
	for _, r := range p.f.OnLink6 {
		if slices.Contains(lan6, r.Prefix) && r.Prefix.Bits() < 32 {
			p.warn("egress: "+r.Prefix.String()+" on "+ui.Visible(r.Dev)+" is wider than a /32, and the deployment is kept off it",
				"It is an on-link route of the host's, so the egress table drops it for the deployment, which then reaches little of the IPv6 internet. A full-tunnel VPN adds such routes; if that is not meant, remove the route and admit again.")
		}
	}
	table, err := hostprep.EgressTable(hostprep.EgressData{
		Token: p.d.Token(), Table: tableName, Mark: fmt.Sprintf("%#x", mark),
		Users: uidOf(p) + ", " + subordinate, Mesh: p.in.Request.MeshSubnet, LAN: lan, Resolvers4: v4, Resolvers6: v6,
		Gateway: p.in.Flags.PublicPorts || len(p.in.Request.WebServer) > 0, PublicPorts: p.in.Flags.PublicPorts, LAN6: lan6,
	})
	if err != nil {
		return err
	}
	unit, err := hostprep.EgressUnit(hostprep.EgressUnitData{Token: p.d.Token(), User: user, Table: tableName, File: file, UID: uidOf(p)})
	if err != nil {
		return err
	}
	if d := p.f.Dirs[admission.Dir]; d.Present && !strings.HasPrefix(d.Mode, "d") {
		return fmt.Errorf("%s is %s, not a directory, and the egress table is written in it. Fix it and admit again. Nothing was changed", admission.Dir, ui.Visible(d.Mode))
	}
	for _, path := range []string{file, unitPath} {
		if p.f.Files[path].Link {
			return fmt.Errorf("%s is a symbolic link, and admit writes it as root. Remove the link and admit again. Nothing was changed", path)
		}
	}
	tableChanged := !asWritten(p.f.Files[file], table)
	unitChanged := !asWritten(p.f.Files[unitPath], unit)
	if tableChanged {
		// Written beside the file and checked by nft before it replaces
		// it, so a table nft refuses never becomes the one the next boot
		// loads.
		next := file + ".new"
		p.step(Step{Title: "write the egress table",
			Describe: "egress: keep " + user + ", its containers and its mesh off this host's loopback and your network (" + file + ", checked by nft before it replaces the one there)",
			File:     &hostprep.File{Path: next, Content: table, Mode: 0o644},
			Command:  "nft -c -f " + next + " && mv -f " + next + " " + file + " || { rm -f " + next + "; echo 'the egress table was not replaced'; exit 1; }",
			Kinds:    []string{"egress"}, Provisional: provisional})
	} else {
		p.present("egress: " + file + " as written")
	}
	if unitChanged || !p.f.EgressEnabled {
		p.step(Step{Title: "install the egress unit",
			Describe: "egress: load the table at boot, before " + user + "'s manager, which requires it (" + unitName + ")",
			File:     &hostprep.File{Path: unitPath, Content: unit, Mode: 0o644}, Command: "systemctl daemon-reload && systemctl reenable " + unitName, Kinds: []string{"egress"}, Provisional: provisional})
	} else {
		p.present("egress: " + unitName + " enabled")
	}
	// start leaves a unit that is already active as it is, and nft -f
	// replaces the table in one transaction, so the table loaded is the
	// one written whether the unit was running or not.
	load := "systemctl start " + unitName + " && nft -f " + file
	switch want := ruleCount(table); {
	case !p.f.EgressTable:
		p.step(Step{Title: "load the egress table", Describe: "egress: load " + tableName + " now", Command: load, Kinds: []string{"egress"}, Provisional: provisional})
	case tableChanged:
		p.step(Step{Title: "reload the egress table", Describe: "egress: replace " + tableName + " in one transaction", Command: load, Kinds: []string{"egress"}, Provisional: provisional})
	case p.f.EgressRules != want:
		// A table flushed or edited by hand holds other rules than the file.
		found := "an unread number of"
		if p.f.EgressRules >= 0 {
			found = fmt.Sprint(p.f.EgressRules)
		}
		p.step(Step{Title: "reload the egress table", Describe: fmt.Sprintf("egress: %s holds %s rules and %s holds %d; replace it in one transaction", tableName, found, file, want), Command: load, Kinds: []string{"egress"}, Provisional: provisional})
	default:
		p.present(fmt.Sprintf("egress: %s loaded, its %d rules as written", tableName, want))
	}
	return nil
}

// ruleCount is how many rules a table file holds: every line of a chain's
// body but its type line and comments, as nft -a lists one rule a line.
func ruleCount(table string) int {
	n, in := 0, false
	for _, line := range strings.Split(table, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "chain ") && strings.HasSuffix(l, "{"):
			in = true
		case in && l == "}":
			in = false
		case in && l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "type "):
			n++
		}
	}
	return n
}

// asWritten reports whether f is a file of root's, mode 644, holding content.
func asWritten(f FileState, content string) bool {
	return f.Present && !f.Link && f.Sum == sha(content) && f.Mode == "644" && f.UID == 0
}

// global6 is IPv6's global unicast space. Link-local and unique local
// addresses are outside it, and the table drops those already.
var global6 = netip.MustParsePrefix("2000::/3")

// onLink6 are the host's on-link IPv6 prefixes the deployment is kept off:
// each global unicast route with no gateway on any interface but loopback
// and the mesh's own (iface), which must stay reachable. The system
// Docker's bridges are kept: the deployment's own networks live in its
// user namespace and never show in the host's table, so they are the
// owner's containers. Routes rather than addresses, because a host with a
// DHCPv6 /128, or with an address it adds without a prefix route, still
// has its LAN's /64 as an on-link route. A prefix inside another kept is
// left out, since the wider one drops it, and the rest are sorted, so the
// same routes always render the same table.
func onLink6(f Facts, iface, mesh string) []netip.Prefix {
	meshPrefix, _ := netip.ParsePrefix(mesh)
	var kept []netip.Prefix
	for _, r := range f.OnLink6 {
		switch {
		case r.Dev == "lo", r.Dev == iface:
		case r.Prefix.Bits() < global6.Bits() || !global6.Contains(r.Prefix.Addr()), meshPrefix.IsValid() && meshPrefix.Overlaps(r.Prefix):
		default:
			kept = append(kept, r.Prefix)
		}
	}
	var out []netip.Prefix
	for _, p := range kept {
		covered := slices.ContainsFunc(kept, func(q netip.Prefix) bool {
			return q != p && q.Bits() <= p.Bits() && q.Contains(p.Addr())
		})
		if !covered && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return out
}

// resolvers are the nameservers resolv.conf names, by family, each once,
// in its plain form: an IPv6 address's zone is dropped, since nft names an
// address, not an interface.
func resolvers(resolv string) (v4, v6 []string) {
	for _, line := range strings.Split(resolv, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		a, err := netip.ParseAddr(f[1])
		if err != nil {
			continue
		}
		a = a.WithZone("").Unmap()
		list := &v6
		if a.Is4() {
			list = &v4
		}
		if s := a.String(); !slices.Contains(*list, s) {
			*list = append(*list, s)
		}
	}
	return v4, v6
}

// meshLAN is the private prefix of the host's own LAN: the network of the
// first global IPv4 address on the default route's interface, refused
// unless it is private and no wider than a /16, so --mesh-lan never opens
// more than the owner's own network.
func meshLAN(dev, addr string) (string, error) {
	if dev == "" || dev == "absent" || addr == "" {
		return "", fmt.Errorf("--mesh-lan: this host has no default route with an IPv4 address, so there is no LAN to let the mesh dial. Drop --mesh-lan. Nothing was changed")
	}
	pfx, err := netip.ParsePrefix(addr)
	if err != nil {
		return "", fmt.Errorf("--mesh-lan: %s's address %q is not a prefix. Nothing was changed", ui.Visible(dev), addr)
	}
	lan := pfx.Masked().String()
	if !admission.MeshLAN(lan) {
		return "", fmt.Errorf("--mesh-lan: %s is on %s, which is not a private network of a /16 or narrower, so there is no LAN of yours for the mesh to dial. Drop --mesh-lan. Nothing was changed", ui.Visible(dev), lan)
	}
	return lan, nil
}

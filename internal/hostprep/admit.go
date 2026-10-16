// What host admit reuses from host prepare, exported.
package hostprep

import (
	"fmt"
	"net/netip"
	"strings"
	"text/template"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// EgressData is what the egress table is rendered with: the deployment's
// token and table, the mesh's mark as nft writes it, the user's UID and
// subordinate range as an nft set's elements ("1001, 165536-231071"), the
// mesh subnet, the host's resolvers by family, and with --mesh-lan the
// host's own LAN prefix. Gateway is a site whose own Caddy, or whose
// owner's web server, answers 80 and 443 on the host's addresses, which the
// deployment may then reach. PublicPorts is the grant, the one case where
// the deployment's own Caddy holds UDP 443 and admit has checked no other
// process does, so only then do its UDP answers from 443 pass. LAN6 are
// the host's on-link IPv6 prefixes, dropped like the private ranges, since
// a home LAN's IPv6 is global.
type EgressData struct {
	Token, Table, Mark, Users, Mesh, LAN string
	Resolvers4, Resolvers6               []string
	Gateway, PublicPorts                 bool
	LAN6                                 []netip.Prefix
}

// EgressTable is the nftables file host admit loads for a deployment. An
// on-link prefix is rendered only when it is a valid, masked IPv6 prefix,
// so nothing but a CIDR ever reaches the table.
func EgressTable(d EgressData) (string, error) {
	var lan6 []string
	for _, p := range d.LAN6 {
		if !p.IsValid() || !p.Addr().Is6() || p.Addr().Is4In6() || p.Addr().Zone() != "" || p != p.Masked() {
			return "", fmt.Errorf("the on-link prefix %q is not a masked IPv6 prefix", p.String())
		}
		lan6 = append(lan6, p.String())
	}
	return snippetFuncs("snippets/egress.nft.tmpl", template.FuncMap{"join": strings.Join}, struct {
		EgressData
		OnLink6 []string
	}{d, lan6})
}

// EgressUnitData is what the egress unit is rendered with.
type EgressUnitData struct{ Token, User, Table, File, UID string }

// EgressUnit is the system unit that loads the egress table at boot.
func EgressUnit(d EgressUnitData) (string, error) {
	return snippet("snippets/paisans-egress.service.tmpl", d)
}

// PlanWatchdog is what host prepare plans for a data site's watchdog in
// mode auto: the device the host has, or softdog with its warning, after
// refusing a host where systemd or a watchdog daemon already holds it.
// Admit plans it whatever the site's mode, which a request does not carry;
// host prepare and doctor compare the mode with what admit found.
func PlanWatchdog(t Transport, d deployment.Deployment, profile Profile) (Section, error) {
	return planWatchdog(t, d, profile, config.Site{Roles: []config.Role{config.RoleData}, Watchdog: config.WatchdogAuto})
}

// AdmitRules are the inbound rules host admit gives a deployment: the SSH
// port first, as host prepare orders them; the mesh's UDP port when one is
// admitted; everything on the mesh interface; and 80 and 443 with the
// public ports grant. No br-+ rule: a rootless container's traffic to the
// mesh address leaves through the user's own process, not a host bridge.
func AdmitRules(d deployment.Deployment, sshPort, listenPort int, public bool) []Rule {
	rules := []Rule{{Port: sshPort, Proto: "tcp", Why: sshWhy, SSH: true}}
	if listenPort != 0 {
		rules = append(rules, Rule{Port: listenPort, Proto: "udp", Why: "WireGuard, the mesh"})
	}
	rules = append(rules, Rule{Interface: d.Interface(), Why: "the mesh: etcd, Patroni, Garage, HAProxy"})
	if public {
		rules = append(rules, Rule{Port: 80, Proto: "tcp", Why: "public ports, HTTP"}, Rule{Port: 443, Proto: "tcp", Why: "public ports, HTTPS"})
	}
	return rules
}

// OwnedKey is one line of a deployment's authorized_keys sidecar: Mark is
// added for a key the deployment appended, shared for one another
// deployment appended that this one also needs.
type OwnedKey struct{ Mark, Fingerprint, Name string }

// ParseOwnedKeys reads a sidecar, skipping any line that is not a claim, as
// host prepare reads one.
func ParseOwnedKeys(content string) []OwnedKey {
	var out []OwnedKey
	for _, e := range parseOwned(content) {
		out = append(out, OwnedKey{Mark: e.mark, Fingerprint: e.fingerprint, Name: e.name})
	}
	return out
}

// OwnedKeysFile is a sidecar holding keys for user's authorized_keys,
// exactly as host prepare writes one.
func OwnedKeysFile(user string, keys []OwnedKey) string {
	entries := make([]ownedEntry, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, ownedEntry{mark: k.Mark, fingerprint: k.Fingerprint, name: k.Name})
	}
	return ownedBody(user, entries)
}

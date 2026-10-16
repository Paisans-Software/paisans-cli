// Package deployment is a deployment's identity and every name and path
// derived from it. It is the one place those are spelled, so that a compose
// project, a bind mount, a firewall comment and a registry entry can never
// disagree about which deployment they belong to.
//
// A deployment is identified by its id, a random UUID written into
// paisans-stack.yaml by `paisans init` and never changed afterwards. Names carry the
// token, the id's first four hex digits, because a name is read by people and
// a full UUID in every container name is noise. The token is a label for
// humans; the id is the identity. Code that decides whether a Docker object
// belongs to this deployment matches the id in a label, never a name prefix.
// The host registry (internal/registry) is what keeps two deployments on one
// host from sharing a token, and so from sharing a name or a root.
package deployment

import (
	"crypto/rand"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Label is the Docker label every rendered service and network carries, with
// the deployment's id as its value.
const Label = "community.paisans.deployment"

// Base is the directory every deployment's root sits under on a host.
const Base = "/srv/paisans"

// TokenLength is how many hex digits of the id the token keeps.
const TokenLength = 4

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ValidID reports whether s is a version 4 UUID in canonical, lowercase form.
// Lowercase only, because the id is compared as a string everywhere (a label
// value, a registry key) and two spellings of one id would be two deployments.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// NewID returns a fresh version 4 UUID from crypto/rand.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a deployment id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Deployment is one deployment, by id. Its zero value has an empty token and
// is never valid to render or apply; config.Load refuses a file without an id.
type Deployment struct {
	ID string
}

// Token is the first TokenLength hex digits of the id.
func (d Deployment) Token() string {
	if len(d.ID) < TokenLength {
		return d.ID
	}
	return d.ID[:TokenLength]
}

// Prefix is what every name this deployment owns starts with: paisans-<token>.
func (d Deployment) Prefix() string { return "paisans-" + d.Token() }

// Project is the compose project name of one stack, paisans-<token>-<stack>.
// Containers are named after it by compose, Eg: paisans-f2a9-talk-app-1.
func (d Deployment) Project(stack string) string { return d.Prefix() + "-" + stack }

// Root is the absolute directory everything apply renders for this deployment
// lands under on a host, /srv/paisans/<token>.
func (d Deployment) Root() string { return Base + "/" + d.Token() }

// Rel is Root without its leading slash: the form a rendered file's path
// takes, since rendered paths are relative to the host's /.
func (d Deployment) Rel() string { return strings.TrimPrefix(d.Root(), "/") }

// Dir is one stack's directory, /srv/paisans/<token>/<stack>.
func (d Deployment) Dir(stack string) string { return d.Root() + "/" + stack }

// Compose is one stack's compose file.
func (d Deployment) Compose(stack string) string { return d.Dir(stack) + "/compose.yaml" }

// ComposeCmd is `docker compose -f <stack's compose file>`, the start of every
// command run against one stack.
func (d Deployment) ComposeCmd(stack string) string {
	return "docker compose -f " + d.Compose(stack)
}

// Path joins elements under the root, Eg: Path("infra", "patroni.env").
func (d Deployment) Path(elem ...string) string {
	return path.Join(append([]string{d.Root()}, elem...)...)
}

// RelPath is Path in rendered form, without the leading slash.
func (d Deployment) RelPath(elem ...string) string {
	return strings.TrimPrefix(d.Path(elem...), "/")
}

// InterfacePrefix is what every deployment's WireGuard interface name starts
// with.
const InterfacePrefix = "psns-"

// MaxInterfaceName is the longest interface name Linux accepts: IFNAMSIZ is
// 16 bytes including the terminating NUL (Linux v6.8,
// include/uapi/linux/if.h, `#define IFNAMSIZ 16`).
const MaxInterfaceName = 15

// Interface is this deployment's WireGuard interface, psns-<token>. Each
// deployment on a host has its own, because one interface carries one
// address, one key and one peer list, and a second deployment's peers in the
// first one's file would be routed by the wrong mesh. It is nine characters,
// under MaxInterfaceName with room to spare.
func (d Deployment) Interface() string { return InterfacePrefix + d.Token() }

// WireGuardConf is the interface's wg-quick file as apply renders and writes
// it, relative to the host's /: srv/paisans/<token>/psns-<token>.conf. It is
// the deployment's own copy, in its own directory, because the deployment's
// user can neither write /etc/wireguard nor read the file there; apply hands
// it to the WireGuard helper, which checks it and installs it as
// WireGuardInstalled. It sits directly under the root, so it is no stack's.
func (d Deployment) WireGuardConf() string {
	return d.RelPath(d.Interface() + ".conf")
}

// WireGuardInstalled is where the WireGuard helper installs the interface's
// file for wg-quick, which names the interface after it:
// /etc/wireguard/psns-<token>.conf, root's, mode 600.
func (d Deployment) WireGuardInstalled() string {
	return "/etc/wireguard/" + d.Interface() + ".conf"
}

// WireGuardUnit is the systemd unit wireguard-tools ships to bring the
// interface up from that file, wg-quick@psns-<token>.
func (d Deployment) WireGuardUnit() string { return "wg-quick@" + d.Interface() }

// FwMark is the mark the deployment's WireGuard socket carries: the helper
// host admit installs adds it to every conf it installs, and the egress
// table drops what a socket with it sends to the host itself, loopback,
// link-local, multicast and the private ranges. Kernel WireGuard sends from
// no user, so the table's UID match cannot see the mesh's own packets, and
// the mark is how it sees them. 0x5053 ("PS") above the token: the registry
// keeps tokens unique on a host, so marks are too. A token that is not
// exactly TokenLength lower-case hex digits has no mark, so no bad token
// shares token 0000's.
//
// The high half 0x5053 and the token in the low half are chosen for Docker
// hosts. Kubernetes CNIs use overlapping bits (kube-proxy's 0x4000 and
// 0x8000, Calico's 0xffff0000 mask), so a host running Kubernetes is outside
// what admit supports.
func (d Deployment) FwMark() (uint32, error) {
	t := d.Token()
	if len(t) != TokenLength || strings.Trim(t, "0123456789abcdef") != "" {
		return 0, fmt.Errorf("deployment id %q has no token of %d lower-case hex digits to mark with", d.ID, TokenLength)
	}
	n, err := strconv.ParseUint(t, 16, 16)
	if err != nil {
		return 0, err
	}
	return 0x50530000 | uint32(n), nil
}

// EgressTable is the nftables table, in family inet, that keeps the
// deployment's user off the host's loopback and the owner's network.
func (d Deployment) EgressTable() string { return "paisans_" + d.Token() }

// EgressUnit is the system unit that loads EgressTable at boot, before the
// deployment user's manager starts.
func (d Deployment) EgressUnit() string { return d.Prefix() + "-egress.service" }

// Manifest is the toolkit's record of every file apply wrote for this
// deployment on a host.
func (d Deployment) Manifest() string { return d.Path(".paisans-manifest.json") }

// LabelFilter is the `docker ... --filter` argument selecting this
// deployment's objects by label.
func (d Deployment) LabelFilter() string { return "label=" + Label + "=" + d.ID }

// SplitRel splits a rendered path into its stack and the remainder within
// that stack, when it lies under some deployment's root:
// srv/paisans/f2a9/talk/.env is ("f2a9", "talk", ".env", true). A path
// with no stack, such as srv/paisans/f2a9/psns-f2a9.conf, or outside every
// root, such as etc/hosts, is not.
func SplitRel(rel string) (token, stack, rest string, ok bool) {
	base := strings.TrimPrefix(Base, "/") + "/"
	if !strings.HasPrefix(rel, base) {
		return "", "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(rel, base), "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

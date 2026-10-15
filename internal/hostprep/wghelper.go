package hostprep

import (
	"fmt"
	"net"
	"path"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// wgHelperData is what snippets/wg-helper.sh.tmpl is rendered with.
type wgHelperData struct {
	Interface, Path, Conf, Unit, Dir, Network string
	ListenPort, Prefix                        int
}

// WGHelperScript is the WireGuard helper host admit installs for d at
// apply.WGHelper's Path, mode 755: a POSIX sh script whose put checks a conf
// and installs it as d.WireGuardInstalled, and whose other verbs start,
// restart, sync, show and read the journal of d's interface. The interface,
// the listen port the owner admits (0 for a site nothing dials) and the mesh
// subnet are built in, so nothing the deployment's user passes it can widen
// them.
func WGHelperScript(d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(path.Dir(d.WireGuardInstalled()), d, listenPort, subnet)
}

func wgHelperScript(dir string, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	iface := d.Interface()
	helper, err := apply.WGHelper{Iface: iface}.Path()
	if err != nil {
		return "", err
	}
	if listenPort < 0 || listenPort > 65535 {
		return "", fmt.Errorf("the WireGuard helper for %s: listen port %d is not from 0 to 65535", iface, listenPort)
	}
	_, network, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("the WireGuard helper for %s: mesh subnet %q is not a network: %w", iface, subnet, err)
	}
	prefix, bits := network.Mask.Size()
	if bits != 32 || prefix < 1 {
		return "", fmt.Errorf("the WireGuard helper for %s: mesh subnet %q is not an IPv4 network with a prefix", iface, subnet)
	}
	// The helper accepts any route inside the subnet, so the subnet is held
	// to what a mesh is: no wider than a /24, and clear of this host
	// (0.0.0.0/8, 127.0.0.0/8), its link (169.254.0.0/16) and multicast and
	// reserved space (224.0.0.0/3). These are the rules of the admission
	// request check; keep the two in step.
	if ip := network.IP.To4(); prefix < 24 || ip[0] == 0 || ip[0] == 127 || ip[0] >= 224 || (ip[0] == 169 && ip[1] == 254) {
		return "", fmt.Errorf("the WireGuard helper for %s: mesh subnet %q is wider than a /24 or overlaps 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16 or 224.0.0.0/3", iface, subnet)
	}
	if !path.IsAbs(dir) || strings.ContainsAny(dir, "'\n") {
		return "", fmt.Errorf("the WireGuard helper for %s: %q cannot be its conf directory", iface, dir)
	}
	return snippet("snippets/wg-helper.sh.tmpl", wgHelperData{
		Interface:  iface,
		Path:       helper,
		Conf:       dir + "/" + iface + ".conf",
		Unit:       d.WireGuardUnit(),
		Dir:        dir,
		Network:    network.IP.String(),
		ListenPort: listenPort,
		Prefix:     prefix,
	})
}

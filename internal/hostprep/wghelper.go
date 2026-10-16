package hostprep

import (
	"fmt"
	"path"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// wgHelperData is what snippets/wg-helper.sh.tmpl is rendered with.
type wgHelperData struct {
	Interface, Path, Conf, Unit, Dir, Network, FwMark string
	ListenPort, Prefix, Owner                         int
}

// WGHelperScript is the WireGuard helper host admit installs for d at
// apply.WGHelper's Path, mode 755: a POSIX sh script whose put checks a conf
// and installs it as d.WireGuardInstalled, and whose other verbs start,
// restart, sync, show and read the journal of d's interface. The interface,
// the listen port the owner admits (0 for a site nothing dials) and the mesh
// subnet are built in, so nothing the deployment's user passes it can widen
// them. put adds d.FwMark to every conf it installs and refuses one that sets
// FwMark itself, so the egress table's mark chain sees the mesh's own packets.
func WGHelperScript(d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(path.Dir(d.WireGuardInstalled()), 0, d, listenPort, subnet)
}

// wgHelperScript renders the helper with its conf directory at dir, which
// put expects owner, a uid, to hold: root (0) on a host.
func wgHelperScript(dir string, owner int, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	iface := d.Interface()
	helper, err := apply.WGHelper{Iface: iface}.Path()
	if err != nil {
		return "", err
	}
	if listenPort < 0 || listenPort > 65535 {
		return "", fmt.Errorf("the WireGuard helper for %s: listen port %d is not from 0 to 65535", iface, listenPort)
	}
	// The helper accepts any route inside the subnet, so the subnet is held
	// to the rule the loader and a request hold a mesh to.
	network, problems := config.CheckMeshSubnet(subnet)
	if len(problems) > 0 {
		return "", fmt.Errorf("the WireGuard helper for %s: mesh subnet %q %s", iface, subnet, strings.Join(problems, "; "))
	}
	prefix, _ := network.Mask.Size()
	if owner < 0 {
		return "", fmt.Errorf("the WireGuard helper for %s: owner %d is not a uid", iface, owner)
	}
	if !path.IsAbs(dir) || strings.ContainsAny(dir, "'\n") {
		return "", fmt.Errorf("the WireGuard helper for %s: %q cannot be its conf directory", iface, dir)
	}
	mark, err := d.FwMark()
	if err != nil {
		return "", fmt.Errorf("the WireGuard helper for %s: %w", iface, err)
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
		Owner:      owner,
		FwMark:     fmt.Sprintf("%#08x", mark),
	})
}

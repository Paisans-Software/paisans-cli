package hostprep

import (
	"os"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// WGHelperScriptIn renders the WireGuard helper with its conf directory at
// dir in place of /etc/wireguard, so a test can run its put against a
// directory of its own. put expects that directory to be the test's own
// uid's, as it expects /etc/wireguard to be root's.
func WGHelperScriptIn(dir string, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(dir, os.Getuid(), d, listenPort, subnet)
}

// WGHelperScriptOwnedBy is WGHelperScriptIn with owner as the uid put
// expects to hold dir.
func WGHelperScriptOwnedBy(dir string, owner int, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(dir, owner, d, listenPort, subnet)
}

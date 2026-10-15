package hostprep

import "github.com/paisans-software/paisans-cli/internal/deployment"

// WGHelperScriptIn renders the WireGuard helper with its conf directory at
// dir in place of /etc/wireguard, so a test can run its put against a
// directory of its own.
func WGHelperScriptIn(dir string, d deployment.Deployment, listenPort int, subnet string) (string, error) {
	return wgHelperScript(dir, d, listenPort, subnet)
}

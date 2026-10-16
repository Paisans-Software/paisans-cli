package render

import "github.com/paisans-software/paisans-cli/internal/deployment"

// HostSitesDir is the gateway's directory of Caddy site blocks the host's
// owner adds for things paisans does not run, Eg: a blog beside the
// community: /srv/paisans/<token>/caddy.d, in the deployment's own root,
// created by apply as the deployment's user. The owner writes into it as
// that user (sudo -u <user> tee), never as root: the user owns the
// directory and could put a symlink in its place, and a root write would
// follow it. paisans never writes, records or removes a file there.
func HostSitesDir(d deployment.Deployment) string { return d.Path("caddy.d") }

// HostSitesMount is where the gateway's Caddy sees HostSitesDir.
const HostSitesMount = "/etc/caddy.d"

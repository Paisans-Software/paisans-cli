package hostprep

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// GatewayCaddyProbe prints "caddy <id> <state>" for each container of d's
// gateway Caddy on the host, running or not: the one labelled with d's id in
// its infrastructure project, service caddy. It prints nothing on a host
// without Docker, and fails when Docker is there and cannot be asked, so a
// failed probe is never read as no Caddy.
func GatewayCaddyProbe(d deployment.Deployment) string {
	return "if command -v docker >/dev/null 2>&1; then docker ps -a" +
		" --filter " + shellQuote(d.LabelFilter()) +
		" --filter " + shellQuote("label=com.docker.compose.project="+d.Project("infra")) +
		" --filter " + shellQuote("label=com.docker.compose.service=caddy") +
		` --format 'caddy {{.ID}} {{.State}}'; fi`
}

// CaddyContainer is one container GatewayCaddyProbe found.
type CaddyContainer struct{ ID, State string }

// ParseGatewayCaddy reads GatewayCaddyProbe's output.
func ParseGatewayCaddy(out string) []CaddyContainer {
	var found []CaddyContainer
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "caddy" {
			c := CaddyContainer{ID: f[1]}
			if len(f) > 2 {
				c.State = f[2]
			}
			found = append(found, c)
		}
	}
	return found
}

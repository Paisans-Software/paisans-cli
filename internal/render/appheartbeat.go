package render

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// A blueprint's own heartbeat (blueprints.Heartbeat): one push monitor per
// app in every monitor's seed, fed by the app's own stack, which pushes what
// it alone knows, as the operators heartbeat pushes the reconciler's answer
// (operatorsheartbeat.go). Every instance pushes to every monitor under the
// app's one token, apps.<app>.heartbeat_token, so a cluster placed app keeps
// one history whichever instance pushes. Paisans renders no pusher: the
// blueprint's templates are given .HeartbeatURLs and hand them to a service
// of the blueprint's that pushes.

// blueprintHeartbeatName is the seed's name for an app's heartbeat, "<app> —
// <name>", in the format of PublicCheckName.
func blueprintHeartbeatName(app, name string) string { return app + " — " + name }

// blueprintHeartbeatURLs is what an app's templates read as .HeartbeatURLs:
// the ping URL of every monitor the app pushes to (appHeartbeatTargets),
// space separated, under the app's token. Empty for a blueprint that declares
// no heartbeat, and in a deployment with no monitor. The token is required
// whether or not there is a monitor, so a monitor added later never meets an
// app with none.
func (p *planner) blueprintHeartbeatURLs(app string, blueprint config.Blueprint) (string, error) {
	if _, ok := blueprints.HeartbeatOf(blueprint); !ok {
		return "", nil
	}
	token, err := p.appHeartbeatToken(app, blueprints.HeartbeatTokenKey)
	if err != nil {
		return "", err
	}
	var urls []string
	for _, t := range appHeartbeatTargets(p.cfg) {
		urls = append(urls, HeartbeatURL(t.Hostname, token))
	}
	return strings.Join(urls, " "), nil
}

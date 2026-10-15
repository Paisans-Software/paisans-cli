package blueprints

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// A blueprint may declare a heartbeat: one push monitor per app in every
// monitor's seed, named "<app> — <name>", which the app's own stack feeds
// with what it alone knows (docs/specs/2026-10-14-wikijs-groups-and-drift.md,
// "A catalog blueprint may declare a heartbeat"). Everything else about it is
// Paisans': the token, generated with the app's other secrets and replaced by
// `secrets rotate`, the monitor's schedule, and the ping URL of every monitor,
// which the app's templates read as .HeartbeatURLs.

// HeartbeatTokenKey is the app secret a blueprint's heartbeat pushes with,
// apps.<app>.heartbeat_token, generated in the site heartbeat token's shape
// (hex16 is 32 lowercase hex characters).
const HeartbeatTokenKey = "heartbeat_token"

// Heartbeat is a blueprint's push monitor.
type Heartbeat struct {
	// Name is the monitor's name after the app's: "<app> — <Name>".
	Name string
}

type heartbeatSpec struct {
	Name string `yaml:"name"`
}

// heartbeatName is the shape of a heartbeat's name: lowercase words, as the
// toolkit's own monitors are named, with nothing that could read as another
// monitor's separator or site.
var heartbeatName = regexp.MustCompile(`^[a-z][a-z0-9-]*( [a-z0-9-]+)*$`)

// takenMonitorNames are the names after an app's that the seed already gives
// one of its own checks (internal/render/uptime.go), and after a site's that
// it gives a site's: the seed tells monitors apart by name, and an app may
// share its key with a site.
var takenMonitorNames = []string{"public", "gate", "signed fetch", "heartbeat", "ping"}

// def checks a heartbeat and the generated token that goes with it.
// apiVersion is the blueprint's, since heartbeat is read only from
// APIVersion on; generated is the secrets the blueprint declares itself.
func (h *heartbeatSpec) def(apiVersion string, generated []SecretSpec) (*Heartbeat, *SecretSpec, error) {
	if h == nil {
		return nil, nil, nil
	}
	if apiVersion != APIVersion {
		return nil, nil, fmt.Errorf("heartbeat: needs apiVersion %s, the format that adds it, so a toolkit that cannot read it skips the release", APIVersion)
	}
	if len(h.Name) > 40 || !heartbeatName.MatchString(h.Name) {
		return nil, nil, fmt.Errorf("heartbeat.name: is %q; it must be lowercase words of letters, digits and hyphens, at most 40 characters, since the monitor is named \"<app> — <name>\"", h.Name)
	}
	if slices.Contains(takenMonitorNames, h.Name) {
		return nil, nil, fmt.Errorf("heartbeat.name: %q is another monitor's name in the seed", h.Name)
	}
	if slices.ContainsFunc(generated, func(s SecretSpec) bool { return s.Name == HeartbeatTokenKey }) {
		return nil, nil, fmt.Errorf("secrets.generate: %s is the heartbeat's token, which Paisans generates for a blueprint that declares one", HeartbeatTokenKey)
	}
	return &Heartbeat{Name: h.Name}, &SecretSpec{Name: HeartbeatTokenKey, Format: FormatHex16}, nil
}

// HeartbeatOf is a blueprint's heartbeat, and false for one that declares
// none.
func HeartbeatOf(blueprint config.Blueprint) (Heartbeat, bool) {
	if h := of(blueprint).Heartbeat; h != nil {
		return *h, true
	}
	return Heartbeat{}, false
}

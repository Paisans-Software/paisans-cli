package kinds

import "github.com/paisans-software/paisans-stack/internal/config"

// Health is the route a monitor requests to learn whether an app is up, and
// the status codes that mean it is. Expect is the uptime fork's own syntax: a
// comma separated list of exact codes, because the fork has no ranges
// (src/lib/checker.js:55-61 at 1.1.0-oidc.1).
//
// A blueprint may have no route: then Path is empty, and the monitor checks
// only that the app answers (see the uptime seed). TCP is a blueprint that
// serves no HTTP at all, checked by a TCP connect alone.
type Health struct {
	Path   string
	Expect string
	TCP    bool
}

// HealthFor returns a kind's health route, false for a kind with none
// recorded. Each route was established from the pinned tag's source and,
// where its record says so, by running the pinned image. None of it is
// recalled. A kind with no health route of its own gets `/`, which is a
// deeper check than a health route (it renders a page) and is said so in its
// record.
func HealthFor(kind config.Kind) (Health, bool) {
	h := of(kind).Health
	return h, h.Path != ""
}

// HealthOf is a kind's health declaration whatever it holds, for the uptime
// seed, which checks a kind with no route by the fallback.
func HealthOf(kind config.Kind) Health { return of(kind).Health }

package kinds

import "github.com/paisans-software/paisans-stack/internal/config"

// Health is the route a monitor requests to learn whether an app is up, and
// the status codes that mean it is. Expect is the uptime fork's own syntax: a
// comma separated list of exact codes, because the fork has no ranges
// (src/lib/checker.js:55-61 at 1.1.0-oidc.1).
type Health struct {
	Path   string
	Expect string
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

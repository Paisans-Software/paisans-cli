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

// health was established on 2026-10-07 from each pinned tag's source and,
// where it says so, by running the pinned image. None of it is recalled. A
// kind with no health route of its own gets `/`, which is a deeper check than
// a health route (it renders a page) and is said so beside it.
var health = map[config.Kind]Health{
	// The gateway's members gate on 4181 is a second instance of the same
	// binary and is not checked separately. /ping answers before any auth and
	// needs no Host (pkg/middleware/healthcheck.go:44-53 at v7.15.4); ran the
	// image: 200 OK.
	config.KindOAuth2Proxy: {"/ping", "200"},
	// The static client has no health route; /version is a cheap file the
	// image serves. Ran v1.12.27: /version 200, /health 404.
	config.KindElement: {"/version", "200"},
	// The paisans fork's status-only route, from v1.15.0+paisans, the first
	// stack-1 release: GET or HEAD, 200 with an empty body when Postgres and
	// the cache answer, an empty 503 when either does not
	// (config/mbin_routes/health.yaml, HealthController.php:27-38). Its own
	// firewall has no security (security.yaml:34-37), so it answers without a
	// session on a private instance too.
	config.KindMbin: {"/healthz", "200"},
	// Runs SELECT 1 and pings Redis, 500 when either fails
	// (server/main.ts:78-105 at v1.10.0).
	config.KindOutline: {"/_health", "200"},
	// 204 No Content, not 200 (backend/internal/controller/
	// healthz_controller.go:18,30-31 at v2.14.0); ran the image.
	config.KindPocketID: {"/healthz", "204"},
	// Not /health, although Synapse has one (synapse/rest/health.py:38-50 at
	// v1.160.0): the gateway sends only /_matrix/* and /_synapse/* to the
	// homeserver and everything else to MAS, whose listener has no health
	// resource, so the public check would ask the wrong service. The client
	// versions endpoint answers without a token
	// (synapse/rest/client/versions.py:40,50-60) on both routes.
	config.KindSynapse: {"/_matrix/client/versions", "200"},
	// The writefreely-wisp fork's status-only route, from v0.21.1+wisp, the
	// first stack-1 release: GET or HEAD, 200 with an empty body when the
	// database answers SELECT 1, an empty 503 when it does not, with no
	// session or private mode check (healthz.go:19-50 at develop f95d840).
	// healthz is a reserved username, so no blog can claim the path
	// (author/author.go:59).
	config.KindWriteFreely: {"/healthz", "200"},
	// src/server.js:63 at 1.1.0-oidc.1. Recorded for completeness: a monitor
	// does not watch itself.
	config.KindUptime: {"/healthz", "200"},
}

// HealthFor returns a kind's health route, false for a kind with none
// recorded.
func HealthFor(kind config.Kind) (Health, bool) {
	h, ok := health[kind]
	return h, ok
}

package render_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

type seedFile struct {
	Settings map[string]any   `json:"settings"`
	Monitors []map[string]any `json:"monitors"`
}

// withMonitor is the fixture with its uptime app on the watch monitor site,
// the secrets it needs, and the given smtp blocks (deployment wide, then the
// app's own).
func withMonitor(t *testing.T, smtp config.SMTP, own *config.SMTP) (*config.Config, *config.Secrets) {
	t.Helper()
	cfg := fixture(t)
	cfg.SMTP = smtp
	cfg.Apps["health"] = config.App{Blueprint: config.BlueprintUptime, Hostname: "health.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"},
		Settings:  map[string]any{"admin_group": "admins"}, SMTP: own}
	secrets := fixtureSecrets(t)
	if secrets.Apps == nil {
		secrets.Apps = map[string]map[string]any{}
	}
	secrets.Apps["health"] = map[string]any{"admin_password": "fixture-admin", "session_secret": "fixture-session"}
	return cfg, secrets
}

func renderSeed(t *testing.T, cfg *config.Config, secrets *config.Secrets) seedFile {
	t.Helper()
	return renderSeedAt(t, cfg, secrets, "watch/srv/paisans/f2a9/health/monitors.json")
}

// renderSeedAt is renderSeed for the seed at path, for a deployment with more
// than one monitor.
func renderSeedAt(t *testing.T, cfg *config.Config, secrets *config.Secrets, path string) seedFile {
	t.Helper()
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if f.Path != path {
			continue
		}
		if f.Mode != 0o600 {
			t.Errorf("the seed carries the SMTP password and was rendered %o", f.Mode)
		}
		var seed seedFile
		if err := json.Unmarshal([]byte(f.Content), &seed); err != nil {
			t.Fatalf("the seed is not JSON: %v\n%s", err, f.Content)
		}
		return seed
	}
	t.Fatalf("no %s was rendered", path)
	return seedFile{}
}

func byName(seed seedFile) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, m := range seed.Monitors {
		out[m["name"].(string)] = m
	}
	return out
}

// Every app other than the monitor gets a check at its hostname and a direct
// check per site it runs on; every other site gets a ping; the monitor checks
// neither its own container nor its own site. talk is gated, so its check at
// its hostname is the gate check (see TestAGatedAppIsCheckedThroughTheGate).
func TestTheSeedChecksEveryAppTwiceAndPingsEveryOtherSite(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))

	talk := monitors["talk — gate"]
	if talk == nil || talk["url"] != "https://talk.example.org/" || talk["follow_redirects"] != false {
		t.Fatalf("talk's gate check: %v", talk)
	}
	// talk is behind the member gate: it redirects a request with no session
	// to sign in, before the app is asked, with a body only the gate sends.
	if talk["expected_string"] != render.GateMarker {
		t.Errorf("talk gate check expects %v", talk["expected_string"])
	}
	auth := monitors["auth — public"]
	if auth == nil || auth["url"] != "https://"+cfg.Apps["auth"].Hostname+"/healthz" || auth["expected_status"] != "204" {
		t.Errorf("pocket-id public check: %v", auth)
	}
	for _, site := range []string{"home-a", "home-b"} {
		d := monitors["talk — direct ("+site+")"]
		if d == nil {
			t.Fatalf("clustered talk has no direct check on %s", site)
		}
		if want := "http://" + cfg.Sites[site].Address + ":8080/healthz"; d["url"] != want {
			t.Errorf("direct %s: url %v, want %s", site, d["url"], want)
		}
		headers, _ := d["request_headers"].(map[string]any)
		if headers["Host"] != "talk.example.org" || headers["X-Forwarded-Proto"] != "https" {
			t.Errorf("direct %s: headers %v", site, headers)
		}
		if d["expected_status"] != "200" {
			t.Errorf("direct %s: a direct check bypasses the gate and expects the blueprint's own codes, got %v", site, d["expected_status"])
		}
		// talk is gated, so Mbin runs as a private instance, and its API
		// refuses a request with no token. The gate keeps anonymous /api/*
		// from reaching the app through the edge, so this is checked on the
		// app directly.
		api := monitors["talk — private API ("+site+")"]
		if want := "http://" + cfg.Sites[site].Address + ":8080/api/entries"; api == nil || api["url"] != want || api["expected_status"] != "401" || api["method"] != "GET" {
			t.Errorf("private API %s: %v, want GET %s expecting 401", site, api, want)
		}
		if headers, _ := api["request_headers"].(map[string]any); headers["Host"] != "talk.example.org" {
			t.Errorf("private API %s: headers %v", site, api["request_headers"])
		}
	}
	for _, name := range []string{"home-a — ping", "home-b — ping", "vm — ping"} {
		if m := monitors[name]; m == nil || m["monitor_type"] != "ping" {
			t.Errorf("%s: %v", name, m)
		}
	}
	if monitors["watch — ping"] != nil {
		t.Error("the monitor pings its own site")
	}
	for name, m := range monitors {
		if url, _ := m["url"].(string); strings.Contains(url, ":3001") {
			t.Errorf("the monitor checks its own container: %s %s", name, url)
		}
	}
}

// A gated app is checked through the edge three ways (docs/specs/
// 2026-10-08-visibility-gate.md). The gate check expects the gate's redirect
// to sign in, by its body, because a private app redirects `/` to its own
// sign-in page too. The public check reaches the app only when its health route is
// open, which a dedicated route is and `/` never is; Mbin's and WriteFreely's
// /healthz are dedicated. The signed fetch check expects the app to refuse an
// unsigned ActivityPub read. An ungated app keeps its public check and gets
// neither of the others.
func TestAGatedAppIsCheckedThroughTheGate(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))
	// Mbin's /healthz is a dedicated route, so it is open past the gate and
	// the public check reaches the app.
	if public := monitors["talk — public"]; public["url"] != "https://talk.example.org/healthz" || public["expected_status"] != "200" {
		t.Errorf("gated mbin public check is %v", public)
	}
	if gate := monitors["talk — gate"]; gate["check_type"] != "string" || gate["expected_string"] != render.GateMarker {
		t.Errorf("gated mbin gate check does not assert the gate's own redirect: %v", gate)
	}
	signed := monitors["talk — signed fetch"]
	if signed["expected_status"] != "401" || signed["url"] != "https://talk.example.org/" {
		t.Errorf("gated mbin signed fetch check is %v", signed)
	}
	if headers, _ := signed["request_headers"].(map[string]any); headers["Accept"] != "application/activity+json" {
		t.Errorf("signed fetch check does not ask for ActivityPub: %v", signed["request_headers"])
	}
	if got := monitors["wiki — public"]["expected_status"]; got != "200" {
		t.Errorf("ungated wikijs public expects %v", got)
	}
	if _, ok := monitors["wiki — gate"]; ok {
		t.Error("ungated wikijs has a gate check")
	}

	wiki := cfg.Apps["wiki"]
	wiki.Gated = false
	cfg.Apps["wiki"] = wiki
	if _, ok := byName(renderSeed(t, cfg, secrets))["wiki — gate"]; ok {
		t.Error("gated: false is no gate, yet wikijs has a gate check")
	}
	wiki.Gated = true
	cfg.Apps["wiki"] = wiki
	gated := byName(renderSeed(t, cfg, secrets))
	if got := gated["wiki — public"]["expected_status"]; got != "200" {
		t.Errorf("gated wikijs's dedicated health route is open, yet its public check expects %v", got)
	}
	if got := gated["wiki — gate"]["expected_string"]; got != render.GateMarker {
		t.Errorf("gated wikijs gate check expects %v", got)
	}
	if _, ok := gated["wiki — signed fetch"]; ok {
		t.Error("wikijs does not federate, yet has a signed fetch check")
	}
	if got := monitors["wiki — direct (home-a)"]["url"]; got != "http://"+cfg.Sites["home-a"].Address+":3000/healthz" {
		t.Errorf("wikijs direct url %v", got)
	}
}

// A gated WriteFreely is proved by its outbox: an ActivityPub only route that
// authorized_fetch refuses unsigned before any lookup, so the probe needs no
// blog to exist. Its /healthz is open past the gate, so it has a public check.
func TestAGatedWriteFreelyIsProvedByItsOutbox(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	blog := cfg.Apps["blog"]
	blog.Gated = true
	cfg.Apps["blog"] = blog
	monitors := byName(renderSeed(t, cfg, secrets))
	signed := monitors["blog — signed fetch"]
	if signed["url"] != "https://blog.example.org/api/collections/blog.example.org/outbox" || signed["expected_status"] != "401" {
		t.Errorf("gated writefreely signed fetch check is %v", signed)
	}
	if public := monitors["blog — public"]; public["url"] != "https://blog.example.org/healthz" || public["expected_status"] != "200" {
		t.Errorf("gated writefreely public check is %v", public)
	}
	for name := range monitors {
		if strings.HasPrefix(name, "blog — private API") {
			t.Errorf("writefreely has %s, which is Mbin's", name)
		}
	}
}

// An ungated Mbin is not a private instance, so its API answers anonymous
// reads by design and there is nothing to prove.
func TestAnUngatedMbinHasNoPrivateAPICheck(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	talk := cfg.Apps["talk"]
	talk.Gated = false
	cfg.Apps["talk"] = talk
	for name := range byName(renderSeed(t, cfg, secrets)) {
		if strings.Contains(name, "private API") {
			t.Errorf("ungated mbin has %s", name)
		}
	}
}

// The health page is switched off from the seed, and SMTP is carried only
// when a host resolves, with the app's own fields and password winning.
func TestTheSeedCarriesSettings(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	settings := renderSeed(t, cfg, secrets).Settings
	if len(settings) != 1 || settings["status_page_enabled"] != false {
		t.Fatalf("with no smtp the settings are %v", settings)
	}

	cfg, secrets = withMonitor(t,
		config.SMTP{Host: "smtp.example.org", Security: "starttls", Username: "robot", FromAddress: "hello@example.org", FromName: "Example"},
		&config.SMTP{Security: "tls", FromName: "Example Health"})
	secrets.External["smtp_password"] = "shared"
	secrets.Apps["health"]["smtp_password"] = "own"
	settings = renderSeed(t, cfg, secrets).Settings
	want := map[string]any{"status_page_enabled": false, "smtp_host": "smtp.example.org", "smtp_port": float64(465),
		"smtp_secure": true, "smtp_user": "robot", "smtp_pass": "own", "smtp_from_address": "hello@example.org", "smtp_from_name": "Example Health"}
	for k, v := range want {
		if settings[k] != v {
			t.Errorf("settings.%s = %v, want %v", k, settings[k], v)
		}
	}
}

// Monitors are named after app and site keys, never hostnames, so renaming a
// hostname updates a monitor in place instead of replacing it and losing the
// channels admins attached.
func TestMonitorNamesSurviveAHostnameChange(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	before := byName(renderSeed(t, cfg, secrets))
	talk := cfg.Apps["talk"]
	talk.Hostname = "forum.example.org"
	cfg.Apps["talk"] = talk
	after := byName(renderSeed(t, cfg, secrets))
	if len(before) != len(after) {
		t.Fatalf("%d monitors became %d", len(before), len(after))
	}
	for name := range before {
		if after[name] == nil {
			t.Errorf("%s disappeared", name)
		}
	}
	if after["talk — gate"]["url"] != "https://forum.example.org/" {
		t.Errorf("the renamed check still points at %v", after["talk — gate"]["url"])
	}
}

// The homeserver's public check must reach Synapse through the gateway, which
// routes only /_matrix/* and /_synapse/* to it and everything else to MAS,
// whose listener has no health resource.
func TestTheHomeserversChecksUseARouteTheGatewaySendsToSynapse(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))
	if got := monitors["chat — public"]["url"]; got != "https://chat.example.org/_matrix/client/versions" {
		t.Errorf("chat public url %v", got)
	}
	if got := monitors["chat — direct (vm)"]["url"]; got != "http://"+cfg.Sites["vm"].Address+":8008/_matrix/client/versions" {
		t.Errorf("chat direct url %v", got)
	}
}

// The edge refuses the token API, not the monitor's own UI: the dashboard's
// live refresh and the response time chart are session authenticated JSON
// under /api/sites (public/js/dashboard.js:115, site-detail.js:20 in the
// fork), while the token API is /api/v1/*.
func TestTheEdgeRefusesTheTokenAPIButNotTheUIsOwnJSON(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	var snippet string
	for _, f := range plan.Files {
		if f.Path == "watch/srv/paisans/f2a9/infra/caddy/snippets/health.caddy" {
			snippet = f.Content
		}
	}
	matcher := ""
	for _, line := range strings.Split(snippet, "\n") {
		if strings.HasPrefix(line, "@refused ") {
			matcher = line
		}
	}
	for _, want := range []string{"/api/v1/*", "/metrics", "/status*", "/badge/*"} {
		if !strings.Contains(matcher, " "+want) {
			t.Errorf("the edge does not refuse %s: %q", want, matcher)
		}
	}
	if strings.Contains(matcher, " /api/*") {
		t.Errorf("the edge refuses the UI's own /api/sites JSON: %q", matcher)
	}
}

// Every site Pocket ID runs on gets a check on its admin reconciler, straight to
// the reconciler's port on the mesh, and nothing public: the gateway does not
// route the reconciler. It asks /livez, the reconciler service's own health,
// which a standby passes too; the admins group's health is the push monitor's
// (TestTheSeedExpectsAnAdminsHeartbeatPerPocketID).
func TestEveryPocketIDSiteHasAnAdminReconcilerCheck(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))
	sites := render.AppSites(cfg)["auth"]
	if len(sites) == 0 {
		t.Fatal("the fixture runs Pocket ID nowhere")
	}
	for _, site := range sites {
		m := monitors["auth — admin reconciler ("+site+")"]
		if m == nil {
			t.Fatalf("no admin reconciler check on %s", site)
		}
		if want := "http://" + cfg.Sites[site].Address + ":1412/livez"; m["url"] != want || m["expected_status"] != "200" {
			t.Errorf("%s: url %v expects %v, want %s expecting 200", site, m["url"], m["expected_status"], want)
		}
		if _, ok := m["request_headers"]; ok {
			t.Errorf("%s: the reconciler needs no Host header: %v", site, m["request_headers"])
		}
	}
	for name := range monitors {
		if strings.Contains(name, "admin reconciler") && !strings.HasPrefix(name, "auth — admin reconciler (") {
			t.Errorf("an admin reconciler check for something other than Pocket ID: %s", name)
		}
	}
}

// A monitor does not watch itself: if it is down it cannot report that, and a
// check of its own container from inside it can only pass. So its own app has
// no check at its hostname and no direct check, while every other app keeps
// both.
func TestTheMonitorDoesNotCheckItself(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))
	for name, m := range monitors {
		if strings.HasPrefix(name, "health — ") {
			t.Errorf("the monitor seeds a check of itself: %s", name)
		}
		if url, _ := m["url"].(string); strings.Contains(url, "health.example.org") {
			t.Errorf("%s checks the monitor's own hostname: %s", name, url)
		}
	}
	for _, name := range []string{"auth — public", "talk — gate", "talk — direct (home-a)", "talk — direct (home-b)"} {
		if monitors[name] == nil {
			t.Errorf("%s is no longer seeded", name)
		}
	}
}

// With a second monitor on a second monitor site, each checks the other's
// public URL, so a monitor host that dies is noticed by the one still
// running. Neither checks itself.
func TestEachMonitorChecksTheOthersPublicURL(t *testing.T) {
	// The fixture's mail server, which its account app and the Pocket ID it
	// names cannot be validated without.
	cfg, secrets := withMonitor(t, fixture(t).SMTP, nil)
	watchB := cfg.Sites["watch"]
	watchB.Address = "10.44.0.5"
	watchB.Endpoint = "watch-b.example.org:51820"
	watchB.PublicAddress = "203.0.113.21"
	watchB.SSH.Host = "watch-b.example.org"
	cfg.Sites["watch-b"] = watchB
	cfg.Apps["health-b"] = config.App{Blueprint: config.BlueprintUptime, Hostname: "health-b.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch-b"},
		Settings:  map[string]any{"admin_group": "admins"}}
	secrets.Sites["watch-b"] = config.SiteSecrets{WireGuardPrivateKey: "REREREREREREREREREREREREREREREREREREREREREQ=", HeartbeatToken: "4444444444444444444444444444dddd"}
	secrets.Apps["health-b"] = map[string]any{"admin_password": "fixture-admin-b", "session_secret": "fixture-session-b"}
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("two monitors is a configuration the toolkit refuses: %v", refusals)
	}

	for self, other := range map[string]string{"health": "health-b", "health-b": "health"} {
		site := cfg.Apps[self].Placement.Site
		seed := renderSeedAt(t, cfg, secrets, site+"/srv/paisans/f2a9/"+self+"/monitors.json")
		count := map[string]int{}
		for _, m := range seed.Monitors {
			count[m["name"].(string)]++
		}
		if n := count[render.PublicCheckName(self)]; n != 0 {
			t.Errorf("%s checks its own public URL %d times", self, n)
		}
		if n := count[render.PublicCheckName(other)]; n != 1 {
			t.Errorf("%s checks %s's public URL %d times", self, other, n)
		}
		m := byName(seed)[render.PublicCheckName(other)]
		want := "https://" + cfg.Apps[other].Hostname + "/healthz"
		if m == nil || m["url"] != want || m["expected_status"] != "200" || m["follow_redirects"] != false ||
			m["monitor_type"] != "active" || m["interval_seconds"] != float64(60) {
			t.Errorf("%s's check of %s: %v", self, other, m)
		}
		pinged := false
		for _, m := range seed.Monitors {
			if m["monitor_type"] == "ping" && m["ping_host"] == cfg.Sites[cfg.Apps[other].Placement.Site].Address {
				pinged = true
			}
		}
		if !pinged {
			t.Errorf("%s does not ping %s's site", self, other)
		}
	}
}

// Two monitors on one monitor site share a host, so checking each other
// proves nothing about losing it, and neither checks itself: neither seeds a
// public check of a monitor. validate
// refuses this shape today (port-collision in mode paisans, and in mode
// external a monitor site hosts the monitor alone), but Build does not
// validate, so the seed holds the rule itself rather than leaning on that.
func TestMonitorsOnOneSiteDoNotCheckEachOther(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	cfg.Apps["health-b"] = config.App{Blueprint: config.BlueprintUptime, Hostname: "health-b.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"},
		Settings:  map[string]any{"admin_group": "admins"}}
	secrets.Apps["health-b"] = map[string]any{"admin_password": "fixture-admin-b", "session_secret": "fixture-session-b"}
	for self, other := range map[string]string{"health": "health-b", "health-b": "health"} {
		monitors := byName(renderSeedAt(t, cfg, secrets, "watch/srv/paisans/f2a9/"+self+"/monitors.json"))
		if monitors[render.PublicCheckName(self)] != nil {
			t.Errorf("%s checks its own public URL", self)
		}
		if monitors[render.PublicCheckName(other)] != nil {
			t.Errorf("%s checks %s, which shares its site", self, other)
		}
	}
}

// A deployment with one monitor seeds no uptime check: the only monitor is
// itself.
func TestASingleMonitorSeedsNoUptimeCheck(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	for _, m := range renderSeed(t, cfg, secrets).Monitors {
		if url, _ := m["url"].(string); strings.HasPrefix(url, "https://health") {
			t.Errorf("uptime check: %s", m["name"])
		}
	}
}

// The edge refuses /metrics/ as well as /metrics: Express routes both to the
// same handler, its routing being non strict. Caddy's path matcher is case
// insensitive, so /Metrics needs nothing more.
func TestTheEdgeRefusesMetricsWithATrailingSlash(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if f.Path == "watch/srv/paisans/f2a9/infra/caddy/snippets/health.caddy" {
			if !strings.Contains(f.Content, "@refused path /status* /badge/* /metrics /metrics/ /api/v1/*") {
				t.Fatalf("%s", f.Content)
			}
			return
		}
	}
	t.Fatal("no snippet")
}

// The reverse of the ping: every other site pushes a heartbeat to the
// monitor, and the seed expects one. The token is the site's own, from the
// secrets, so the monitor and the host that pushes agree on the URL without
// the fork inventing one. Interval 60 with grace 120, which the fork adds
// (src/lib/checker.js evaluateHeartbeat: tolerated = interval + grace), so a
// site is stale after 180 s: one missed push, and the next one late. The
// monitor's own site pushes to no one here, and so gets no heartbeat.
func TestTheSeedExpectsAHeartbeatFromEveryOtherSite(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))
	for _, site := range []string{"home-a", "home-b", "vm"} {
		m := monitors[site+" — heartbeat"]
		if m == nil {
			t.Fatalf("no heartbeat for %s", site)
		}
		token := secrets.Sites[site].HeartbeatToken
		if token == "" {
			t.Fatalf("the fixture secrets carry no heartbeat token for %s", site)
		}
		if m["monitor_type"] != "heartbeat" || m["heartbeat_token"] != token {
			t.Errorf("%s heartbeat: %v", site, m)
		}
		if m["heartbeat_schedule_kind"] != "interval" || m["interval_seconds"] != float64(60) || m["heartbeat_grace_seconds"] != float64(120) {
			t.Errorf("%s heartbeat schedule: %v", site, m)
		}
		if m["failure_threshold"] != float64(2) {
			t.Errorf("%s heartbeat threshold: %v", site, m["failure_threshold"])
		}
		if _, ok := m["url"]; ok {
			t.Errorf("%s heartbeat carries a url: %v", site, m)
		}
	}
	if monitors["watch — heartbeat"] != nil {
		t.Error("the monitor expects a heartbeat from its own site, which nothing pushes")
	}
}

// Every site but the monitor's own pushes: its infrastructure stack gains a
// heartbeat service whose URLs, token included, are in a 0600 env file and
// nowhere in the 0644 compose file. The monitor's own site pushes nothing with
// one monitor, and a role-less monitor site therefore still renders no infra
// stack.
func TestEveryWatchedSitePushesAHeartbeatToTheMonitor(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	for _, site := range []string{"home-a", "home-b", "vm"} {
		token := secrets.Sites[site].HeartbeatToken
		var env *render.File
		for i, f := range plan.Files {
			if f.Path == site+"/srv/paisans/f2a9/infra/heartbeat/heartbeat.env" {
				env = &plan.Files[i]
			}
		}
		if env == nil {
			t.Fatalf("%s renders no heartbeat.env", site)
		}
		if env.Mode != 0o600 {
			t.Errorf("%s: heartbeat.env carries the token and is rendered %o", site, env.Mode)
		}
		if want := "HEARTBEAT_URLS=https://health.example.org/ping/" + token + "\n"; !strings.Contains(env.Content, want) {
			t.Errorf("%s: heartbeat.env lacks %q:\n%s", site, want, env.Content)
		}
		compose := files[site+"/srv/paisans/f2a9/infra/compose.yaml"]
		if !strings.Contains(compose, "\n  heartbeat:\n") {
			t.Errorf("%s: the infrastructure stack has no heartbeat service:\n%s", site, compose)
		}
		if strings.Contains(compose, token) {
			t.Errorf("%s: the token is in the 0644 compose file", site)
		}
		if _, ok := files[site+"/srv/paisans/f2a9/infra/heartbeat/push.sh"]; !ok {
			t.Errorf("%s renders no push.sh", site)
		}
	}
	if _, ok := files["watch/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"]; ok {
		t.Error("the monitor's own site pushes a heartbeat, to itself")
	}
	if strings.Contains(files["watch/srv/paisans/f2a9/infra/compose.yaml"], "\n  heartbeat:\n") {
		t.Error("the monitor's own site runs a heartbeat service")
	}
}

// withTwoMonitors is withMonitor plus a second monitor site and app.
func withTwoMonitors(t *testing.T) (*config.Config, *config.Secrets) {
	t.Helper()
	// The fixture's mail server, which its account app and the Pocket ID it
	// names cannot be validated without.
	cfg, secrets := withMonitor(t, fixture(t).SMTP, nil)
	watchB := cfg.Sites["watch"]
	watchB.Address = "10.44.0.5"
	watchB.Endpoint = "watch-b.example.org:51820"
	watchB.PublicAddress = "203.0.113.21"
	watchB.SSH.Host = "watch-b.example.org"
	cfg.Sites["watch-b"] = watchB
	cfg.Apps["health-b"] = config.App{Blueprint: config.BlueprintUptime, Hostname: "health-b.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch-b"},
		Settings:  map[string]any{"admin_group": "admins"}}
	secrets.Sites["watch-b"] = config.SiteSecrets{WireGuardPrivateKey: "REREREREREREREREREREREREREREREREREREREREREQ=", HeartbeatToken: "4444444444444444444444444444dddd"}
	secrets.Apps["health-b"] = map[string]any{"admin_password": "fixture-admin-b", "session_secret": "fixture-session-b"}
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("two monitors is a configuration the toolkit refuses: %v", refusals)
	}
	return cfg, secrets
}

// With two monitors each expects the other's site to push, and every site
// pushes to every monitor off its own site: a host's env lists both URLs with
// the one token, in app order, and each monitor site lists the other's alone.
// One token per site serves both because the fork keeps tokens unique within
// one instance only (src/lib/sitePayload.js insertSite).
func TestTwoMonitorsCrossCoverHeartbeats(t *testing.T) {
	cfg, secrets := withTwoMonitors(t)
	for self, other := range map[string]string{"health": "health-b", "health-b": "health"} {
		site := cfg.Apps[self].Placement.Site
		otherSite := cfg.Apps[other].Placement.Site
		monitors := byName(renderSeedAt(t, cfg, secrets, site+"/srv/paisans/f2a9/"+self+"/monitors.json"))
		m := monitors[otherSite+" — heartbeat"]
		if m == nil || m["heartbeat_token"] != secrets.Sites[otherSite].HeartbeatToken {
			t.Errorf("%s does not expect %s's heartbeat: %v", self, otherSite, m)
		}
		if monitors[site+" — heartbeat"] != nil {
			t.Errorf("%s expects a heartbeat from its own site", self)
		}
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	token := secrets.Sites["home-a"].HeartbeatToken
	if want := "HEARTBEAT_URLS=https://health.example.org/ping/" + token + " https://health-b.example.org/ping/" + token + "\n"; !strings.Contains(files["home-a/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"], want) {
		t.Errorf("home-a pushes to %q, want %q", files["home-a/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"], want)
	}
	if want := "HEARTBEAT_URLS=https://health-b.example.org/ping/" + secrets.Sites["watch"].HeartbeatToken + "\n"; !strings.Contains(files["watch/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"], want) {
		t.Errorf("watch pushes to %q, want %q", files["watch/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"], want)
	}
	if want := "HEARTBEAT_URLS=https://health.example.org/ping/" + secrets.Sites["watch-b"].HeartbeatToken + "\n"; !strings.Contains(files["watch-b/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"], want) {
		t.Errorf("watch-b pushes to %q, want %q", files["watch-b/srv/paisans/f2a9/infra/heartbeat/heartbeat.env"], want)
	}
	if !strings.Contains(files["watch-b/srv/paisans/f2a9/infra/compose.yaml"], "\n  heartbeat:\n") {
		t.Errorf("watch-b, a monitor site in mode paisans, runs no heartbeat service:\n%s", files["watch-b/srv/paisans/f2a9/infra/compose.yaml"])
	}
}

// A deployment with no monitor has nothing to push to, so no site runs a
// heartbeat service.
func TestNoMonitorMeansNoHeartbeatPusher(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Apps, "health")
	delete(cfg.Sites, "watch")
	secrets := fixtureSecrets(t)
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("a deployment without a monitor is refused: %v", refusals)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range planFiles(plan) {
		if strings.Contains(path, "/heartbeat/") {
			t.Errorf("%s is rendered with no monitor to push to", path)
		}
		if strings.HasSuffix(path, "/infra/compose.yaml") && strings.Contains(content, "\n  heartbeat:\n") {
			t.Errorf("%s runs a heartbeat service with no monitor to push to", path)
		}
	}
}

// A site without a token, or with one the fork would silently replace, is
// refused at render by name: the seed and the host would otherwise disagree
// on the URL, which the monitor reports as the site being down.
func TestAMissingOrMalformedHeartbeatTokenIsRefused(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{"", "paisans init"},
		{"not-hex", "32 lowercase hex"},
		{"0123456789ABCDEF0123456789ABCDEF", "32 lowercase hex"},
	} {
		cfg, secrets := withMonitor(t, config.SMTP{}, nil)
		site := secrets.Sites["home-a"]
		site.HeartbeatToken = tc.token
		secrets.Sites["home-a"] = site
		_, err := render.Build(cfg, secrets)
		if err == nil || !strings.Contains(err.Error(), "sites.home-a.heartbeat_token") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("token %q: got %v, want an error naming sites.home-a.heartbeat_token and %q", tc.token, err, tc.want)
		}
	}
}

// With a client at the identity provider the monitor is SSO only: its login
// page goes straight to the provider, and only the .env super-admin signs in
// with a password, at /login?local=1. Without a client there is no provider to
// send anyone to, so the variable is not rendered at all.
func TestTheMonitorIsSSOOnlyWhenItHasAClient(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	if _, ok := secrets.OIDCClients["health"]; !ok {
		t.Fatal("the fixture gives the monitor no client, so this test proves nothing")
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	env := planFiles(plan)["watch/srv/paisans/f2a9/health/.env"]
	if !strings.Contains(env, "\nOIDC_DISABLE_PASSWORD_LOGIN=true\n") {
		t.Errorf("with a client the monitor is SSO only:\n%s", env)
	}

	delete(secrets.OIDCClients, "health")
	plan, err = render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if env := planFiles(plan)["watch/srv/paisans/f2a9/health/.env"]; strings.Contains(env, "OIDC_DISABLE_PASSWORD_LOGIN") {
		t.Errorf("without a client there is no provider to be SSO only with:\n%s", env)
	}
}

// The fork refuses to start SSO only when ADMIN_PASS is "admin", so render
// refuses first rather than leave a container that exits after apply moved.
func TestAnSSOOnlyMonitorRefusesTheDefaultAdminPassword(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	secrets.Apps["health"]["admin_password"] = "admin"
	_, err := render.Build(cfg, secrets)
	if err == nil || !strings.Contains(err.Error(), "apps.health.admin_password") {
		t.Fatalf("got %v, want a refusal naming apps.health.admin_password", err)
	}

	// Without a client the fork only warns, but the password is still the
	// one anybody would guess first.
	delete(secrets.OIDCClients, "health")
	if _, err := render.Build(cfg, secrets); err == nil {
		t.Error("the literal admin password was accepted without a client")
	}
}

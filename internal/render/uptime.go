package render

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// The uptime blueprint's seed file: what the fork reconciles its monitors and
// settings from at every boot. See docs/specs/2026-10-07-uptime-monitoring.md.
//
// Field names are the fork's own column names (src/lib/sitePayload.js
// buildPayload), because the fork feeds each entry through the same builder
// and validator its API uses.
type seedMonitor struct {
	Name            string            `json:"name"`
	MonitorType     string            `json:"monitor_type"`
	URL             string            `json:"url,omitempty"`
	Method          string            `json:"method,omitempty"`
	CheckType       string            `json:"check_type,omitempty"`
	ExpectedStatus  string            `json:"expected_status,omitempty"`
	ExpectedString  string            `json:"expected_string,omitempty"`
	FollowRedirects *bool             `json:"follow_redirects,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	PingHost        string            `json:"ping_host,omitempty"`
	// A TCP check's host and port (src/lib/sitePayload.js:127-134 at uptime
	// fork b730e9b), for an app whose blueprint has no health route.
	TCPHost          string `json:"tcp_host,omitempty"`
	TCPPort          int    `json:"tcp_port,omitempty"`
	IntervalSeconds  int    `json:"interval_seconds"`
	TimeoutMS        int    `json:"timeout_ms"`
	FailureThreshold int    `json:"failure_threshold"`
	// A heartbeat's own fields (src/lib/sitePayload.js buildPayload and
	// insertSite at 1.1.0-oidc.4): the token the fork sets the monitor to,
	// rather than inventing one, the schedule kind, which is interval here
	// and never cron, and the grace the fork adds to the interval before the
	// site is stale.
	HeartbeatToken        string `json:"heartbeat_token,omitempty"`
	HeartbeatScheduleKind string `json:"heartbeat_schedule_kind,omitempty"`
	HeartbeatGraceSeconds int    `json:"heartbeat_grace_seconds,omitempty"`
}

// Two consecutive failures before an incident, so one dropped packet on a
// residential line does not wake anyone.
const (
	seedInterval  = 60
	seedTimeoutMS = 10000
	seedThreshold = 2
)

// The seed's monitor names. The fork reconciles monitors by name, so a format
// that changed would replace every deployed monitor and drop the channels
// admins attached to it; these are the formats the first release shipped.
// Names come from app and site keys, never hostnames, so a renamed hostname
// updates a monitor in place.

// PublicCheckName is the name of an app's check at its public hostname.
func PublicCheckName(app string) string { return app + " — public" }

// GateCheckName is the name of a gated app's check that the gate answers.
func GateCheckName(app string) string { return app + " — gate" }

// SignedFetchCheckName is the name of a gated federating app's check that an
// unsigned ActivityPub read is refused.
func SignedFetchCheckName(app string) string { return app + " — signed fetch" }

func directCheckName(app, site string) string {
	return fmt.Sprintf("%s — direct (%s)", app, site)
}

func privateAPICheckName(app, site string) string {
	return fmt.Sprintf("%s — private API (%s)", app, site)
}

func pingName(site string) string { return site + " — ping" }

// reachableCheckName is the name of the TCP check of an app with no health
// route on one site, in the format of directCheckName.
func reachableCheckName(app, site string) string {
	return fmt.Sprintf("%s \u2014 reachable (%s)", app, site)
}

// unroutedStatuses is every status below 500 an app's front page plausibly
// answers, spelled out because the fork matches exact codes only: an app
// that answers at all passes, and the edge answering 502, 503 or 504 because
// the app is down fails.
const unroutedStatuses = "200,301,302,303,307,308,401,403,404"

// fallbackChecks watch an app whose blueprint has no health route
// (docs/specs/2026-10-12-blueprints-and-catalogs.md, "Uptime watches every
// blueprint"): a TCP connect to its port on each site it runs on, and, for an
// app that serves HTTP, its front page at the public hostname answering
// anything below 500, plus the gate check when it is gated. What this cannot
// see is an app that is up and broken, which its check's name says.
func (p *planner) fallbackChecks(name string, app config.App, sites []string) []seedMonitor {
	noRedirects := false
	var out []seedMonitor
	if !blueprints.HealthOf(app.Blueprint).TCP {
		out = append(out, seedMonitor{
			Name: PublicCheckName(name) + " (no health route)", MonitorType: "active", Method: "GET", CheckType: "status",
			URL: "https://" + app.Hostname + "/", ExpectedStatus: unroutedStatuses, FollowRedirects: &noRedirects,
			IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
		})
		if app.Gated {
			out = append(out, seedMonitor{
				Name: GateCheckName(name), MonitorType: "active", Method: "GET", CheckType: "string",
				URL: "https://" + app.Hostname + "/", ExpectedString: GateMarker, FollowRedirects: &noRedirects,
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
		}
	}
	sorted := append([]string(nil), sites...)
	sort.Strings(sorted)
	for _, site := range sorted {
		out = append(out, seedMonitor{
			Name: reachableCheckName(name, site), MonitorType: "tcp",
			TCPHost: p.sites[site].Address, TCPPort: blueprints.Port(app.Blueprint),
			IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
		})
	}
	return out
}

func (p *planner) uptimeSeed(self string) (string, error) {
	monitorSite := p.cfg.Apps[self].Placement.Site
	placed := AppSites(p.cfg)
	noRedirects := false

	var monitors []seedMonitor
	for _, name := range p.cfg.AppNames() {
		app := p.cfg.Apps[name]
		if app.Blueprint == config.BlueprintUptime {
			// A monitor does not watch itself: down, it cannot report that,
			// and a direct check of its own container can only ever pass.
			// Every other monitor's public URL is checked below, after every
			// other app.
			continue
		}
		health, ok := blueprints.HealthFor(app.Blueprint)
		if !ok {
			monitors = append(monitors, p.fallbackChecks(name, app, placed[name])...)
			continue
		}
		// A gated app is checked three ways through the edge. The public
		// check reaches the app only when its health route is one of the
		// blueprint's open paths, which a dedicated route is and `/` never is,
		// so a gated app whose health route is its front page has no public
		// check: the gate would answer it, not the app. The gate check proves
		// the edge and the gate are in the path; the signed fetch check proves
		// the app still refuses an unsigned ActivityPub read, which an admin
		// can turn off in the app with nothing in this configuration to say so.
		// See docs/specs/2026-10-08-visibility-gate.md.
		gated := app.Gated
		if !gated || health.Path != "/" {
			monitors = append(monitors, seedMonitor{
				Name: PublicCheckName(name), MonitorType: "active", Method: "GET", CheckType: "status",
				URL: "https://" + app.Hostname + health.Path, ExpectedStatus: health.Expect,
				FollowRedirects: &noRedirects,
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
		}
		if gated {
			// The gate redirects a request with no session to sign in. A
			// string check passes on any status below 400 whose body holds
			// the marker, and only the gate's redirect carries it: the app's
			// own redirect to its sign-in page does not.
			monitors = append(monitors, seedMonitor{
				Name: GateCheckName(name), MonitorType: "active", Method: "GET", CheckType: "string",
				URL: "https://" + app.Hostname + "/", ExpectedString: GateMarker,
				FollowRedirects: &noRedirects,
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
			if sf := blueprints.SignedFetchFor(app); sf != nil && blueprints.Federates(app) {
				monitors = append(monitors, seedMonitor{
					Name: SignedFetchCheckName(name), MonitorType: "active", Method: "GET", CheckType: "status",
					URL: "https://" + app.Hostname + sf.SignedFetchProbe(app.Hostname), ExpectedStatus: sf.Expect,
					FollowRedirects: &noRedirects,
					RequestHeaders:  map[string]string{"Accept": "application/activity+json"},
					IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
				})
			}
		}
		sites := append([]string(nil), placed[name]...)
		sort.Strings(sites)
		for _, site := range sites {
			monitors = append(monitors, seedMonitor{
				Name: directCheckName(name, site), MonitorType: "active", Method: "GET", CheckType: "status",
				URL:            fmt.Sprintf("http://%s:%d%s", p.sites[site].Address, blueprints.Port(app.Blueprint), health.Path),
				ExpectedStatus: health.Expect, FollowRedirects: &noRedirects,
				// What the gateway would send, minus the gateway.
				RequestHeaders:  map[string]string{"Host": app.Hostname, "X-Forwarded-Proto": "https"},
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
			if probe := blueprints.GateFor(app.Blueprint).PrivateProbe; probe != nil && gated {
				// Through the edge the gate answers first, so this is asked
				// of the app directly (blueprints.GateSpec.PrivateProbe).
				monitors = append(monitors, seedMonitor{
					Name: privateAPICheckName(name, site), MonitorType: "active", Method: "GET", CheckType: "status",
					URL:            fmt.Sprintf("http://%s:%d%s", p.sites[site].Address, blueprints.Port(app.Blueprint), probe.Path),
					ExpectedStatus: probe.Expect, FollowRedirects: &noRedirects,
					RequestHeaders:  map[string]string{"Host": app.Hostname, "X-Forwarded-Proto": "https"},
					IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
				})
			}
			if app.Blueprint == config.BlueprintPocketID {
				// The operator reconciler service beside each instance,
				// which /livez answers 200 while its pass loop runs,
				// standby or not, and 503 once it is stale
				// (docs/specs/2026-10-14-operators-and-management.md). It
				// is that site's service alone: whether the operators
				// check out, which only the active instance knows, is the
				// operators heartbeat below. The gateway does not route the reconciler, so
				// there is no public check.
				monitors = append(monitors, seedMonitor{
					Name: fmt.Sprintf("%s — operator reconciler (%s)", name, site), MonitorType: "active", Method: "GET", CheckType: "status",
					URL:            fmt.Sprintf("http://%s:%d/livez", p.sites[site].Address, reconcilerPort),
					ExpectedStatus: "200", FollowRedirects: &noRedirects,
					IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
				})
			}
		}
	}
	// The public URL of every monitor on another monitor site, in AppNames
	// order, and never this monitor's own. A monitor cannot report its own
	// death or its host's, so a monitor on a separate site is what notices
	// either: this check covers the other monitor and the path in front of it,
	// and the ping of its site, seeded below, covers its host. The path in
	// front of this monitor is still exercised from outside it: every other
	// site's heartbeat is pushed to its public hostname (heartbeat.go), so
	// broken DNS, web server or certificate there leaves every heartbeat
	// stale. A monitor on this same site dies with this host, so checking it
	// would add nothing about losing the host, and it is left out.
	if health, ok := blueprints.HealthFor(config.BlueprintUptime); ok {
		for _, name := range p.cfg.AppNames() {
			other := p.cfg.Apps[name]
			if name == self || other.Blueprint != config.BlueprintUptime || other.Placement.Site == monitorSite {
				continue
			}
			monitors = append(monitors, seedMonitor{
				Name: PublicCheckName(name), MonitorType: "active", Method: "GET", CheckType: "status",
				URL: "https://" + other.Hostname + health.Path, ExpectedStatus: health.Expect,
				FollowRedirects: &noRedirects,
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
		}
	}
	// No ping and no heartbeat for this monitor's own site: a ping from the
	// host to itself, and a push from the host to the monitor running on it,
	// can only ever succeed. Every other app on this site keeps its checks
	// above.
	for _, site := range p.order {
		if site == monitorSite {
			continue
		}
		monitors = append(monitors, seedMonitor{
			Name: pingName(site), MonitorType: "ping", PingHost: p.sites[site].Address,
			IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
		})
	}
	// The reverse of each ping: the site's host pushes to this monitor's
	// public hostname once a minute (see heartbeat.go), with the site's own
	// token from the secrets, so the seed and the host agree on the URL. The
	// fork evaluates a heartbeat every 15 s (src/monitor.js watchdogTick at
	// 1.1.0-oidc.4) and counts failures towards the threshold the same way,
	// so the threshold shared with every other check costs one such tick.
	for _, site := range p.order {
		if site == monitorSite {
			continue
		}
		token, err := p.heartbeatToken(site)
		if err != nil {
			return "", err
		}
		monitors = append(monitors, seedMonitor{
			Name: heartbeatName(site), MonitorType: "heartbeat",
			HeartbeatToken: token, HeartbeatScheduleKind: "interval", HeartbeatGraceSeconds: heartbeatGraceSeconds,
			IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
		})
	}

	// One operators heartbeat per Pocket ID app, pushed by whichever of its
	// instances is active (operatorsheartbeat.go), with the app's own token
	// and the site heartbeat's schedule, grace and threshold. Seeded by
	// every monitor, wherever the app runs: every instance pushes to every
	// monitor.
	for _, name := range p.cfg.AppNames() {
		if p.cfg.Apps[name].Blueprint != config.BlueprintPocketID {
			continue
		}
		token, err := p.operatorsHeartbeatToken(name)
		if err != nil {
			return "", err
		}
		monitors = append(monitors, seedMonitor{
			Name: p.continuityName(name, ""), MonitorType: "heartbeat",
			HeartbeatToken: token, HeartbeatScheduleKind: "interval", HeartbeatGraceSeconds: heartbeatGraceSeconds,
			IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
		})
		// The DNS check's two heartbeats, beside it, with its fields
		// (dnscontinuity.go), when the deployment renders the check.
		if !p.dnsContinuity() {
			continue
		}
		for _, m := range dnsMonitors {
			token, err := p.dnsHeartbeatToken(name, m.Secret)
			if err != nil {
				return "", err
			}
			monitors = append(monitors, seedMonitor{
				Name: p.continuityName(name, m.Function), MonitorType: "heartbeat",
				HeartbeatToken: token, HeartbeatScheduleKind: "interval", HeartbeatGraceSeconds: heartbeatGraceSeconds,
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
		}
	}

	settings := map[string]any{"status_page_enabled": false}
	if smtp := p.smtpFor(self); smtp.Host != "" {
		settings["smtp_host"] = smtp.Host
		settings["smtp_port"] = smtp.Port
		settings["smtp_secure"] = smtp.Secure
		settings["smtp_user"] = smtp.Username
		settings["smtp_pass"] = smtp.Password
		settings["smtp_from_address"] = smtp.FromAddress
		settings["smtp_from_name"] = smtp.FromName
	}
	out, err := json.MarshalIndent(map[string]any{"settings": settings, "monitors": monitors}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// smtpFor is an app's effective SMTP settings with its password: the app's
// own smtp_password secret when set, else external.smtp_password.
func (p *planner) smtpFor(app string) smtpValues {
	s := p.cfg.SMTPFor(app)
	if s.Host == "" {
		return smtpValues{}
	}
	password := p.appSecret(app, "smtp_password")
	if password == "" {
		password = p.secrets.External["smtp_password"]
	}
	security := s.Security
	if security == "" {
		security = config.SMTPStartTLS
	}
	return smtpValues{
		Host: s.Host, Port: s.PortOrDefault(), Secure: s.Security == config.SMTPTLS, Security: security,
		Username: s.Username, Password: password, FromAddress: s.FromAddress, FromName: s.FromName,
	}
}

// smtpValues is an app's resolved mail settings, zero when no host resolves.
type smtpValues struct {
	Host string
	Port int
	// Secure is implicit TLS. Security is the resolved mode, starttls, tls
	// or none, spelled as Pocket ID's SMTP_TLS and the account service's
	// ACCOUNT_SMTP_TLS both take it. The monitor's smtp_secure is Secure:
	// false is nodemailer's STARTTLS when the server offers it and plaintext
	// otherwise, which is what none needs.
	Secure      bool
	Security    string
	Username    string
	Password    string
	FromAddress string
	FromName    string
}

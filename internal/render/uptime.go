package render

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// The uptime kind's seed file: what the fork reconciles its monitors and
// settings from at every boot. See docs/specs/2026-10-07-uptime-monitoring.md.
//
// Field names are the fork's own column names (src/lib/sitePayload.js
// buildPayload), because the fork feeds each entry through the same builder
// and validator its API uses.
type seedMonitor struct {
	Name             string            `json:"name"`
	MonitorType      string            `json:"monitor_type"`
	URL              string            `json:"url,omitempty"`
	Method           string            `json:"method,omitempty"`
	CheckType        string            `json:"check_type,omitempty"`
	ExpectedStatus   string            `json:"expected_status,omitempty"`
	ExpectedString   string            `json:"expected_string,omitempty"`
	FollowRedirects  *bool             `json:"follow_redirects,omitempty"`
	RequestHeaders   map[string]string `json:"request_headers,omitempty"`
	PingHost         string            `json:"ping_host,omitempty"`
	IntervalSeconds  int               `json:"interval_seconds"`
	TimeoutMS        int               `json:"timeout_ms"`
	FailureThreshold int               `json:"failure_threshold"`
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

func (p *planner) uptimeSeed(self string) (string, error) {
	monitorSite := p.cfg.Apps[self].Placement.Site
	placed := AppSites(p.cfg)
	noRedirects := false

	var monitors []seedMonitor
	for _, name := range p.cfg.AppNames() {
		app := p.cfg.Apps[name]
		if app.Kind == config.KindUptime {
			// A monitor does not watch itself: down, it cannot report that,
			// and a direct check of its own container can only ever pass.
			// Every other monitor's public URL is checked below, after every
			// other app.
			continue
		}
		health, ok := kinds.HealthFor(app.Kind)
		if !ok {
			return "", fmt.Errorf("apps.%s: kind %s has no health route recorded in internal/kinds, so the monitor cannot check it", name, app.Kind)
		}
		// A gated app is checked three ways through the edge. The public
		// check reaches the app only when its health route is one of the
		// kind's open paths, which a dedicated route is and `/` never is, so a
		// gated app whose health route is its front page has no public check:
		// the gate would answer it, not the app. The gate check proves the
		// edge and the gate are in the path; the signed fetch check proves the
		// app still refuses an unsigned ActivityPub read, which an admin can
		// turn off in the app with nothing in this configuration to say so.
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
			if sf := kinds.SignedFetchFor(app); sf != nil && kinds.Federates(app) {
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
				URL:            fmt.Sprintf("http://%s:%d%s", p.sites[site].Address, appPort[app.Kind], health.Path),
				ExpectedStatus: health.Expect, FollowRedirects: &noRedirects,
				// What the gateway would send, minus the gateway.
				RequestHeaders:  map[string]string{"Host": app.Hostname, "X-Forwarded-Proto": "https"},
				IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
			})
			if app.Kind == config.KindMbin && gated {
				// A gated Mbin runs as a private instance, so its API refuses
				// a read with no token. Through the edge the gate answers
				// first, so this is asked of the app directly: it is what
				// notices an image that ignores MBIN_PRIVATE_INSTANCE.
				monitors = append(monitors, seedMonitor{
					Name: privateAPICheckName(name, site), MonitorType: "active", Method: "GET", CheckType: "status",
					URL:            fmt.Sprintf("http://%s:%d%s", p.sites[site].Address, appPort[app.Kind], kinds.MbinPrivateAPIProbe),
					ExpectedStatus: "401", FollowRedirects: &noRedirects,
					RequestHeaders:  map[string]string{"Host": app.Hostname, "X-Forwarded-Proto": "https"},
					IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
				})
			}
			if app.Kind == config.KindPocketID {
				// The admin reconciler service beside each instance, which
				// /livez answers 200 while its pass loop runs, standby or
				// not, and 503 once it is stale
				// (docs/specs/2026-10-08-admin-reconciler.md). It is that
				// site's service alone: the admins group's health, which
				// only the active instance knows, is the admins heartbeat
				// below. The gateway does not route the reconciler, so
				// there is no public check.
				monitors = append(monitors, seedMonitor{
					Name: fmt.Sprintf("%s — admin reconciler (%s)", name, site), MonitorType: "active", Method: "GET", CheckType: "status",
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
	if health, ok := kinds.HealthFor(config.KindUptime); ok {
		for _, name := range p.cfg.AppNames() {
			other := p.cfg.Apps[name]
			if name == self || other.Kind != config.KindUptime || other.Placement.Site == monitorSite {
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

	// One admins heartbeat per Pocket ID app, pushed by whichever of its
	// instances is active (adminsheartbeat.go), with the app's own token
	// and the site heartbeat's schedule, grace and threshold. Seeded by
	// every monitor, wherever the app runs: every instance pushes to every
	// monitor.
	for _, name := range p.cfg.AppNames() {
		if p.cfg.Apps[name].Kind != config.KindPocketID {
			continue
		}
		token, err := p.adminsHeartbeatToken(name)
		if err != nil {
			return "", err
		}
		monitors = append(monitors, seedMonitor{
			Name: adminsHeartbeatName(name), MonitorType: "heartbeat",
			HeartbeatToken: token, HeartbeatScheduleKind: "interval", HeartbeatGraceSeconds: heartbeatGraceSeconds,
			IntervalSeconds: seedInterval, TimeoutMS: seedTimeoutMS, FailureThreshold: seedThreshold,
		})
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
	return smtpValues{
		Host: s.Host, Port: s.PortOrDefault(), Secure: s.Security == config.SMTPTLS,
		Username: s.Username, Password: password, FromAddress: s.FromAddress, FromName: s.FromName,
	}
}

// smtpValues is an app's resolved mail settings, zero when no host resolves.
type smtpValues struct {
	Host        string
	Port        int
	Secure      bool
	Username    string
	Password    string
	FromAddress string
	FromName    string
}

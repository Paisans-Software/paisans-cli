package secretsrotate

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// uptimeWorld is a deployment with two monitors, status on home-c and watch
// on home-a, three sites that each push a heartbeat to every monitor off
// their own site, and a Pocket ID app, auth, on home-a and home-b, whose
// instances push its operators heartbeat to every monitor. seeded is the token a
// monitor's seed holds for a site, keyed app/site, and for auth's admins
// heartbeat, keyed app/auth; pushed is the token a site's heartbeat service
// pushes with; operatorsPushed is the token auth's pusher on a site pushes with;
// admin is the ADMIN_PASS each monitor runs with.
type uptimeWorld struct {
	t      *testing.T
	cfg    *config.Config
	store  *memStore
	seeded map[string]string
	pushed map[string]string
	// operatorsPushed is keyed by site.
	operatorsPushed map[string]string
	admin           map[string]string
	unhealthy       map[string]error
	// seededAtPush is what every monitor's seed held when a site's heartbeat
	// service was handed its token.
	seededAtPush map[string]map[string]string
	// adminsSeededAtPush is what every monitor's seed held when auth's
	// stack on a site was handed its token.
	adminsSeededAtPush map[string]map[string]string
	log                []string
}

var heartbeatShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

func newUptimeWorld(t *testing.T) *uptimeWorld {
	t.Helper()
	cfg := &config.Config{
		ID: "3f2a6c1e-8b4d-4e2a-9c1f-7d5e6b8a9c0d",
		Sites: map[string]config.Site{
			"home-a": {Roles: []config.Role{config.RoleData, config.RoleApps, config.RoleMonitor}, Address: "10.0.0.1"},
			"home-b": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.2"},
			"home-c": {Roles: []config.Role{config.RoleData, config.RoleMonitor}, Address: "10.0.0.3"},
		},
		Cluster: config.Cluster{Sites: []string{"home-a", "home-b", "home-c"}},
		Apps: map[string]config.App{
			"auth":   {Blueprint: config.BlueprintPocketID, Hostname: "id.example.org", Placement: config.Placement{Mode: config.PlacementCluster}},
			"forum":  {Blueprint: config.Blueprint("mbin"), Placement: config.Placement{Mode: config.PlacementCluster}},
			"health": {Blueprint: config.BlueprintUptime, Hostname: "health.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-c"}},
			"watch":  {Blueprint: config.BlueprintUptime, Hostname: "watch.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}},
		},
	}
	s := &config.Secrets{
		Sites: map[string]config.SiteSecrets{},
		Apps: map[string]map[string]any{
			"auth":   {"operators_heartbeat_token": strings.Repeat("ab", 16)},
			"health": {"admin_password": "health-admin-old", "session_secret": "health-session"},
			"watch":  {"admin_password": "watch-admin-old", "session_secret": "watch-session"},
		},
	}
	for i, site := range []string{"home-a", "home-b", "home-c"} {
		s.Sites[site] = config.SiteSecrets{HeartbeatToken: strings.Repeat(fmt.Sprintf("%x", i+10), 32)}
	}
	w := &uptimeWorld{
		t: t, cfg: cfg, store: &memStore{s: s},
		seeded: map[string]string{}, pushed: map[string]string{}, operatorsPushed: map[string]string{}, admin: map[string]string{},
		unhealthy: map[string]error{}, seededAtPush: map[string]map[string]string{}, adminsSeededAtPush: map[string]map[string]string{},
	}
	w.render(s, func(string) {})
	w.log = nil
	return w
}

// monitors is the uptime apps the configuration declares, in app order.
func (w *uptimeWorld) monitors() []string {
	var out []string
	for _, name := range w.cfg.AppNames() {
		if w.cfg.Apps[name].Blueprint == config.BlueprintUptime {
			out = append(out, name)
		}
	}
	return out
}

// render is what an apply of each stack leaves on the host, as the render
// places it: a monitor's seed names every site off its own and auth's admins
// heartbeat, a site pushes to every monitor off it, and auth's pusher on
// each of its sites pushes to every monitor while there is one. changed is
// called for each change.
func (w *uptimeWorld) renderStack(site, stack string, s *config.Secrets, changed func(string)) {
	switch stack {
	case "infra":
		pushes := false
		for _, app := range []string{"health", "watch"} {
			if w.cfg.Apps[app].Placement.Site != site {
				pushes = true
			}
		}
		if !pushes {
			return
		}
		if token := s.Sites[site].HeartbeatToken; w.pushed[site] != token {
			w.pushed[site] = token
			changed(fmt.Sprintf("apply %s infra heartbeat", site))
		}
	case "health", "watch":
		if w.cfg.Apps[stack].Placement.Site != site {
			w.t.Fatalf("%s applied on %s, where it does not run", stack, site)
		}
		for _, other := range []string{"home-a", "home-b", "home-c"} {
			if other == site {
				continue
			}
			at := stack + "/" + other
			if token := s.Sites[other].HeartbeatToken; w.seeded[at] != token {
				w.seeded[at] = token
				changed(fmt.Sprintf("apply %s %s seed %s", site, stack, other))
			}
		}
		if token, _ := s.Get("apps.auth.operators_heartbeat_token"); w.seeded[stack+"/auth"] != token {
			w.seeded[stack+"/auth"] = token
			changed(fmt.Sprintf("apply %s %s seed auth", site, stack))
		}
		pass, _ := s.Get("apps." + stack + ".admin_password")
		if w.admin[stack] != pass {
			w.admin[stack] = pass
			changed(fmt.Sprintf("apply %s %s admin", site, stack))
		}
	case "auth":
		if site != "home-a" && site != "home-b" {
			w.t.Fatalf("auth applied on %s, where it does not run", site)
		}
		if len(w.monitors()) == 0 {
			return
		}
		if token, _ := s.Get("apps.auth.operators_heartbeat_token"); w.operatorsPushed[site] != token {
			w.operatorsPushed[site] = token
			changed(fmt.Sprintf("apply %s auth operators-heartbeat", site))
		}
	default:
		w.t.Fatalf("%s applied on %s: not a stack this world runs", stack, site)
	}
}

func (w *uptimeWorld) render(s *config.Secrets, changed func(string)) {
	for _, site := range []string{"home-a", "home-b", "home-c"} {
		w.renderStack(site, "infra", s, changed)
	}
	w.renderStack("home-c", "health", s, changed)
	w.renderStack("home-a", "watch", s, changed)
	w.renderStack("home-a", "auth", s, changed)
	w.renderStack("home-b", "auth", s, changed)
}

func (w *uptimeWorld) env() Env {
	return Env{
		Cfg:   w.cfg,
		Store: w.store,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			for _, stack := range stacks {
				if stack == "infra" {
					w.seededAtPush[site] = maps(w.seeded)
				}
				if stack == "auth" {
					w.adminsSeededAtPush[site] = maps(w.seeded)
				}
				w.renderStack(site, stack, s, func(l string) { w.log = append(w.log, l) })
			}
			return nil
		},
		Healthy: func(site string, stacks []string) error {
			if err := w.unhealthy[site]; err != nil {
				w.log = append(w.log, "unhealthy "+site)
				return err
			}
			w.log = append(w.log, "healthy "+site)
			return nil
		},
	}
}

func (w *uptimeWorld) rotate(r Rotator) error {
	w.t.Helper()
	e := w.env()
	run, err := Plan(e, []Rotator{r})
	if err != nil {
		return err
	}
	return run.Execute(e, nil)
}

// The monitors learn the token first, each healthy before the next, and the
// site pushes with it only once every monitor that checks it holds it.
func TestHeartbeatMonitorsLearnTheTokenBeforeTheSitePushesIt(t *testing.T) {
	w := newUptimeWorld(t)
	r := Heartbeat("home-b")
	if r.Path() != "sites.home-b.heartbeat_token" {
		t.Fatalf("path %q", r.Path())
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-c:health", "home-a:watch", "home-b:infra"}) {
		t.Fatalf("users %v, want every monitor off home-b, then home-b's heartbeat", got)
	}
	old := w.store.s.Sites["home-b"].HeartbeatToken
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply home-c health seed home-b", "healthy home-c",
		"apply home-a watch seed home-b", "healthy home-a",
		"apply home-b infra heartbeat", "healthy home-b",
	}
	if !slices.Equal(w.log, want) {
		t.Fatalf("log\n%v\nwant\n%v", w.log, want)
	}
	next := w.store.s.Sites["home-b"].HeartbeatToken
	if next == old || !heartbeatShape.MatchString(next) {
		t.Fatalf("the file holds a token of %d characters, not a new one of 32 lowercase hex", len(next))
	}
	at := w.seededAtPush["home-b"]
	if at["health/home-b"] != next || at["watch/home-b"] != next {
		t.Fatal("home-b pushed the next token before every monitor that checks it held it")
	}
	if w.pushed["home-b"] != next {
		t.Fatal("home-b does not push the promoted token")
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
}

// A monitor does not check its own site, so the site's heartbeat goes only
// to the monitors off it.
func TestHeartbeatOfAMonitorSiteSkipsItsOwnMonitor(t *testing.T) {
	w := newUptimeWorld(t)
	if got := usesOf(w.env(), Heartbeat("home-c")); !slices.Equal(got, []string{"home-a:watch", "home-c:infra"}) {
		t.Fatalf("users %v, want watch, then home-c's heartbeat", got)
	}
}

// With the one monitor on the site itself nobody checks its token and no
// service pushes it: it is replaced in the file and handed to nobody.
func TestHeartbeatNobodyChecks(t *testing.T) {
	w := newUptimeWorld(t)
	delete(w.cfg.Apps, "watch")
	r := Heartbeat("home-c")
	if got := usesOf(w.env(), r); len(got) != 0 {
		t.Fatalf("users %v, want none", got)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	if len(w.log) != 0 {
		t.Fatalf("log %v, want nothing applied", w.log)
	}
}

// A run stopped between the monitors and the pusher resumes with the same
// token: the monitors already hold it and are not changed again.
func TestHeartbeatResumesAtThePusher(t *testing.T) {
	w := newUptimeWorld(t)
	w.unhealthy["home-b"] = errors.New("heartbeat is restarting")
	if err := w.rotate(Heartbeat("home-b")); err == nil {
		t.Fatal("an unhealthy pusher did not stop the rotation")
	}
	next, pending := w.store.s.Rotations["sites.home-b.heartbeat_token"]
	if !pending {
		t.Fatal("the next token was not kept for the resumed run")
	}
	delete(w.unhealthy, "home-b")
	w.log = nil
	if err := w.rotate(Heartbeat("home-b")); err != nil {
		t.Fatal(err)
	}
	want := []string{"healthy home-c", "healthy home-a", "healthy home-b"}
	if !slices.Equal(w.log, want) {
		t.Fatalf("resumed log %v, want %v", w.log, want)
	}
	if got := w.store.s.Sites["home-b"].HeartbeatToken; got != next {
		t.Fatal("the resumed run promoted a different token from the one the monitors hold")
	}
}

func TestHeartbeatCostStatesTheRefusedPush(t *testing.T) {
	c := Heartbeat("home-b").Cost()
	if c.SignsOut || c.Reconnects {
		t.Fatalf("cost %+v: a heartbeat token signs nobody out", c)
	}
	if !strings.Contains(c.Downtime, "refuse") || !strings.Contains(c.Downtime, "grace") {
		t.Fatalf("downtime %q: the plan must say pushes may be refused, within the grace", c.Downtime)
	}
}

func TestUptimeAdminRotatesOnTheMonitorsSite(t *testing.T) {
	w := newUptimeWorld(t)
	r := UptimeAdmin("health")
	if r.Path() != "apps.health.admin_password" {
		t.Fatalf("path %q", r.Path())
	}
	if c := r.Cost(); c != (Cost{}) {
		t.Fatalf("cost %+v, want none to members", c)
	}
	e := w.env()
	if err := r.(Prechecker).Precheck(e); err != nil {
		t.Fatal(err)
	}
	if got := usesOf(e, r); !slices.Equal(got, []string{"home-c:health"}) {
		t.Fatalf("users %v", got)
	}
	if ok, err := r.Accepts(e, "anything"); !ok || err != nil {
		t.Fatalf("Accepts %v %v: the fork reads ADMIN_PASS when it starts", ok, err)
	}
	if err := r.Checked(e, "anything"); err != nil {
		t.Fatal(err)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.log, []string{"apply home-c health admin", "healthy home-c"}) {
		t.Fatalf("log %v", w.log)
	}
	next, _ := w.store.s.Get("apps.health.admin_password")
	if next == "health-admin-old" || next == "" || w.admin["health"] != next {
		t.Fatal("health does not run with the promoted password")
	}
	if w.admin["watch"] != "watch-admin-old" {
		t.Fatal("watch's password changed with health's")
	}
}

func TestUptimePrechecksRefuse(t *testing.T) {
	w := newUptimeWorld(t)
	for _, tc := range []struct {
		r    Rotator
		want string
	}{
		{UptimeAdmin("nowhere"), "declares no app"},
		{UptimeAdmin("forum"), "uptime"},
		{Heartbeat("nowhere"), "declares no site"},
		{OperatorsHeartbeat("nowhere"), "declares no app"},
		{OperatorsHeartbeat("health"), "pocket-id"},
	} {
		err := tc.r.(Prechecker).Precheck(w.env())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a refusal naming %q", tc.r.Path(), err, tc.want)
		}
	}
	if err := Heartbeat("home-b").(Prechecker).Precheck(w.env()); err != nil {
		t.Fatal(err)
	}
	if err := OperatorsHeartbeat("auth").(Prechecker).Precheck(w.env()); err != nil {
		t.Fatal(err)
	}
}

// Every monitor learns the operators heartbeat's next token first, the one on a
// site auth runs on included, each healthy before the next, and auth's
// pushers are handed it only once every monitor holds it, one site at a time.
func TestOperatorsHeartbeatMonitorsLearnTheTokenBeforeThePushers(t *testing.T) {
	w := newUptimeWorld(t)
	r := OperatorsHeartbeat("auth")
	if r.Path() != "apps.auth.operators_heartbeat_token" {
		t.Fatalf("path %q", r.Path())
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-c:health", "home-a:watch", "home-a:auth", "home-b:auth"}) {
		t.Fatalf("users %v, want every monitor, then auth on each of its sites", got)
	}
	old, _ := w.store.s.Get("apps.auth.operators_heartbeat_token")
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply home-c health seed auth", "healthy home-c",
		"apply home-a watch seed auth", "healthy home-a",
		"apply home-a auth operators-heartbeat", "healthy home-a",
		"apply home-b auth operators-heartbeat", "healthy home-b",
	}
	if !slices.Equal(w.log, want) {
		t.Fatalf("log\n%v\nwant\n%v", w.log, want)
	}
	next, _ := w.store.s.Get("apps.auth.operators_heartbeat_token")
	if next == old || !heartbeatShape.MatchString(next) {
		t.Fatalf("the file holds a token of %d characters, not a new one of 32 lowercase hex", len(next))
	}
	for _, site := range []string{"home-a", "home-b"} {
		at := w.adminsSeededAtPush[site]
		if at["health/auth"] != next || at["watch/auth"] != next {
			t.Fatalf("auth on %s pushed the next token before every monitor held it", site)
		}
		if w.operatorsPushed[site] != next {
			t.Fatalf("auth on %s does not push the promoted token", site)
		}
	}
	for _, site := range []string{"home-a", "home-b", "home-c"} {
		if w.seeded["health/"+site] == next || w.seeded["watch/"+site] == next {
			t.Fatal("a site heartbeat's seeded token changed with the operators heartbeat's")
		}
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
}

// With no monitor no pusher is rendered and no seed names the token: it is
// replaced in the file and handed to nobody.
func TestOperatorsHeartbeatWithNoMonitor(t *testing.T) {
	w := newUptimeWorld(t)
	delete(w.cfg.Apps, "health")
	delete(w.cfg.Apps, "watch")
	r := OperatorsHeartbeat("auth")
	if got := usesOf(w.env(), r); len(got) != 0 {
		t.Fatalf("users %v, want none", got)
	}
	old, _ := w.store.s.Get("apps.auth.operators_heartbeat_token")
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	if len(w.log) != 0 {
		t.Fatalf("log %v, want nothing applied", w.log)
	}
	if next, _ := w.store.s.Get("apps.auth.operators_heartbeat_token"); next == old || !heartbeatShape.MatchString(next) {
		t.Fatal("the token was not replaced in the file")
	}
}

// A run stopped between the monitors and the pushers resumes with the same
// token: the monitors already hold it and are not changed again.
func TestOperatorsHeartbeatResumesAtThePushers(t *testing.T) {
	w := newUptimeWorld(t)
	w.unhealthy["home-b"] = errors.New("operators-heartbeat is restarting")
	if err := w.rotate(OperatorsHeartbeat("auth")); err == nil {
		t.Fatal("an unhealthy pusher did not stop the rotation")
	}
	next, pending := w.store.s.Rotations["apps.auth.operators_heartbeat_token"]
	if !pending {
		t.Fatal("the next token was not kept for the resumed run")
	}
	delete(w.unhealthy, "home-b")
	w.log = nil
	if err := w.rotate(OperatorsHeartbeat("auth")); err != nil {
		t.Fatal(err)
	}
	want := []string{"healthy home-c", "healthy home-a", "healthy home-a", "healthy home-b"}
	if !slices.Equal(w.log, want) {
		t.Fatalf("resumed log %v, want %v", w.log, want)
	}
	if got, _ := w.store.s.Get("apps.auth.operators_heartbeat_token"); got != next {
		t.Fatal("the resumed run promoted a different token from the one the monitors hold")
	}
}

func TestOperatorsHeartbeatCostStatesTheRefusedPush(t *testing.T) {
	c := OperatorsHeartbeat("auth").Cost()
	if c.SignsOut || c.Reconnects {
		t.Fatalf("cost %+v: an operators heartbeat token signs nobody out", c)
	}
	if !strings.Contains(c.Downtime, "operators heartbeat") || !strings.Contains(c.Downtime, "refuse") || !strings.Contains(c.Downtime, "grace") {
		t.Fatalf("downtime %q: the plan must say the operators heartbeat may be refused, within the grace", c.Downtime)
	}
	if ok, err := OperatorsHeartbeat("auth").Accepts(Env{}, "anything"); !ok || err != nil {
		t.Fatalf("Accepts %v %v: the monitors are handed it as users", ok, err)
	}
}

package topology_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// everyoneAnswers is evidence of a deployment whose every site answered with
// a record that covers it, and that nothing lists as joined yet.
func everyoneAnswers(t *testing.T, cfg *config.Config) topology.Evidence {
	t.Helper()
	ev := topology.Evidence{Login: map[string]error{}, Records: map[string]topology.RecordRead{}, Joined: map[string][]string{}}
	for _, name := range cfg.SiteNames() {
		req, err := admission.Build(cfg, name)
		if err != nil {
			t.Fatal(err)
		}
		rec := admission.NewRecord(req, req.Grants, "", time.Unix(0, 0))
		// host admit records the owner's web server itself, as it finds it.
		if cfg.Sites[name].WebServerManagedBy() == config.ManagedByExternal {
			rec.WebServer = &admission.WebServer{Server: "caddy", Process: "caddy"}
		}
		ev.Login[name] = nil
		ev.Records[name] = topology.RecordRead{Record: rec}
	}
	return ev
}

var refused = errors.Join(apply.ErrLoginRefused, errors.New("Permission denied (publickey)"))
var timedOut = errors.Join(apply.ErrUnreachable, errors.New("Connection timed out"))

// The spec's Tests: a site whose login is refused and which neither the
// deployment record nor the cluster lists is pending; the same site listed
// is down; a timeout reads the same as a refusal, either way.
func TestClassifyPendingAndDown(t *testing.T) {
	cfg := fixture(t)
	for _, failed := range []error{refused, timedOut} {
		ev := everyoneAnswers(t, cfg)
		ev.Login["home-c"] = failed
		delete(ev.Records, "home-c")
		c, err := topology.Classify(cfg, ev)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Sites["home-c"]; got.Class != topology.Pending {
			t.Errorf("%v, not listed: %+v", failed, got)
		}
		ev.Joined["etcd's membership"] = []string{"home-a", "home-b", "home-c"}
		c, err = topology.Classify(cfg, ev)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Sites["home-c"]; got.Class != topology.Down || got.Why != topology.DownLine("home-c") {
			t.Errorf("%v, listed: %+v", failed, got)
		}
	}
}

// The down line is the spec's, word for word.
func TestDownLine(t *testing.T) {
	want := "home-b: its login is refused or it does not answer: the host is down, or its owner evicted this deployment. paisans site remove --site home-b --host-gone removes it from the deployment."
	if got := topology.DownLine("home-b"); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// A site that answers with no record, or with one lacking a grant its roles
// need, is pending while nothing lists it.
func TestAnsweringWithoutAdmissionIsPending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Records["home-c"] = topology.RecordRead{Err: admission.ErrNotAdmitted}
	rec := ev.Records["home-b"].Record
	rec.Grants.Watchdog = false
	ev.Records["home-b"] = topology.RecordRead{Record: rec}
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Pending(), []string{"home-b", "home-c"}) {
		t.Errorf("pending %v", c.Pending())
	}
	if !strings.Contains(c.Sites["home-b"].Why, "--watchdog") {
		t.Errorf("why %q", c.Sites["home-b"].Why)
	}
}

// Review Focus: a site that does not answer while a source of evidence
// could not be read is never taken for pending.
func TestUnreadableEvidenceNeverMakesAJoinedSitePending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = timedOut
	delete(ev.Records, "home-c")
	ev.Unread = []topology.Source{{Name: "the deployment record (no gateway answered)"}}
	if _, err := topology.Classify(cfg, ev); err == nil || !strings.Contains(err.Error(), "the deployment record") {
		t.Errorf("got %v", err)
	}
	ev.Joined["etcd's membership"] = []string{"home-c"}
	c, err := topology.Classify(cfg, ev)
	if err != nil || c.Sites["home-c"].Class != topology.Down {
		t.Errorf("listed by a source that was read: %+v %v", c, err)
	}

	// A host that answers but no longer admits the deployment, whose owner
	// evicted it and kept the user, is as unknown while the record is unread.
	ev = everyoneAnswers(t, cfg)
	ev.Records["home-c"] = topology.RecordRead{Err: admission.ErrNotAdmitted}
	ev.Unread = []topology.Source{{Name: "the deployment record (home-a: reading deployed.json: permission denied)"}}
	_, err = topology.Classify(cfg, ev)
	if err == nil || !strings.Contains(err.Error(), "the deployment record") {
		t.Errorf("answering, not admitted, record unread: %v", err)
	}
	lowerCase(t, err)
}

// An unread source holds back only the sites it could have listed: etcd's
// membership lists only etcd's voters, so while it is unread a voter nothing
// lists refuses, and a site that is no voter is still pending.
func TestUnreadEvidenceHoldsBackOnlyTheSitesItCouldList(t *testing.T) {
	cfg := fixture(t)
	etcd := topology.Source{Name: "etcd's membership (home-b: context deadline exceeded)", Sites: cfg.Etcd.Members}
	if slices.Contains(cfg.Etcd.Members, "status") || !slices.Contains(cfg.Etcd.Members, "home-c") {
		t.Fatalf("the fixture's etcd members %v", cfg.Etcd.Members)
	}
	ev := everyoneAnswers(t, cfg)
	ev.Login["status"] = refused
	delete(ev.Records, "status")
	ev.Unread = []topology.Source{etcd}
	c, err := topology.Classify(cfg, ev)
	if err != nil || c.Sites["status"].Class != topology.Pending {
		t.Errorf("a site etcd could never list: %+v %v", c, err)
	}
	ev.Records["home-c"] = topology.RecordRead{Err: admission.ErrNotAdmitted}
	_, err = topology.Classify(cfg, ev)
	if err == nil || !strings.Contains(err.Error(), "home-c") || !strings.Contains(err.Error(), "etcd's membership") {
		t.Errorf("a voter nothing lists, etcd unread: %v", err)
	}
	lowerCase(t, err)
}

// lowerCase fails the test when err's message opens with a capital.
func lowerCase(t *testing.T, err error) {
	t.Helper()
	if err != nil && err.Error() != "" && unicode.IsUpper([]rune(err.Error())[0]) {
		t.Errorf("error opens with a capital: %v", err)
	}
}

// A site the deployment lists as joined is never pending, whatever its
// route: one whose every route into the mesh is through pending sites
// refuses the run instead of leaving the mesh.
func TestARelayNeverDropsAJoinedSite(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].DialEndpoint() != "" {
			ev.Login[name] = refused
			delete(ev.Records, name)
		}
	}
	ev.Joined["etcd's membership"] = []string{"home-b"}
	c, err := topology.Classify(cfg, ev)
	if err == nil || !strings.Contains(err.Error(), "home-b") {
		t.Errorf("a joined site with no route: %+v %v", c, err)
	}
	lowerCase(t, err)
}

// The zero Class is no class: an undeclared site is not ready, and --site
// naming one refuses.
func TestAnUndeclaredSiteIsNotReady(t *testing.T) {
	cfg := fixture(t)
	c, err := topology.Classify(cfg, everyoneAnswers(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if c.Ready("typo") || topology.Class(0) == topology.Ready {
		t.Error("the zero class reads as ready")
	}
	err = c.Refuse(cfg, "typo")
	if err == nil || !strings.Contains(err.Error(), "--site typo") {
		t.Errorf("--site typo: %v", err)
	}
	lowerCase(t, err)
}

// The effective topology is the declaration less the pending sites: no
// peer, member, Garage node or pinned app of theirs. A down site stays.
func TestEffectiveTopology(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = refused
	delete(ev.Records, "home-c")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	eff := c.Effective(cfg)
	if _, ok := eff.Sites["home-c"]; ok || slices.Contains(eff.Etcd.Members, "home-c") || slices.Contains(eff.Cluster.Sites, "home-c") || slices.Contains(eff.Storage.Garage.Sites, "home-c") {
		t.Errorf("home-c is still in %+v", eff)
	}
	if _, ok := cfg.Sites["home-c"]; !ok {
		t.Error("the declaration was changed")
	}
	ev.Joined["etcd's membership"] = []string{"home-c"}
	c, _ = topology.Classify(cfg, ev)
	if eff := c.Effective(cfg); !slices.Contains(eff.Etcd.Members, "home-c") {
		t.Errorf("a down site was left out: %v", eff.Etcd.Members)
	}
}

// Review Focus: with Garage short of its replication, an app the
// declaration keeps in object storage is held back, never rendered with
// local storage; one the deployment record lists as deployed refuses.
func TestEffectiveTopologyNeverMovesAnAppToLocalStorage(t *testing.T) {
	cfg := fixture(t)
	var s3 []string
	for _, name := range cfg.AppNames() {
		if blueprints.InObjectStorage(cfg, cfg.Apps[name]) {
			s3 = append(s3, name)
		}
	}
	if len(s3) == 0 {
		t.Fatal("the fixture has no app in object storage")
	}
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = refused
	delete(ev.Records, "home-c")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !c.StorageHeld() || !slices.Equal(c.Held, s3) {
		t.Fatalf("held %v, want %v", c.Held, s3)
	}
	eff := c.Effective(cfg)
	for _, name := range eff.AppNames() {
		if blueprints.InObjectStorage(cfg, cfg.Apps[name]) && !blueprints.InObjectStorage(eff, eff.Apps[name]) {
			t.Errorf("%s moved to local storage", name)
		}
	}
	ev.DeployedApps = s3[:1]
	_, err = topology.Classify(cfg, ev)
	if err == nil || !strings.Contains(err.Error(), s3[0]) {
		t.Errorf("a deployed app held back: %v", err)
	}
	lowerCase(t, err)

	// With a site outside Garage pending, Garage keeps its replication: no
	// app is held, and every one in object storage stays there.
	ev = everyoneAnswers(t, cfg)
	ev.Login["vm"] = refused
	delete(ev.Records, "vm")
	if slices.Contains(cfg.Storage.Garage.Sites, "vm") || len(cfg.PinnedTo("vm")) > 0 {
		t.Fatal("the fixture's vm holds Garage or an app")
	}
	c, err = topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageHeld() || c.Sites["vm"].Class != topology.Pending {
		t.Fatalf("held %v, vm %+v", c.Held, c.Sites["vm"])
	}
	eff = c.Effective(cfg)
	for _, name := range s3 {
		if app, ok := eff.Apps[name]; !ok || !blueprints.InObjectStorage(eff, app) {
			t.Errorf("%s left object storage (in the run: %v)", name, ok)
		}
	}
}

// A site that dials out only, whose every route into the mesh is through
// pending sites, is pending too.
func TestASiteReachableOnlyThroughAPendingRelayIsPending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	for _, name := range cfg.SiteNames() {
		if cfg.Sites[name].DialEndpoint() != "" {
			ev.Login[name] = refused
			delete(ev.Records, name)
		}
	}
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.SiteNames() {
		if c.Sites[name].Class != topology.Pending {
			t.Errorf("%s is %v", name, c.Sites[name].Class)
		}
	}
}

// The spec's refusals: the gateway pending, the gateway down, no ready data
// site while an app needs a database, and --site naming a pending site.
func TestRefusals(t *testing.T) {
	cfg := fixture(t)
	gw := cfg.GatewaySites()[0]
	for name, set := range map[string]func(*topology.Evidence){
		"gateway pending": func(ev *topology.Evidence) { ev.Login[gw] = refused; delete(ev.Records, gw) },
		"gateway down": func(ev *topology.Evidence) {
			ev.Login[gw] = refused
			delete(ev.Records, gw)
			ev.Joined["the deployment record"] = []string{gw}
		},
		"no ready data site": func(ev *topology.Evidence) {
			for _, s := range cfg.DataSites() {
				ev.Login[s] = refused
				delete(ev.Records, s)
			}
		},
	} {
		ev := everyoneAnswers(t, cfg)
		set(&ev)
		c, err := topology.Classify(cfg, ev)
		if err == nil {
			err = c.Refuse(cfg, "")
		}
		if err == nil {
			t.Errorf("%s: not refused", name)
		}
		lowerCase(t, err)
	}
	ev := everyoneAnswers(t, cfg)
	ev.Login["home-c"] = refused
	delete(ev.Records, "home-c")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Refuse(cfg, "home-c"); err == nil || !strings.Contains(err.Error(), "--site home-c") {
		t.Errorf("--site pending: %v", err)
	}
	if err := c.Refuse(cfg, ""); err != nil || !c.Degraded() {
		t.Errorf("one pending data site of three: %v, degraded %v", err, c.Degraded())
	}
}

// A ready site the deployment has not joined is made pending when its join
// has to wait, Eg: while a joined site is down. What hangs on the pending
// set is judged again: storage is held when Garage is left short, and an
// app the deployment runs that Garage could then not hold refuses.
func TestAJoinThatWaitsMakesItsSitePending(t *testing.T) {
	cfg := fixture(t)
	ev := everyoneAnswers(t, cfg)
	ev.Joined["etcd's membership"] = []string{"home-a", "home-b"}
	ev.Login["home-a"] = refused
	delete(ev.Records, "home-a")
	c, err := topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Ready("home-c") || c.StorageHeld() {
		t.Fatalf("before: home-c %+v, held %v", c.Sites["home-c"], c.Held)
	}
	const why = "its join waits until every joined site answers"
	if err := c.Wait(cfg, []string{"home-c"}, why); err != nil {
		t.Fatal(err)
	}
	if got := c.Sites["home-c"]; got.Class != topology.Pending || got.Why != "home-c: "+why {
		t.Errorf("home-c %+v", got)
	}
	if !c.StorageHeld() || len(c.Warnings) != 1 {
		t.Errorf("held %v, warnings %v", c.Held, c.Warnings)
	}
	if slices.Contains(c.Effective(cfg).Etcd.Members, "home-c") {
		t.Error("home-c is still an etcd member of the run")
	}

	ev.DeployedApps = c.Held[:1]
	c, err = topology.Classify(cfg, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(cfg, []string{"home-c"}, why); err == nil || !strings.Contains(err.Error(), ev.DeployedApps[0]) {
		t.Errorf("a deployed app held back: %v", err)
	}
}

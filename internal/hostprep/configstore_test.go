package hostprep_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/hostprep"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const (
	storeRepo   = "/var/lib/paisans/config/f2a9.git"
	storePeers  = "/var/lib/paisans/config/f2a9.peers"
	storeScript = "/usr/local/lib/paisans-f2a9/config-sync.sh"
	storeDaemon = "/etc/systemd/system/paisans-f2a9-config-daemon.service"
	storeSyncSv = "/etc/systemd/system/paisans-f2a9-config-sync.service"
	storeTimer  = "/etc/systemd/system/paisans-f2a9-config-sync.timer"

	probeDaemonUnit = "is-enabled paisans-f2a9-config-daemon.service"
	probeTimerUnit  = "is-enabled paisans-f2a9-config-sync.timer"
)

func storePlan(t *testing.T, host *fakeHost, peers []string) *hostprep.Plan {
	t.Helper()
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture(t)
	section, err := profile.ConfigStore(host, cfg.Deployment(), cfg.Sites["home-a"], peers)
	if err != nil {
		t.Fatal(err)
	}
	plan := &hostprep.Plan{}
	plan.Steps = section.Steps
	return plan
}

func fileSteps(p *hostprep.Plan) map[string]uint32 {
	out := map[string]uint32{}
	for _, s := range p.Steps {
		if s.File != nil {
			out[s.File.Path] = s.File.Mode
		}
	}
	return out
}

// settled is a host that holds exactly what the store plans, enabled and running.
func settled(t *testing.T, peers []string) *fakeHost {
	t.Helper()
	host := freshHost()
	plan := storePlan(t, host, peers)
	for _, s := range plan.Steps {
		if s.File != nil {
			host.files[s.File.Path] = s.File.Content
		}
	}
	host.files[storeRepo+"/HEAD"] = "ref: refs/heads/main\n"
	host.responses[probeDaemonUnit] = "enabled enabled\nactive active\n"
	host.responses[probeTimerUnit] = "enabled enabled\nactive active\n"
	return host
}

func TestAFreshHostPlansTheWholeStore(t *testing.T) {
	host := freshHost()
	plan := storePlan(t, host, []string{"10.44.0.2", "10.44.0.3"})

	want := map[string]uint32{
		storePeers: 0o644, storeScript: 0o755,
		storeDaemon: 0o644, storeSyncSv: 0o644, storeTimer: 0o644,
	}
	got := fileSteps(plan)
	if len(got) != len(want) {
		t.Fatalf("files planned %v, want %v", got, want)
	}
	for p, mode := range want {
		if got[p] != mode {
			t.Errorf("%s planned with mode %o, want %o", p, got[p], mode)
		}
	}
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == storePeers && s.File.Content != "10.44.0.2\n10.44.0.3\n" {
			t.Errorf("peers file is %q", s.File.Content)
		}
	}

	var commands []string
	for _, s := range plan.Steps {
		commands = append(commands, s.Command)
	}
	all := strings.Join(commands, "\n")
	for _, w := range []string{"git init --bare", storeRepo, "systemctl daemon-reload && systemctl enable --now paisans-f2a9-config-daemon.service paisans-f2a9-config-sync.timer"} {
		if !strings.Contains(all, w) {
			t.Errorf("no command contains %q:\n%s", w, all)
		}
	}
	if first := plan.Steps[0]; !strings.Contains(first.Command, "git init --bare") {
		t.Errorf("the repository is not created first: %q", first.Command)
	}
	if last := plan.Steps[len(plan.Steps)-1]; !strings.Contains(last.Command, "enable --now") {
		t.Errorf("enabling is not last: %q", last.Command)
	}

	// Executing writes exactly the planned paths.
	if err := hostprep.Execute(plan, host, &ui.Recorder{}); err != nil {
		t.Fatal(err)
	}
	var wrote []string
	wrote = append(wrote, host.written...)
	sort.Strings(wrote)
	var paths []string
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if strings.Join(wrote, ",") != strings.Join(paths, ",") {
		t.Errorf("wrote %v, want %v", wrote, paths)
	}
}

func TestTheRepositoryIsCreatedOnlyWhereHeadIsAbsentAndStaysReadableByNobody(t *testing.T) {
	host := settled(t, []string{"10.44.0.2"})
	delete(host.files, storeRepo+"/HEAD")
	plan := storePlan(t, host, []string{"10.44.0.2"})
	if len(plan.Steps) != 1 {
		t.Fatalf("got %d steps, want only the repository", len(plan.Steps))
	}
	c := plan.Steps[0].Command
	for _, w := range []string{"umask 022", "--shared=0644", "git init --bare", "chmod 0755", "chmod 0644"} {
		if !strings.Contains(c, w) {
			t.Errorf("the repository command lacks %q:\n%s", w, c)
		}
	}
}

func TestAHostAlreadyHoldingTheStorePlansNothing(t *testing.T) {
	peers := []string{"10.44.0.2", "10.44.0.3"}
	plan := storePlan(t, settled(t, peers), peers)
	if len(plan.Steps) != 0 {
		t.Errorf("planned %d step(s) on a host that is in state: %s", len(plan.Steps), describes(plan))
	}
}

func TestAChangedPeerListPlansOnlyThePeersFile(t *testing.T) {
	host := settled(t, []string{"10.44.0.2"})
	plan := storePlan(t, host, []string{"10.44.0.2", "10.44.0.3"})
	if len(plan.Steps) != 1 || plan.Steps[0].File == nil || plan.Steps[0].File.Path != storePeers || plan.Steps[0].Command != "" {
		t.Fatalf("want one step writing the peers file, got: %s", describes(plan))
	}
}

func TestAUnitThatIsNotRunningIsEnabledAgain(t *testing.T) {
	host := settled(t, nil)
	host.responses[probeDaemonUnit] = "enabled disabled\nactive inactive\n"
	plan := storePlan(t, host, nil)
	if len(plan.Steps) != 1 || !strings.Contains(plan.Steps[0].Command, "enable --now") {
		t.Fatalf("got: %s", describes(plan))
	}
}

func TestAChangedDaemonUnitRestartsTheDaemon(t *testing.T) {
	host := settled(t, nil)
	host.files[storeDaemon] = "stale"
	plan := storePlan(t, host, nil)
	last := plan.Steps[len(plan.Steps)-1].Command
	if !strings.Contains(last, "systemctl restart paisans-f2a9-config-daemon.service") {
		t.Errorf("the daemon keeps running the old unit: %q", last)
	}
}

func TestTheDaemonServesOnlyThisDeploymentsRepositoryOnTheMeshAddress(t *testing.T) {
	plan := storePlan(t, freshHost(), nil)
	var unit string
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == storeDaemon {
			unit = s.File.Content
		}
	}
	for _, w := range []string{
		"--listen=10.44.0.1 ", "--port=9418", "--base-path=/var/lib/paisans/config",
		"/var/lib/paisans/config/f2a9.git\n", "User=nobody", "After=wg-quick@psns-f2a9\n", "Requires=wg-quick@psns-f2a9\n",
	} {
		if !strings.Contains(unit, w) {
			t.Errorf("the daemon unit lacks %q:\n%s", w, unit)
		}
	}
	if strings.Contains(unit, "--enable=") || strings.Contains(unit, "receive-pack") {
		t.Errorf("the daemon unit can be written to:\n%s", unit)
	}
}

func TestBuildGivesEverySiteTheStoreWithTheOtherSitesAsPeers(t *testing.T) {
	cfg := fixture(t)
	plan, err := hostprep.Build("home-a", cfg, freshHost())
	if err != nil {
		t.Fatal(err)
	}
	var peers string
	for _, s := range plan.Steps {
		if s.File != nil && s.File.Path == storePeers {
			peers = s.File.Content
		}
	}
	var want []string
	for _, name := range cfg.SiteNames() {
		if name != "home-a" {
			want = append(want, cfg.Sites[name].Address+"\n")
		}
	}
	if peers != strings.Join(want, "") || peers == "" {
		t.Errorf("peers %q, want %q", peers, strings.Join(want, ""))
	}
	if !strings.Contains(strings.Join(plan.Kinds(), ","), hostprep.KindConfigStore) {
		t.Errorf("kinds %v lack the configuration store", plan.Kinds())
	}
}

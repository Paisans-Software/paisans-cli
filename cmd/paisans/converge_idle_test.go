package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// foundedCluster has every etcd member of the fixture recorded, so the run
// founds and joins nothing.
func foundedCluster(t *testing.T) {
	t.Helper()
	founded := initial("home-a", "home-b", "vm")
	convergeReadInitial = func(*config.Config, string) (render.EtcdInitial, bool, error) { return founded, true, nil }
}

// busy is the plan of a site whose apply has something to do: a stack to
// bring up, as a container that stopped needs.
func busy(site string) *apply.Plan {
	return &apply.Plan{Site: site, Actions: []apply.Action{{Stack: "infra", Recreate: true}}}
}

// everySiteBusy gives every site's plan work, so the run applies each one
// as planned rather than leaving it alone.
func everySiteBusy(t *testing.T) {
	t.Helper()
	plans := map[string]*apply.Plan{}
	for _, s := range preparedSites {
		plans[s] = busy(s)
	}
	fakePulls(t, plans, nil)
}

// grantHosts gives each site a grant record of its own, and returns them.
func grantHosts(t *testing.T) map[string]*memGrant {
	t.Helper()
	var mu sync.Mutex
	hosts := map[string]*memGrant{}
	saved := convergeGrantHost
	convergeGrantHost = func(_ *config.Config, site string) registry.Runner {
		mu.Lock()
		defer mu.Unlock()
		if hosts[site] == nil {
			hosts[site] = &memGrant{}
		}
		return hosts[site]
	}
	t.Cleanup(func() { convergeGrantHost = saved })
	return hosts
}

// applied is each deployment apply the run ran.
func applied(ran []string) []string {
	var out []string
	for _, r := range ran {
		if strings.HasPrefix(r, "deployment apply") {
			out = append(out, r)
		}
	}
	return out
}

// A run with nothing to do on any site applies none of them: each one's
// pull and install line say it is unchanged, and its grant record is
// written as its apply would.
func TestApplyLeavesAloneASiteWithNothingToDo(t *testing.T) {
	ran := fakeConverge(t, nil)
	foundedCluster(t)
	hosts := grantHosts(t)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if got := applied(*ran); len(got) != 0 {
		t.Errorf("applied %v, want none", got)
	}
	if !slices.Contains(*ran, "storage add") {
		t.Errorf("storage add did not run: %v", *ran)
	}
	all := []string{"done home-a: unchanged", "done home-b: unchanged", "done vm: unchanged", "done watch: unchanged"}
	sameLines(t, ended(rec, "pull"), all)
	install := ended(rec, "install")
	for _, want := range all {
		if !slices.Contains(install, want) {
			t.Errorf("install lines %v, want %q", install, want)
		}
	}
	for _, site := range preparedSites {
		if h := hosts[site]; h == nil || h.writes == 0 {
			t.Errorf("%s's grant record was not written", site)
		}
	}
}

// A site whose plan has work is applied, in both its visits, and only it.
func TestApplyAppliesOnlyTheSiteWithWork(t *testing.T) {
	ran := fakeConverge(t, nil)
	foundedCluster(t)
	grantHosts(t)
	fakePulls(t, map[string]*apply.Plan{"home-b": busy("home-b")}, nil)
	recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	got := applied(*ran)
	if len(got) == 0 {
		t.Fatal("home-b was not applied")
	}
	for _, a := range got {
		if a != "deployment apply --site home-b" {
			t.Errorf("applied %q, want only home-b", a)
		}
	}
}

// Each thing besides a file or a stack that a site's apply would do is
// work, and the site is applied.
func TestApplyAppliesASiteWithAnyWork(t *testing.T) {
	plans := map[string]*apply.Plan{
		"an action":   busy("home-b"),
		"a write":     {Site: "home-b", Changes: []apply.Change{{Path: "infra/compose.yaml", Kind: apply.Update}}},
		"a mesh step": {Site: "home-b", WireGuard: apply.WireGuardStart},
		"a sweep":     {Site: "home-b", Sweep: []apply.Prune{{}}},
		"a grant":     {Site: "home-b", Bootstrap: &apply.Bootstrap{}},
		"an image":    toPull("home-b", gib(20), "ghcr.io/example/home-b:2"),
		"a conflict":  {Site: "home-b", Changes: []apply.Change{{Path: "infra/compose.yaml", Kind: apply.Conflict}}},
		"a leftover":  {Site: "home-b", Leftovers: []render.ManifestFile{{}}},
	}
	for name, plan := range plans {
		t.Run(name, func(t *testing.T) {
			ran := fakeConverge(t, nil)
			foundedCluster(t)
			grantHosts(t)
			fakePulls(t, map[string]*apply.Plan{"home-b": plan}, nil)
			recordConverge(t)
			if err := converge(t, "--execute"); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(*ran, "deployment apply --site home-b") {
				t.Errorf("home-b was not applied with %s: ran %v", name, *ran)
			}
			if slices.Contains(*ran, "deployment apply --site home-a") {
				t.Errorf("home-a was applied with nothing to do")
			}
		})
	}
}

// A site whose clients Pocket ID would make or change is applied.
func TestApplyAppliesASiteWithAClientToMake(t *testing.T) {
	ran := fakeConverge(t, nil)
	foundedCluster(t)
	grantHosts(t)
	convergeIdleClients = func(_ *config.Config, _ *config.Secrets, _, site string) bool { return site != "vm" }
	recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if got := applied(*ran); len(got) == 0 || slices.ContainsFunc(got, func(a string) bool { return a != "deployment apply --site vm" }) {
		t.Errorf("applied %v, want vm alone", got)
	}
}

// A site whose review the operator approved is applied, so what was
// approved is put in place.
func TestApplyAppliesASiteWithAnApprovedReview(t *testing.T) {
	ran := fakeConverge(t, nil)
	foundedCluster(t)
	fakeRunGrants(t, "home-a")
	if err := convergeWith(t, answering(t, approveWord+"\n"), new(strings.Builder), "--execute"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*ran, "deployment apply --site home-a") {
		t.Errorf("home-a was not applied: %v", *ran)
	}
	if slices.Contains(*ran, "deployment apply --site home-b") {
		t.Errorf("home-b was applied with nothing to do or approve")
	}
}

// A step that publishes another version may change what a site left alone
// renders, so that site is applied after all, once the rest have run.
func TestApplyAppliesASiteLeftAloneOnceTheVersionMoves(t *testing.T) {
	ran := fakeConverge(t, nil)
	foundedCluster(t)
	grantHosts(t)
	fakePulls(t, map[string]*apply.Plan{"home-b": busy("home-b")}, nil)
	recordConverge(t)
	run := convergeRun
	convergeRun = func(args []string) error {
		if v := reviewedByRun; v != nil && strings.Join(args[:min(4, len(args))], " ") == "deployment apply --site home-b" {
			v.version = &appliedVersion{sha: "moved"}
		}
		return run(args)
	}
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	got := applied(*ran)
	for _, site := range []string{"home-a", "vm", "watch"} {
		if !slices.Contains(got, "deployment apply --site "+site) {
			t.Errorf("%s was not applied once the version moved: %v", site, got)
		}
	}
	if len(got) < 3 {
		t.Fatalf("applied %v", got)
	}
	if last := got[len(got)-3:]; slices.Contains(last, "deployment apply --site home-b") {
		t.Errorf("the sites left alone did not run last: %v", got)
	}
}

// A gateway left alone is still asked from outside, as its apply ends, and
// one that does not answer as this deployment stops the run before its
// grant record is written.
func TestApplyChecksAGatewayLeftAlone(t *testing.T) {
	ran := fakeConverge(t, nil)
	foundedCluster(t)
	hosts := grantHosts(t)
	rec := recordConverge(t)
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(data), "  vm:\n    roles: [gateway, witness]\n", "  vm:\n    roles: [gateway, witness]\n    public_address: 203.0.113.30\n", 1)
	if body == string(data) {
		t.Fatal("the fixture has no vm gateway to edit")
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := gatewayProbe
	t.Cleanup(func() { gatewayProbe = saved })
	asked := false
	gatewayProbe = func() selfcheck.Probe {
		return selfcheck.Probe{Fetch: func(context.Context, string, string, string, string) (selfcheck.Answer, error) {
			asked = true
			return selfcheck.Answer{Status: 200, Body: "hello"}, nil
		}, Now: time.Now, Sleep: func(time.Duration) {}, Wait: time.Second}
	}
	captureStdout(t, func() {
		err = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--execute"}, strings.NewReader(""), io.Discard)
	})
	if err == nil || !strings.Contains(problemText(err), "not as this deployment") {
		t.Fatalf("err = %v, want the gateway refused\n%s", err, rec.Lines())
	}
	if !asked {
		t.Error("the gateway was not asked")
	}
	if got := applied(*ran); len(got) != 0 {
		t.Errorf("applied %v", got)
	}
	if h := hosts["vm"]; h != nil && h.writes > 0 {
		t.Error("vm's grant record was written although its gateway did not answer")
	}
}

// A run that founds etcd members runs every apply as planned.
func TestApplyLeavesNoSiteAloneWhileFounding(t *testing.T) {
	ran := fakeConverge(t, nil)
	grantHosts(t)
	recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	for _, site := range preparedSites {
		if !slices.Contains(*ran, "deployment apply --site "+site) {
			t.Errorf("%s was not applied: %v", site, *ran)
		}
	}
}

func TestLeavesIdle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []convergeStep
		want  bool
	}{
		{"applies alone", []convergeStep{applyStep(stageOthers, "a"), applyStep(stagePassTwo, "a")}, true},
		{"founding", []convergeStep{applyStep(stageFounding, "a")}, false},
		{"joining", []convergeStep{step(stageJoining, "site", "add", "a")}, false},
		{"gateway move", []convergeStep{step(stageGateway, "gateway", "move")}, false},
		{"held apps", []convergeStep{step(stageOthers, "deployment", "apply", "--site", "g", "--hold-apps")}, false},
	} {
		if got := leavesIdle(tc.steps); got != tc.want {
			t.Errorf("%s: leavesIdle = %v, want %v", tc.name, got, tc.want)
		}
	}
}

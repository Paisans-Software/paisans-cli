package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// movingHost is a fresh home-a whose Patroni leads until the
// infrastructure stack's `up -d`, and home-b leads after it: what a recreate
// of the leader's Patroni does.
type movingHost struct {
	emptyHost
	moved bool
}

func (h *movingHost) Run(command string) (string, error) {
	switch {
	case strings.Contains(command, "/infra/compose.yaml up -d"):
		h.moved = true
	case strings.HasSuffix(command, ":8008/patroni"):
		return `{"state":"running","role":"primary"}`, nil
	case strings.HasSuffix(command, ":8008/cluster"):
		leader := "home-a"
		if h.moved {
			leader = "home-b"
		}
		return `{"members":[{"name":"` + leader + `","role":"leader","state":"running"}]}`, nil
	case strings.HasSuffix(command, " ps --all --format json"):
		return `{"Service":"app","Name":"x","State":"running","Health":""}` + "\n", nil
	}
	return h.emptyHost.Run(command)
}

// A step whose apply moved Patroni's leader reports it under its install
// line, which shows only with -v. The run says it again once its steps are
// done, as a warning of its own, at every verbosity, and says it whether
// the run went on to finish or stopped at a later step. The move comes from
// a real apply.Execute, through apply.OnLeaderMove.
func TestConvergeSaysTheLeaderMoved(t *testing.T) {
	for _, stop := range []bool{false, true} {
		var fail map[string]error
		if stop {
			fail = map[string]error{"storage add": errors.New("the layout did not apply")}
		}
		fakeConverge(t, fail)
		rec := recordConverge(t)
		savedRun := convergeRun
		moved := false
		convergeRun = func(args []string) error {
			if isApply(args) && siteOf(args) == "home-a" && !moved {
				moved = true
				// The replica's wait for its databases stops the apply;
				// the move is found before it.
				_ = apply.Execute(clusteredPlan(t, ui.Discard), &movingHost{})
			}
			return savedRun(args)
		}
		err := converge(t, "--execute")
		convergeRun = savedRun
		if (err != nil) != stop {
			t.Fatalf("stop %t: %v", stop, err)
		}
		var warned []ui.Event
		for _, e := range rec.Events {
			if e.Kind == "warn" && strings.Contains(e.Text, "leader moved") {
				warned = append(warned, e)
			}
		}
		if len(warned) != 1 {
			t.Fatalf("stop %t: want one warning of the move, got %d:\n%s", stop, len(warned), rec.Lines())
		}
		if w := warned[0]; w.Text != "Patroni's leader moved from home-a to home-b" || !strings.Contains(w.Extra, "recreate infra") {
			t.Errorf("stop %t: warned %q: %q", stop, w.Text, w.Extra)
		}
	}
}

// clusteredPlan is home-a's first apply of freshSite with home-b in its
// cluster, its database work attached.
func clusteredPlan(tb testing.TB, r ui.Reporter) *apply.Plan {
	tb.Helper()
	site := strings.Replace(freshSite, "cluster:\n  sites: [home-a]", "cluster:\n  sites: [home-a, home-b]", 1)
	site = strings.Replace(site, "cluster:\n", `  home-b:
    roles: [data]
    address: 10.44.0.2
    ssh:
      host: home-b.local
      user: ubuntu
      keys: [alice]
cluster:
`, 1)
	path := filepath.Join(tb.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(site), 0o600); err != nil {
		tb.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		tb.Fatal(err)
	}
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		tb.Fatal(err)
	}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		tb.Fatal(err)
	}
	plan, err := apply.Build("home-a", rendered, acme.Module(cfg.ACME.Provider), emptyHost{}, apply.Report(r))
	if err != nil {
		tb.Fatal(err)
	}
	databases, err := apply.Databases(cfg, secrets, "home-a")
	if err != nil {
		tb.Fatal(err)
	}
	plan.WithDatabases(databases)
	return plan
}

// A run in which no apply moved the leader says nothing about it.
func TestConvergeSaysNothingOfALeaderThatStayed(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if rec.Has("warn", "leader moved") {
		t.Errorf("a move was reported:\n%s", rec.Lines())
	}
}

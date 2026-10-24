package apply_test

import (
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// A service that joins another's namespace is restarted after it, never with
// it: `docker compose restart` of the whole project stops the app, which ends
// the namespace and the joined container with it, and starts the joined one
// while the app is still down, which fails with "failed to join PID
// namespace" and is not retried.
func TestRestartTiersOrderNamespaceJoins(t *testing.T) {
	for _, tc := range []struct {
		name, compose string
		want          [][]string
	}{
		{"none shared", "services:\n  app: {}\n  postgres: {}\n", nil},
		{"pid", "services:\n  app: {}\n  bootstrap:\n    pid: \"service:app\"\n  postgres: {}\n",
			[][]string{{"app", "postgres"}, {"bootstrap"}}},
		{"network and ipc chain", "services:\n  vpn: {}\n  app:\n    network_mode: service:vpn\n  sidecar:\n    ipc: service:app\n",
			[][]string{{"vpn"}, {"app"}, {"sidecar"}}},
		{"container, not service", "services:\n  app:\n    pid: host\n  b:\n    network_mode: container:abc\n", nil},
	} {
		got, err := apply.RestartTiers(tc.compose)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: tiers %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRestartTiersRefusesACycle(t *testing.T) {
	if _, err := apply.RestartTiers("services:\n  a:\n    pid: service:b\n  b:\n    pid: service:a\n"); err == nil {
		t.Error("a namespace cycle was accepted")
	}
}

func TestWholeStackRestartRunsTiersInOrder(t *testing.T) {
	a := apply.Action{Stack: "wiki", Tiers: [][]string{{"app", "postgres"}, {"bootstrap"}}}
	compose := apply.Fixture.ComposeCmd("wiki")
	want := compose + " restart 'app' 'postgres' && " + compose + " restart 'bootstrap'"
	if got := a.Command(apply.Fixture); got != want {
		t.Errorf("runs %q, want %q", got, want)
	}
}

// Wiki.js's bootstrap runs in the app's PID namespace, so the restart a moved
// database path owes the wiki takes the app first and the bootstrap after it.
func TestADatabasePathRestartTakesTheWikiAppBeforeItsBootstrap(t *testing.T) {
	host := appliedHost(t)
	p, err := apply.Build("home-a", planChanging(t, "srv/paisans/f2a9/infra/haproxy/haproxy.cfg"), acmeModule(t), host, databaseApps(t))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := actionOn(p, "wiki")
	if !ok {
		t.Fatal("the wiki is not restarted")
	}
	compose := apply.Fixture.ComposeCmd("wiki")
	if want := compose + " restart 'app' && " + compose + " restart 'bootstrap'"; a.Command(apply.Fixture) != want {
		t.Errorf("the wiki runs %q, want %q", a.Command(apply.Fixture), want)
	}
	if a, _ := actionOn(p, "talk"); len(a.Tiers) != 0 {
		t.Errorf("talk shares no namespace and is restarted in tiers %v", a.Tiers)
	}
}

package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// Every command that can stop the infrastructure stack's Patroni passes its
// 60 s stop timeout, since a container being replaced stops with its own
// timeout otherwise, and a Patroni created before it had one would be
// killed at Docker's 10 s with the watchdog armed. A restart of other named
// services, and every other stack, keeps Compose's own.
func TestCommandsThatStopPatroniPassItsTimeout(t *testing.T) {
	for _, c := range []struct {
		name string
		a    apply.Action
		want bool
	}{
		{"infra up", apply.Action{Stack: "infra", Recreate: true}, true},
		{"infra narrowed up", apply.Action{Stack: "infra", Recreate: true, Up: []string{"etcd", "haproxy"}}, true},
		{"infra forced up", apply.Action{Stack: "infra", Force: true}, true},
		{"infra restart", apply.Action{Stack: "infra"}, true},
		{"patroni restart", apply.Action{Stack: "infra", Services: []string{"patroni"}}, true},
		{"haproxy restart", apply.Action{Stack: "infra", Services: []string{"haproxy"}}, false},
		{"app up", apply.Action{Stack: "talk", Recreate: true}, false},
	} {
		if got := strings.Contains(c.a.Command(apply.Fixture), " --timeout 60"); got != c.want {
			t.Errorf("%s: %q passes the timeout %v, want %v", c.name, c.a.Command(apply.Fixture), got, c.want)
		}
	}
	if !strings.Contains(apply.RecreatePatroni(apply.Fixture), " --timeout 60 patroni") {
		t.Errorf("RecreatePatroni is %q", apply.RecreatePatroni(apply.Fixture))
	}
}

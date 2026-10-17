package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/topology"
)

// A site that did not answer is a warning once the classification says it
// is pending or down, saying what the deployment is without and what to
// run; a gateway is a failure either way, since apply refuses to run
// without it; a site the classification did not place is a failure, as
// before, with the classification's refusal when there was one. A refused
// login and a timeout read the same.
func TestAbsentSites(t *testing.T) {
	cfg := fixture(t)
	timeout := "ssh: connect to host home-b.local port 22: Operation timed out"
	refused := "ubuntu@home-b.local: Permission denied (publickey)."
	pendingB := topology.Site{Name: "home-b", Class: topology.Pending, Request: true}
	for _, c := range []struct {
		name  string
		reach SiteReach
		level Level
		line  string
		more  []string
	}{
		{"pending", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: refused, Class: pendingB}, Warn,
			"home-b: pending: its login is refused or it does not answer, and the deployment has not joined it",
			[]string{"roles: data, apps", "ssh to ubuntu@home-b.local: ubuntu@home-b.local: Permission denied (publickey).", "Until it joins, the deployment is without one of 3 etcd votes", "`paisans host request --site home-b > home-b.admit`"}},
		{"down, timed out", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout, Class: topology.Site{Name: "home-b", Class: topology.Down}}, Warn,
			topology.DownLine("home-b"),
			[]string{"roles: data, apps", "While it is down the deployment is without one of 3 etcd votes", "ssh to ubuntu@home-b.local: Operation timed out", "recover:\n  1. Start the host or fix the route to it."}},
		{"down, refused", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: refused, Class: topology.Site{Name: "home-b", Class: topology.Down}}, Warn,
			topology.DownLine("home-b"), []string{"roles: data, apps", "ssh to ubuntu@home-b.local: ubuntu@home-b.local: Permission denied (publickey).", "recover:"}},
		{"gateway down", SiteReach{Site: "vm", Destination: "ubuntu@vm.example.org", Err: timeout, Class: topology.Site{Name: "vm", Class: topology.Down}}, Fail,
			topology.DownLine("vm"), []string{"its gateway"}},
		{"gateway pending", SiteReach{Site: "vm", Destination: "ubuntu@vm.example.org", Err: refused, Class: topology.Site{Name: "vm", Class: topology.Pending, Request: true}}, Fail,
			"vm: pending: its login is refused or it does not answer, and the deployment has not joined it",
			[]string{"`paisans host request --site vm > vm.admit`"}},
		{"unclassified", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout, Unclassified: "home-b: its login is refused or it does not answer, and whether it has joined the deployment cannot be told"}, Fail,
			"home-b: ssh to ubuntu@home-b.local did not answer (Operation timed out)",
			[]string{"roles: data, apps", "While it is down the deployment is without one of 3 etcd votes", "Whether it has joined the deployment could not be told: home-b: its login", "Start the host or fix the route to it."}},
		{"placed ready, yet not answering", SiteReach{Site: "home-b", Destination: "ubuntu@home-b.local", Err: timeout, Class: topology.Site{Name: "home-b", Class: topology.Ready}}, Fail,
			"home-b: ssh to ubuntu@home-b.local did not answer (Operation timed out)",
			[]string{"The run's classification found it ready, yet it did not answer here: its login fails some of the time."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Reach(cfg, []SiteReach{c.reach})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			if f.Level != c.level || f.Line != c.line {
				t.Errorf("got %v %q", f.Level, f.Line)
			}
			text := strings.Join(f.More, "\n")
			for _, want := range c.more {
				if !strings.Contains(text, want) {
					t.Errorf("lacks %q:\n%s", want, text)
				}
			}
		})
	}
}

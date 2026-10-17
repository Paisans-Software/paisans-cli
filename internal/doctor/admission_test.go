package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

// admittedRecord is the record site admit leaves for a site of cfg as
// declared, with every grant its roles need. It panics rather than take a
// *testing.T, since the graph tests' reads call it.
func admittedRecord(cfg *config.Config, site string) admission.Record {
	req, err := admission.Build(cfg, site)
	if err != nil {
		panic(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if cfg.Sites[site].WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "nginx", Process: "nginx"}
	}
	return rec
}

func pendingClass(site string) topology.Site {
	return topology.Site{Name: site, Class: topology.Pending, Why: site + ": its host has not admitted this deployment", Request: true}
}

func TestAdmission(t *testing.T) {
	cfg := fixture(t)
	noWatchdog := admittedRecord(cfg, "home-b")
	noWatchdog.Grants.Watchdog = false
	moved := admittedRecord(cfg, "home-a")
	moved.Ports.SSH = 2222
	noPorts := admittedRecord(cfg, "vm")
	noPorts.Grants.PublicPorts = false
	noPorts.Ports.Public = []int{}
	joined := "home-a has joined this deployment, and its admission does not cover it: the host opened ssh port 2222, and the site's is 22."

	for _, c := range []struct {
		name    string
		probe   AdmissionProbe
		level   Level
		line    string
		more    []string
		pending bool
	}{
		{"admitted", AdmissionProbe{Site: "home-a", Found: true, Record: admittedRecord(cfg, "home-a")}, OK,
			"home-a: admitted as ubuntu, covering its roles", nil, false},
		{"not admitted, pending", AdmissionProbe{Site: "home-b", Class: pendingClass("home-b")}, Warn,
			"home-b: pending: this deployment is not admitted on its host",
			[]string{"roles: data, apps", "Until it joins, the deployment is without one of 3 etcd votes", "`paisans site request --site home-b > home-b.admit`"}, true},
		{"without the watchdog, pending", AdmissionProbe{Site: "home-b", Found: true, Record: noWatchdog, Class: pendingClass("home-b")}, Warn,
			"home-b: pending: its admission does not cover it",
			[]string{"the site's roles need --watchdog, which the host's owner did not grant", "--watchdog grants Patroni the machine's watchdog"}, true},
		{"gateway without the ports", AdmissionProbe{Site: "vm", Found: true, Record: noPorts, Class: pendingClass("vm")}, Fail,
			"vm: pending: its admission does not cover it",
			[]string{"--public-ports lets the deployment bind ports 80 and 443", "its gateway"}, true},
		{"differs, joined", AdmissionProbe{Site: "home-a", Found: true, Record: moved, Unclassified: joined}, Fail,
			"home-a: its admission does not cover it",
			[]string{"the host opened ssh port 2222, and the site's is 22", "The run's classification of the sites refuses: home-a has joined", "`paisans site request --site home-a > home-a.admit`"}, false},
		{"unreadable, pending", AdmissionProbe{Site: "home-b", Err: `the admission record for "f2a9" is refused:`, More: []string{"- version: \"2\""}, Class: pendingClass("home-b")}, Warn,
			"home-b: pending: its admission record cannot be read",
			[]string{"roles: data, apps", `- version: "2"`, "`paisans site request --site home-b > home-b.admit`"}, true},
		{"unreadable", AdmissionProbe{Site: "home-a", Err: `the admission record for "f2a9" is refused:`, More: []string{"- version: \"2\""}}, Fail,
			"home-a: its admission record cannot be read",
			[]string{`- version: "2"`, "`paisans site request --site home-a > home-a.admit`"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Admission(cfg, []AdmissionProbe{c.probe})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			text := strings.Join(f.More, "\n")
			if f.Level != c.level || f.Line != c.line {
				t.Errorf("got %v %q", f.Level, f.Line)
			}
			for _, want := range c.more {
				if !strings.Contains(text, want) {
					t.Errorf("lacks %q:\n%s", want, text)
				}
			}
			if got := c.probe.Pending(); got != c.pending {
				t.Errorf("Pending %v, want %v", got, c.pending)
			}
		})
	}
}

// Each missing grant adds what it gives, and a grant with no words of its
// own adds no empty line.
func TestGrantLines(t *testing.T) {
	got := grantLines([]string{"watchdog", "something-new"})
	if len(got) != 1 || !strings.HasPrefix(got[0], "--watchdog grants") {
		t.Errorf("grantLines: %q", got)
	}
}

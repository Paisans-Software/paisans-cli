package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// memOut is MemoryCommand's answer: mem bytes of RAM, swap bytes of swap on,
// and the forms of swap the host has.
func memOut(mem, swap string, active bool, forms ...string) string {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	has := func(form string) bool {
		for _, f := range forms {
			if f == form {
				return true
			}
		}
		return false
	}
	file := "absent"
	if has("file") {
		file = "present"
	}
	return "mem-total " + mem + "\nswap-total " + swap + "\nswap-active " + yes(active) +
		"\nswap-partition " + yes(has("partition")) + "\nswap-fstab " + yes(has("fstab")) +
		"\nswap-zram " + yes(has("zram")) + "\nswapfile " + file + "\n"
}

const (
	gib4   = "4113297408"
	gib2   = "2147479552"
	mib458 = "480247808"
	gib1   = "1003524096" // a 1 GiB VPS's MemTotal, 957 MiB
)

// Swap is on, configured and not on, or absent; each warning says why and
// what to do, and an answer doctor cannot read is a warning, never a pass.
func TestSwap(t *testing.T) {
	for _, c := range []struct {
		name  string
		probe MemoryProbe
		level Level
		line  string
		more  []string
	}{
		{"on", MemoryProbe{Site: "home-a", Out: memOut(gib4, gib2, true, "fstab", "file")}, OK, "home-a: on, 2.0 GiB", nil},
		{"none", MemoryProbe{Site: "vm", Out: memOut(mib458, "0", false)}, Warn, "vm: none on this host",
			[]string{"kernel kill a process", "`paisans site admit` again, which offers a swapfile"}},
		{"configured, not on", MemoryProbe{Site: "vm", Out: memOut(gib1, "0", false, "fstab", "file")}, Warn,
			"vm: configured but not on (a swap line in /etc/fstab, /swapfile exists)",
			[]string{"kernel kill a process", "swapon -a"}},
		{"unreadable", MemoryProbe{Site: "vm", Err: "Permission denied"}, Warn, "vm: could not read the host's memory and swap", []string{"Permission denied"}},
		{"garbled", MemoryProbe{Site: "vm", Out: "mem-total 4G\n"}, Warn, "vm: could not read the host's memory and swap", []string{`unreadable mem-total line "4G"`}},
		{"empty", MemoryProbe{Site: "vm", Out: ""}, Warn, "vm: could not read the host's memory and swap", []string{"no mem-total line"}},
		{"cut short", MemoryProbe{Site: "vm", Out: "mem-total 4113297408\n"}, Warn, "vm: could not read the host's memory and swap", []string{"no swap-active line"}},
		{"zram alone, on", MemoryProbe{Site: "vm", Out: memOut(gib1, gib2, true, "zram") + "zram-on " + gib2 + "\n"}, Warn, "vm: only zram on this host",
			[]string{"stretches the host's memory rather than adding to it", "offers a swapfile beside it"}},
		{"zram alone, configured, not on", MemoryProbe{Site: "vm", Out: memOut(gib1, "0", false, "zram")}, Warn, "vm: only zram on this host",
			[]string{"offers a swapfile beside it"}},
		{"on, partly zram", MemoryProbe{Site: "home-a", Out: memOut(gib4, gib2, true, "zram") + "zram-on 1073741824\n"}, OK, "home-a: on, 2.0 GiB, 1.0 GiB of it zram", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Swap([]MemoryProbe{c.probe})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			if f.Section != SectionSwap || f.Level != c.level || f.Line != c.line {
				t.Errorf("got %s %v %q", f.Section, f.Level, f.Line)
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

// Each site's memory is judged against its own roles' floor in the
// fixture: home-a and home-b are data and apps (3.0 GiB), vm gateway and
// witness (960 MiB), watch a monitor (896 MiB) with the fixture's Beszel
// hub left out. Swap that is on counts up to a quarter of the floor.
func TestMemory(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Apps, "stats")
	delete(cfg.Apps, "notify") // it serves the hub
	for _, c := range []struct {
		name  string
		probe MemoryProbe
		level Level
		line  string
	}{
		{"data and apps, 4 GB", MemoryProbe{Site: "home-a", Out: memOut(gib4, gib2, true, "file")}, OK,
			"home-a: 3.8 GiB of RAM and 2.0 GiB of swap, at least its 3.0 GiB floor for what it runs"},
		{"data and apps, 2 GiB and swap", MemoryProbe{Site: "home-b", Out: memOut("2147483648", "4294967296", true, "file")}, Warn,
			"home-b: 2.0 GiB of RAM and 4.0 GiB of swap, under its 3.0 GiB floor for what it runs"},
		{"gateway and witness, 458 MiB, no swap", MemoryProbe{Site: "vm", Out: memOut(mib458, "0", false)}, Warn,
			"vm: 458 MiB of RAM and no swap on, under its 960 MiB floor for what it runs"},
		{"gateway and witness, 1 GiB, swap on", MemoryProbe{Site: "vm", Out: memOut(gib1, gib2, true, "file")}, OK,
			"vm: 957 MiB of RAM and 2.0 GiB of swap, at least its 960 MiB floor for what it runs"},
		{"gateway and witness, 1 GiB, swap not on", MemoryProbe{Site: "vm", Out: memOut(gib1, "0", false, "file")}, Warn,
			"vm: 957 MiB of RAM and no swap on, under its 960 MiB floor for what it runs"},
		{"monitor, 1 GiB, no swap", MemoryProbe{Site: "watch", Out: memOut(gib1, "0", false)}, OK,
			"watch: 957 MiB of RAM and no swap on, at least its 896 MiB floor for what it runs"},
		{"RAM unread", MemoryProbe{Site: "vm", Out: memOut("", "0", false)}, Warn, "vm: could not read the host's RAM"},
		{"data and apps, zram does not count", MemoryProbe{Site: "home-a", Out: memOut("3006477107", gib2, true, "zram") + "zram-on " + gib2 + "\n"}, Warn,
			"home-a: 2.8 GiB of RAM and 2.0 GiB of zram and no other swap, under its 3.0 GiB floor for what it runs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Memory(cfg, []MemoryProbe{c.probe})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			if f.Section != SectionMemory || f.Level != c.level || f.Line != c.line {
				t.Errorf("got %s %v %q", f.Section, f.Level, f.Line)
			}
			if f.Level == Warn && f.Line != "vm: could not read the host's RAM" {
				text := strings.Join(f.More, "\n")
				for _, want := range []string{"What it runs as ", "Swap counts toward it up to a quarter", "more RAM"} {
					if !strings.Contains(text, want) {
						t.Errorf("lacks %q:\n%s", want, text)
					}
				}
			}
		})
	}
	// A probe doctor could not read has its warning under swap alone.
	if fs := Memory(cfg, []MemoryProbe{{Site: "vm", Err: "Permission denied"}}); len(fs) != 0 {
		t.Errorf("an unread probe judged twice:\n%s", dump(fs))
	}
}

// The floor doctor judges by follows the configuration, not the roles
// alone: Garage where storage.garage.sites places it, Caddy on a monitor
// only when it serves its own hostname.
func TestMemoryFollowsPlacement(t *testing.T) {
	const gib3 = "3221225472"
	cfg := fixture(t)
	cfg.Storage.Garage.Sites = []string{"home-a"}
	// home-b is a data site outside storage.garage.sites: 2.8 GiB.
	f := Memory(cfg, []MemoryProbe{{Site: "home-b", Out: memOut("2952790016", "0", false)}})
	if len(f) != 1 || f[0].Level != OK || f[0].Line != "home-b: 2.8 GiB of RAM and no swap on, at least its 2.8 GiB floor for what it runs" {
		t.Errorf("data site outside garage sites:\n%s", dump(f))
	}
	// An apps site storage.garage.sites names is counted with Garage.
	cfg = fixture(t)
	cfg.Sites["apps-only"] = config.Site{Roles: []config.Role{config.RoleApps}}
	cfg.Storage.Garage.Sites = append(cfg.Storage.Garage.Sites, "apps-only")
	f = Memory(cfg, []MemoryProbe{{Site: "apps-only", Out: memOut(gib1, "0", false)}})
	if len(f) != 1 || f[0].Level != Warn || !strings.Contains(f[0].Line, "under its 1.8 GiB floor for what it runs") || !strings.Contains(strings.Join(f[0].More, "\n"), "256 MiB for Garage") {
		t.Errorf("apps site in garage sites:\n%s", dump(f))
	}
	// A monitor behind its owner's web server runs no Caddy: 768 MiB.
	cfg = fixture(t)
	watch := cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal}
	cfg.Sites["watch"] = watch
	// It hosts the uptime app alone (web-server-external-serves-one-app).
	delete(cfg.Apps, "stats")
	delete(cfg.Apps, "notify") // it serves the hub
	f = Memory(cfg, []MemoryProbe{{Site: "watch", Out: memOut(gib3, "0", false)}})
	if len(f) != 1 || !strings.Contains(f[0].Line, "at least its 768 MiB floor for what it runs") {
		t.Errorf("monitor behind its owner's web server:\n%s", dump(f))
	}
	// A gateway with no gated app runs no oauth2-proxy: 896 MiB.
	cfg = fixture(t)
	delete(cfg.Apps, config.GateApp)
	f = Memory(cfg, []MemoryProbe{{Site: "vm", Out: memOut(gib1, "0", false)}})
	if len(f) != 1 || f[0].Level != OK || !strings.Contains(f[0].Line, "at least its 896 MiB floor for what it runs") {
		t.Errorf("gateway without the gate:\n%s", dump(f))
	}
}

// In the graph, each admitted site's memory is read once and judged twice,
// swap then memory; a read doctor could not make is a swap warning and
// keeps the memory check from running.
func TestMemoryInTheGraph(t *testing.T) {
	cfg := fixture(t)
	fix := healthyInput()
	fix.Memory = []MemoryProbe{
		{Site: "home-a", Err: "Permission denied"},
		{Site: "vm", Out: memOut(mib458, "0", false)},
	}
	reads := &fixtureReads{in: fix, log: &events{}}
	rec := &ui.Recorder{}
	report, _ := Checks(cfg, fix.Sites, reads).Run(rec, 0)

	find(t, report.Findings, SectionSwap, "home-a: could not read the host's memory and swap")
	if !rec.Has("explain", "Not checked because of this: memory on home-a.") {
		t.Errorf("swap home-a does not say it kept memory from running:\n%s", rec.Lines())
	}
	if f := find(t, report.Findings, SectionSwap, "vm: none on this host"); f.Level != Warn {
		t.Errorf("vm swap: %v", f.Level)
	}
	if f := find(t, report.Findings, SectionMemory, "vm: 458 MiB of RAM and no swap on"); f.Level != Warn {
		t.Errorf("vm memory: %v", f.Level)
	}
	if f := find(t, report.Findings, SectionMemory, "home-b: "); f.Level != OK {
		t.Errorf("home-b memory: %v %q", f.Level, f.Line)
	}
	n := 0
	for _, read := range reads.log.list {
		if strings.HasPrefix(read, "memory ") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("memory read %d times, want once per site: %v", n, reads.log.list)
	}
}

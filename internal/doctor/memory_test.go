package doctor

import (
	"strings"
	"testing"

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
// witness (960 MiB), watch a monitor (896 MiB). Swap that is on counts up
// to a quarter of the floor.
func TestMemory(t *testing.T) {
	cfg := fixture(t)
	for _, c := range []struct {
		name  string
		probe MemoryProbe
		level Level
		line  string
	}{
		{"data and apps, 4 GB", MemoryProbe{Site: "home-a", Out: memOut(gib4, gib2, true, "file")}, OK,
			"home-a: 3.8 GiB of RAM and 2.0 GiB of swap, at least the 3.0 GiB its roles need"},
		{"data and apps, 2 GiB and swap", MemoryProbe{Site: "home-b", Out: memOut("2147483648", "4294967296", true, "file")}, Warn,
			"home-b: 2.0 GiB of RAM and 4.0 GiB of swap, under the 3.0 GiB its roles need"},
		{"gateway and witness, 458 MiB, no swap", MemoryProbe{Site: "vm", Out: memOut(mib458, "0", false)}, Warn,
			"vm: 458 MiB of RAM and no swap on, under the 960 MiB its roles need"},
		{"gateway and witness, 1 GiB, swap on", MemoryProbe{Site: "vm", Out: memOut(gib1, gib2, true, "file")}, OK,
			"vm: 957 MiB of RAM and 2.0 GiB of swap, at least the 960 MiB its roles need"},
		{"gateway and witness, 1 GiB, swap not on", MemoryProbe{Site: "vm", Out: memOut(gib1, "0", false, "file")}, Warn,
			"vm: 957 MiB of RAM and no swap on, under the 960 MiB its roles need"},
		{"monitor, 1 GiB, no swap", MemoryProbe{Site: "watch", Out: memOut(gib1, "0", false)}, OK,
			"watch: 957 MiB of RAM and no swap on, at least the 896 MiB its roles need"},
		{"RAM unread", MemoryProbe{Site: "vm", Out: memOut("", "0", false)}, Warn, "vm: could not read the host's RAM"},
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
				for _, want := range []string{"Its roles (", "Swap counts toward it up to a quarter", "more RAM"} {
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

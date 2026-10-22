package hostmem

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Every line Probe prints is read, RAM and swap the probe printed nothing
// for read as 0, and a number that is not one refused.
func TestParse(t *testing.T) {
	f, err := Parse("mem-total 4113297408\nswap-total 2147479552\nswap-active yes\nswap-partition no\nswap-fstab yes\nswap-zram no\nswapfile present\nother x\n")
	if err != nil {
		t.Fatal(err)
	}
	want := Facts{MemTotal: 4113297408, SwapTotal: 2147479552, Active: true, FSTab: true, File: true}
	if f != want {
		t.Errorf("got %+v, want %+v", f, want)
	}
	if f, err := Parse("mem-total \nswap-total \n"); err != nil || f.MemTotal != 0 || f.SwapTotal != 0 {
		t.Errorf("empty values: %+v, %v", f, err)
	}
	for _, bad := range []string{"mem-total 4G\n", "mem-total -1\n", "swap-total 1.5e9\n"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// Swap that is on is never listed as configured; swap that is not on is
// named in every form the host has it.
func TestConfigured(t *testing.T) {
	if got := (Facts{Active: true, FSTab: true}).Configured(); got != nil {
		t.Errorf("on: %q", got)
	}
	got := Facts{Partition: true, FSTab: true, ZRAM: true, File: true}.Configured()
	want := []string{"a swap partition", "a swap line in /etc/fstab", "a zram-generator configuration", "/swapfile exists"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	if got := (Facts{}).Configured(); got != nil {
		t.Errorf("none: %q", got)
	}
}

// Probe runs as any user under /bin/sh and prints every key Parse reads.
func TestCommandRuns(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	out, err := exec.Command("sh", "-c", Command).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, key := range []string{"mem-total", "swap-total", "swap-active", "swap-partition", "swap-fstab", "swap-zram", "swapfile"} {
		if !strings.Contains(string(out), "\n"+key+" ") && !strings.HasPrefix(string(out), key+" ") {
			t.Errorf("no %s line:\n%s", key, out)
		}
	}
	if _, err := Parse(string(out)); err != nil {
		t.Errorf("%v:\n%s", err, out)
	}
}

func roles(r ...config.Role) []config.Role { return r }

// A host's floor is the base and each part its roles run, a part shared by
// two roles counted once.
func TestFloor(t *testing.T) {
	for _, c := range []struct {
		name  string
		roles []config.Role
		want  int64
	}{
		{"witness", roles(config.RoleWitness), FloorBase + FloorEtcd},
		{"gateway", roles(config.RoleGateway), FloorBase + FloorCaddy + FloorGate},
		{"monitor", roles(config.RoleMonitor), FloorBase + FloorCaddy + FloorUptime},
		{"storage", roles(config.RoleStorage), FloorBase + FloorGarage},
		{"apps", roles(config.RoleApps), FloorBase + FloorApps},
		{"data", roles(config.RoleData), FloorBase + FloorEtcd + FloorPostgres + FloorGarage},
		{"data and apps", roles(config.RoleData, config.RoleApps), FloorBase + FloorEtcd + FloorPostgres + FloorGarage + FloorApps},
		{"gateway and witness", roles(config.RoleGateway, config.RoleWitness), FloorBase + FloorCaddy + FloorGate + FloorEtcd},
		{"gateway, witness and monitor share one Caddy", roles(config.RoleGateway, config.RoleWitness, config.RoleMonitor), FloorBase + FloorCaddy + FloorGate + FloorEtcd + FloorUptime},
		{"data and storage share one Garage", roles(config.RoleData, config.RoleStorage), FloorBase + FloorEtcd + FloorPostgres + FloorGarage},
	} {
		if got := Floor(c.roles); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, Size(got), Size(c.want))
		}
	}
}

// Swap counts toward the floor up to a quarter of it, and only swap that
// is on.
func TestJudge(t *testing.T) {
	witness := roles(config.RoleWitness) // 768 MiB
	edge := roles(config.RoleGateway, config.RoleWitness, config.RoleMonitor)
	both := roles(config.RoleData, config.RoleApps)
	for _, c := range []struct {
		name  string
		facts Facts
		roles []config.Role
		short bool
	}{
		{"witness, enough RAM", Facts{MemTotal: 1 << 30}, witness, false},
		{"witness, exactly the floor", Facts{MemTotal: FloorBase + FloorEtcd}, witness, false},
		{"witness, short, no swap", Facts{MemTotal: 512 << 20}, witness, true},
		{"witness, short, swap makes it up", Facts{MemTotal: 600 << 20, SwapTotal: 1 << 30, Active: true}, witness, false},
		{"witness, short, swap counts a quarter at most", Facts{MemTotal: 512 << 20, SwapTotal: 4 << 30, Active: true}, witness, true},
		{"edge, the host that stalled etcd", Facts{MemTotal: 458 << 20}, edge, true},
		{"edge, a 1 GiB VPS with no swap", Facts{MemTotal: 957 << 20}, edge, true},
		{"edge, a 1 GiB VPS with 2 GiB of swap", Facts{MemTotal: 957 << 20, SwapTotal: 2 << 30, Active: true}, edge, false},
		{"data and apps, 4 GB", Facts{MemTotal: 4113297408}, both, false},
		{"data and apps, 2 GB with swap", Facts{MemTotal: 2 << 30, SwapTotal: 4 << 30, Active: true}, both, true},
	} {
		v := Judge(c.facts, c.roles)
		if v.Short() != c.short {
			t.Errorf("%s: short %v, want %v (counted %s of %s)", c.name, v.Short(), c.short, Size(v.Counted), Size(v.Floor))
		}
	}
}

// The verdict's line names RAM, swap and the floor, and its explanation
// every part and the swap rule.
func TestVerdictWords(t *testing.T) {
	v := Judge(Facts{MemTotal: 458 << 20}, roles(config.RoleGateway, config.RoleWitness, config.RoleMonitor))
	if got, want := v.Line(), "458 MiB of RAM and no swap on, under the 1.2 GiB its roles need"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	text := strings.Join(v.Explain(), "\n")
	for _, want := range []string{"gateway, witness, monitor", "512 MiB for the host", "256 MiB for etcd", "128 MiB for Caddy", "Swap counts toward it up to a quarter"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	v = Judge(Facts{MemTotal: 4113297408, SwapTotal: 2 << 30, Active: true}, roles(config.RoleData, config.RoleApps))
	if got, want := v.Line(), "3.8 GiB of RAM and 2.0 GiB of swap, at least the 3.0 GiB its roles need"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{458 << 20: "458 MiB", 1 << 30: "1.0 GiB", 1216 << 20: "1.2 GiB", 0: "0 MiB"} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

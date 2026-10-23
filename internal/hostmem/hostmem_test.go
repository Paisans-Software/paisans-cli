package hostmem

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Every line Probe prints is read, RAM and swap the probe printed nothing
// for read as 0, and a number that is not one refused.
func TestParse(t *testing.T) {
	f, err := Parse("mem-total 4113297408\nswap-total 2147479552\nzram-on 1073741824\nswap-active yes\nswap-partition no\nswap-fstab yes\nswap-zram no\nswapfile present\nother x\n")
	if err != nil {
		t.Fatal(err)
	}
	want := Facts{MemTotal: 4113297408, SwapTotal: 2147479552, ZRAMOn: 1 << 30, Active: true, FSTab: true, File: true}
	if f != want {
		t.Errorf("got %+v, want %+v", f, want)
	}
	if f, err := Parse("mem-total \nswap-total \nswap-active no\n"); err != nil || f.MemTotal != 0 || f.SwapTotal != 0 {
		t.Errorf("empty values: %+v, %v", f, err)
	}
	// An empty or cut short answer is refused, never read as no swap.
	for _, bad := range []string{"", "\n", "mem-total 4113297408\n", "swap-active no\n", "mem-total 4G\nswap-active no\n", "mem-total -1\nswap-active no\n", "mem-total 1\nswap-total 1.5e9\nswap-active no\n"} {
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
	for _, key := range []string{"mem-total", "swap-total", "zram-on", "swap-active", "swap-partition", "swap-fstab", "swap-zram", "swapfile"} {
		if !strings.Contains(string(out), "\n"+key+" ") && !strings.HasPrefix(string(out), key+" ") {
			t.Errorf("no %s line:\n%s", key, out)
		}
	}
	if _, err := Parse(string(out)); err != nil {
		t.Errorf("%v:\n%s", err, out)
	}
}

func roles(r ...config.Role) []config.Role { return r }

// byRoles is the floor site admit judges a host with roles by: Caddy on
// the gateway and on a monitor that serves its own hostname.
func byRoles(r ...config.Role) Runs {
	return ForRoles(r, slices.Contains(r, config.RoleGateway) || slices.Contains(r, config.RoleMonitor))
}

// A host's floor by its roles is the base and each part its roles run, a
// part shared by two roles counted once.
func TestFloorForRoles(t *testing.T) {
	for _, c := range []struct {
		name string
		runs Runs
		want int64
	}{
		{"witness", byRoles(config.RoleWitness), FloorBase + FloorEtcd},
		{"gateway", byRoles(config.RoleGateway), FloorBase + FloorCaddy + FloorGate},
		{"monitor", byRoles(config.RoleMonitor), FloorBase + FloorCaddy + FloorUptime},
		{"monitor behind its owner's web server", ForRoles(roles(config.RoleMonitor), false), FloorBase + FloorUptime},
		{"storage", byRoles(config.RoleStorage), FloorBase + FloorGarage},
		{"apps", byRoles(config.RoleApps), FloorBase + FloorApps},
		{"data", byRoles(config.RoleData), FloorBase + FloorEtcd + FloorPostgres + FloorGarage},
		{"data and apps", byRoles(config.RoleData, config.RoleApps), FloorBase + FloorEtcd + FloorPostgres + FloorGarage + FloorApps},
		{"gateway and witness", byRoles(config.RoleGateway, config.RoleWitness), FloorBase + FloorCaddy + FloorGate + FloorEtcd},
		{"gateway, witness and monitor share one Caddy", byRoles(config.RoleGateway, config.RoleWitness, config.RoleMonitor), FloorBase + FloorCaddy + FloorGate + FloorEtcd + FloorUptime},
		{"data and storage share one Garage", byRoles(config.RoleData, config.RoleStorage), FloorBase + FloorEtcd + FloorPostgres + FloorGarage},
	} {
		if got := c.runs.Floor(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, Size(got), Size(c.want))
		}
	}
}

func fixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A site's floor in a configuration follows where each part is placed:
// Garage by storage.garage.sites, whatever the site's roles, the gate
// where it is pinned, and Caddy on a monitor only when it serves its own
// hostname.
func TestForSite(t *testing.T) {
	cfg := fixtureConfig(t)
	if got, want := ForSite(cfg, "home-a"), (Runs{Etcd: true, Postgres: true, Garage: true, Apps: true}); got != want {
		t.Errorf("home-a: %+v, want %+v", got, want)
	}
	if got, want := ForSite(cfg, "vm"), (Runs{Etcd: true, Caddy: true, Gate: true}); got != want {
		t.Errorf("vm: %+v, want %+v", got, want)
	}
	if got, want := ForSite(cfg, "watch"), (Runs{Caddy: true, Uptime: true}); got != want {
		t.Errorf("watch: %+v, want %+v", got, want)
	}

	// A data site storage.garage.sites leaves out runs no Garage, and an
	// apps site it names runs one.
	cfg = fixtureConfig(t)
	cfg.Storage.Garage.Sites = []string{"home-a"}
	if ForSite(cfg, "home-b").Garage {
		t.Error("home-b, outside storage.garage.sites, is counted with Garage")
	}
	cfg.Sites["home-b"] = config.Site{Roles: roles(config.RoleApps)}
	cfg.Storage.Garage.Sites = []string{"home-a", "home-b"}
	if r := ForSite(cfg, "home-b"); !r.Garage || r.Floor() != FloorBase+FloorEtcd+FloorPostgres+FloorGarage+FloorApps {
		t.Errorf("apps site in storage.garage.sites: %+v, %s", r, Size(r.Floor()))
	}

	// A monitor behind its owner's web server runs no Caddy.
	cfg = fixtureConfig(t)
	watch := cfg.Sites["watch"]
	watch.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal}
	cfg.Sites["watch"] = watch
	if ForSite(cfg, "watch").Caddy {
		t.Error("a monitor behind its owner's web server is counted with Caddy")
	}

	// With no gated app there is no gate, and no oauth2-proxy on the gateway.
	cfg = fixtureConfig(t)
	delete(cfg.Apps, config.GateApp)
	if ForSite(cfg, "vm").Gate {
		t.Error("vm is counted with oauth2-proxy while no gate app runs")
	}
}

// Swap counts toward the floor up to a quarter of it, and only swap that
// is on and not zram.
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
		{"witness, short, zram does not count", Facts{MemTotal: 600 << 20, SwapTotal: 1 << 30, ZRAMOn: 1 << 30, Active: true}, witness, true},
		{"witness, short, the swap beside zram counts", Facts{MemTotal: 600 << 20, SwapTotal: 2 << 30, ZRAMOn: 1 << 30, Active: true}, witness, false},
		{"edge, the host that stalled etcd", Facts{MemTotal: 458 << 20}, edge, true},
		{"edge, a 1 GiB VPS with no swap", Facts{MemTotal: 957 << 20}, edge, true},
		{"edge, a 1 GiB VPS with 2 GiB of swap", Facts{MemTotal: 957 << 20, SwapTotal: 2 << 30, Active: true}, edge, false},
		{"data and apps, 4 GB", Facts{MemTotal: 4113297408}, both, false},
		{"data and apps, 2 GB with swap", Facts{MemTotal: 2 << 30, SwapTotal: 4 << 30, Active: true}, both, true},
	} {
		v := Judge(c.facts, c.roles, byRoles(c.roles...))
		if v.Short() != c.short {
			t.Errorf("%s: short %v, want %v (counted %s of %s)", c.name, v.Short(), c.short, Size(v.Counted), Size(v.Floor))
		}
	}
}

// The verdict's line names RAM, swap and the floor, and its explanation
// every part, the swap rule, and why in the words of what the host runs.
func TestVerdictWords(t *testing.T) {
	edge := roles(config.RoleGateway, config.RoleWitness, config.RoleMonitor)
	v := Judge(Facts{MemTotal: 458 << 20}, edge, byRoles(edge...))
	if got, want := v.Line(), "458 MiB of RAM and no swap on, under the 1.2 GiB its roles need"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	text := strings.Join(v.Explain(), "\n")
	for _, want := range []string{"gateway, witness, monitor", "512 MiB for the host", "256 MiB for etcd", "128 MiB for Caddy", "Swap counts toward it up to a quarter", "etcd slows to the speed of the disk when what it uses", "costs the cluster a vote"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "PostgreSQL") {
		t.Errorf("names PostgreSQL, which the host does not run:\n%s", text)
	}
	both := roles(config.RoleData, config.RoleApps)
	v = Judge(Facts{MemTotal: 4113297408, SwapTotal: 2 << 30, Active: true}, both, byRoles(both...))
	if got, want := v.Line(), "3.8 GiB of RAM and 2.0 GiB of swap, at least the 3.0 GiB its roles need"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if text := strings.Join(v.Explain(), "\n"); !strings.Contains(text, "etcd and PostgreSQL slow") {
		t.Errorf("data site's explanation:\n%s", text)
	}
	monitor := roles(config.RoleMonitor)
	v = Judge(Facts{MemTotal: 512 << 20, SwapTotal: 1 << 30, ZRAMOn: 1 << 30, Active: true}, monitor, byRoles(monitor...))
	if got, want := v.Line(), "512 MiB of RAM and 1.0 GiB of zram and no other swap, under the 896 MiB its roles need"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	text = strings.Join(v.Explain(), "\n")
	for _, bad := range []string{"etcd", "PostgreSQL"} {
		if strings.Contains(text, bad) {
			t.Errorf("a monitor's explanation names %s:\n%s", bad, text)
		}
	}
	for _, want := range []string{"a process whose working memory is swapped out", "zram swap does not count"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{458 << 20: "458 MiB", 1 << 30: "1.0 GiB", 1216 << 20: "1.2 GiB", 0: "0 MiB"} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

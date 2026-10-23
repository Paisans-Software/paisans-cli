// Package hostmem reads a host's RAM and swap, and holds the least memory
// each site role needs. `site admit` reads it in its probe, as root, and
// `doctor` on its own, as the deployment's user: nothing it reads needs
// root, so both read the same facts the same way and judge them by the same
// rules.
package hostmem

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Swapfile is where `site admit --swapfile` makes a host's swapfile, and
// what the probe looks for.
const Swapfile = "/swapfile"

// SwapNeeded is why a host with no swap should have some.
const SwapNeeded = "Without swap, a host that runs short of memory has the kernel kill a process, which may be the database, instead of slowing down."

// ZRAMNotEnough is why zram swap does not count as swap.
const ZRAMNotEnough = "zram swap compresses what it holds into RAM, so it stretches the host's memory rather than adding to it."

// zramName is a zram device's name under /dev, as lsblk prints it; Probe's
// awk and ZRAMDevice both match it, so prepare, admit and doctor tell zram
// apart the same way.
const zramName = `zram[0-9]+`

// ZRAMDevice matches the name /proc/swaps gives a zram device.
var ZRAMDevice = regexp.MustCompile(`^/dev/` + zramName + `$`)

// Probe is shell that prints one "key value" line per fact through a
// function say, which the script it is part of defines: the host's RAM and
// the swap it has on, in bytes, from /proc/meminfo, then swap in any form,
// on or not. Every file it reads is readable by any user.
const Probe = `say mem-total "$(awk '$1 == "MemTotal:" { printf "%.0f", $2 * 1024 }' /proc/meminfo)"
say swap-total "$(awk '$1 == "SwapTotal:" { printf "%.0f", $2 * 1024 }' /proc/meminfo)"
say zram-on "$(awk '` + ZRAMOnAwk + `' /proc/swaps 2>/dev/null)"
if [ "$(awk 'NR > 1' /proc/swaps 2>/dev/null | wc -l)" -gt 0 ]; then say swap-active yes; else say swap-active no; fi
if command -v lsblk >/dev/null 2>&1 && lsblk -nro NAME,FSTYPE 2>/dev/null | awk '` + PartitionAwk + `' | grep -q .; then say swap-partition yes; else say swap-partition no; fi
if awk '$1 !~ /^#/ && $3 == "swap"' /etc/fstab 2>/dev/null | grep -q .; then say swap-fstab yes; else say swap-fstab no; fi
z=no
for f in /etc/systemd/zram-generator.conf /usr/lib/systemd/zram-generator.conf /run/systemd/zram-generator.conf \
  /etc/systemd/zram-generator.conf.d/*.conf /usr/lib/systemd/zram-generator.conf.d/*.conf /run/systemd/zram-generator.conf.d/*.conf; do
  [ -e "$f" ] && z=yes
done
say swap-zram "$z"
if [ -e ` + Swapfile + ` ] || [ -L ` + Swapfile + ` ]; then say swapfile present; else say swapfile absent; fi
`

// ZRAMOnAwk sums the sizes /proc/swaps lists for zram devices, in bytes:
// after its header, one line per swap area, its name first and its size in
// KiB third. A name with a space in it is written with \040, so it stays
// one field.
const ZRAMOnAwk = `NR > 1 && $1 ~ /^\/dev\/` + zramName + `$/ { s += $3 } END { printf "%.0f", s * 1024 }`

// PartitionAwk prints the block devices formatted as swap that are not
// zram devices, from lsblk -nro NAME,FSTYPE: a zram device is formatted
// as swap too, and is not a partition.
const PartitionAwk = `$2 == "swap" && $1 !~ /^` + zramName + `$/`

// Command is Probe on its own, for a caller with no probe script to put it
// in.
const Command = "say() { printf '%s %s\\n' \"$1\" \"$2\"; }\n" + Probe

// Facts are what Probe found. MemTotal is the host's RAM, SwapTotal the
// swap it has on and ZRAMOn the part of that on a zram device, in bytes,
// each 0 when unread. Inside a container, MemTotal is the machine's RAM,
// not the container's limit, unless something like lxcfs presents the
// limit as /proc/meminfo. Active is any entry in
// /proc/swaps; Partition a block device formatted as swap, FSTab a swap
// line in /etc/fstab, ZRAM a zram-generator configuration and File
// something at /swapfile, each whether it is on or not.
type Facts struct {
	MemTotal, SwapTotal, ZRAMOn          int64
	Active, Partition, FSTab, ZRAM, File bool
}

// Read takes one of Probe's lines into f, reporting whether key is one of
// Probe's. A number it printed nothing for is 0, unread; one that is not a
// number is refused, so RAM never reads as none.
func (f *Facts) Read(key, value string) (bool, error) {
	switch key {
	case "mem-total", "swap-total", "zram-on":
		var n int64
		if value != "" {
			var err error
			if n, err = strconv.ParseInt(value, 10, 64); err != nil || n < 0 {
				return true, fmt.Errorf("unreadable %s line %q", key, value)
			}
		}
		switch key {
		case "mem-total":
			f.MemTotal = n
		case "swap-total":
			f.SwapTotal = n
		default:
			f.ZRAMOn = n
		}
	case "swap-active":
		f.Active = value == "yes"
	case "swap-partition":
		f.Partition = value == "yes"
	case "swap-fstab":
		f.FSTab = value == "yes"
	case "swap-zram":
		f.ZRAM = value == "yes"
	case "swapfile":
		f.File = value == "present"
	default:
		return false, nil
	}
	return true, nil
}

// Parse reads Command's output. A line it does not know is left out, and
// an answer without the mem-total and swap-active lines is refused, so an
// empty or cut short answer never reads as a host with no swap.
func Parse(out string) (Facts, error) {
	var f Facts
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimRight(line, "\r"), " ")
		if _, err := f.Read(key, strings.TrimSpace(value)); err != nil {
			return f, err
		}
		seen[key] = true
	}
	for _, key := range []string{"mem-total", "swap-active"} {
		if !seen[key] {
			return f, fmt.Errorf("the answer has no %s line", key)
		}
	}
	return f, nil
}

// CountsOn reports whether swap that counts toward memory is on: any swap
// on beyond zram's. Swap on whose size was not read counts.
func (f Facts) CountsOn() bool {
	return f.Active && (f.SwapTotal == 0 || f.SwapTotal > f.ZRAMOn)
}

// Configured is the swap the host has that would count toward memory and
// has not turned on, each form named; nil when such swap is on, or when
// the host has none. zram is left out, configured or on: it keeps what it
// holds in RAM, so it is no reason to hold back a swapfile, and turning it
// on adds nothing to the host's memory.
func (f Facts) Configured() []string {
	if f.CountsOn() {
		return nil
	}
	var out []string
	for _, c := range []struct {
		has  bool
		what string
	}{{f.Partition, "a swap partition"}, {f.FSTab, "a swap line in /etc/fstab"}, {f.File, Swapfile + " exists"}} {
		if c.has {
			out = append(out, c.what)
		}
	}
	return out
}

// HasZRAM reports whether the host has zram swap, on or configured.
func (f Facts) HasZRAM() bool { return f.ZRAM || f.ZRAMOn > 0 }

const mib = 1 << 20

// The least memory each part of a site needs, compared with the host's
// MemTotal, which the kernel reports a little below the machine's RAM.
// Each is what the part holds at rest with room to work, not what it can
// grow to under load: a host at its floor runs, and one below it is where
// the kernel starts killing processes. They are set from what each role
// runs (the rendered compose files), not measured on hosts.
const (
	// FloorBase is every host: Ubuntu Server's own services, an apt or
	// unattended-upgrades run among them, the deployment user's rootless
	// Docker (dockerd, containerd and RootlessKit), WireGuard and the
	// heartbeat container.
	FloorBase = 512 * mib
	// FloorEtcd is an etcd v3.5 member, on every data site and the witness:
	// its keyspace is Patroni's few keys, and the rest is room for its
	// snapshots and compactions, since a member that stalls answers
	// Patroni's reads late and costs the cluster a vote.
	FloorEtcd = 256 * mib
	// FloorPostgres is a data site's database: Spilo's PostgreSQL with its
	// shared buffers and a few connections per app, Patroni's Python
	// process, and HAProxy in front of them.
	FloorPostgres = 1024 * mib
	// FloorGarage is a Garage node, on a data or storage site: its
	// metadata database and the buffers of the uploads it is handling.
	FloorGarage = 256 * mib
	// FloorCaddy is the toolkit's Caddy, on the gateway and on a monitor
	// that serves its own hostname, with its certificates and connections.
	FloorCaddy = 128 * mib
	// FloorGate is oauth2-proxy, the visibility gate, which runs on the
	// gateway.
	FloorGate = 64 * mib
	// FloorUptime is the uptime monitor, a Node.js process.
	FloorUptime = 256 * mib
	// FloorApps is an apps site's least: Pocket ID and one app beside it.
	// An app such as Mbin, with its PHP workers and Valkey, or Wiki.js
	// needs more of its own, so a site running several needs more than
	// this floor.
	FloorApps = 1024 * mib
)

// Part is one thing a host runs, and the least memory it needs.
type Part struct {
	Name  string
	Bytes int64
}

// Runs is what a host runs, as its floor counts it. Each part is counted
// once however many roles run it: a witness on a data site runs one etcd,
// and a monitor beside the gateway is served by the gateway's Caddy.
type Runs struct {
	Etcd, Postgres, Garage, Caddy, Gate, Uptime, Apps bool
	// ByRole is set by ForRoles: the parts are what the roles usually
	// run, not what a configuration places, and the words say so.
	ByRole bool
}

// ForRoles is what a host with roles runs, as far as its roles say, which
// is all site admit knows: a data site is counted with Garage, which
// storage.garage.sites usually places there, and a gateway with
// oauth2-proxy, which runs there when an app is gated. RunsCaddy is
// whether the site runs the toolkit's Caddy: the gateway, and a monitor
// that serves its own hostname rather than sitting behind its owner's web
// server.
func ForRoles(roles []config.Role, runsCaddy bool) Runs {
	has := func(r config.Role) bool { return slices.Contains(roles, r) }
	return Runs{
		Etcd:     has(config.RoleData) || has(config.RoleWitness),
		Postgres: has(config.RoleData),
		Garage:   has(config.RoleData) || has(config.RoleStorage),
		Caddy:    runsCaddy,
		Gate:     has(config.RoleGateway),
		Uptime:   has(config.RoleMonitor),
		Apps:     has(config.RoleApps),
		ByRole:   true,
	}
}

// ForSite is what site runs in cfg: etcd on the members of etcd.members,
// the database on cluster.sites, Garage on storage.garage.sites, Caddy
// where Site.RunsCaddy says, oauth2-proxy where the gate app is pinned,
// the uptime monitor on a monitor and the apps on an apps site.
func ForSite(cfg *config.Config, site string) Runs {
	s := cfg.Sites[site]
	gate, gated := cfg.Apps[config.GateApp]
	return Runs{
		Etcd:     slices.Contains(cfg.Etcd.Members, site),
		Postgres: slices.Contains(cfg.Cluster.Sites, site),
		Garage:   slices.Contains(cfg.Storage.Garage.Sites, site),
		Caddy:    s.RunsCaddy(),
		Gate:     gated && gate.Blueprint == config.BlueprintOAuth2Proxy && gate.Placement.Site == site,
		Uptime:   s.Has(config.RoleMonitor),
		Apps:     s.Has(config.RoleApps),
	}
}

// Parts is everything r counts, the host itself first.
func (r Runs) Parts() []Part {
	out := []Part{{"the host and its Docker", FloorBase}}
	for _, c := range []struct {
		runs bool
		part Part
	}{
		{r.Etcd, Part{"etcd", FloorEtcd}},
		{r.Postgres, Part{"PostgreSQL, Patroni and HAProxy", FloorPostgres}},
		{r.Garage, Part{"Garage", FloorGarage}},
		{r.Caddy, Part{"Caddy", FloorCaddy}},
		{r.Gate, Part{"oauth2-proxy", FloorGate}},
		{r.Uptime, Part{"the uptime monitor", FloorUptime}},
		{r.Apps, Part{"Pocket ID and an app", FloorApps}},
	} {
		if c.runs {
			out = append(out, c.part)
		}
	}
	return out
}

// Floor is the least memory a host running r needs: the sum of its Parts.
func (r Runs) Floor() int64 {
	var n int64
	for _, p := range r.Parts() {
		n += p.Bytes
	}
	return n
}

// SwapShare is the most of a floor swap may make up: a quarter. Swap holds
// what a host keeps and rarely touches, which is what lets it ride out an
// apt run, but a process whose working memory is swapped out slows to the
// speed of the disk, etcd and PostgreSQL above all, so the rest must be
// RAM.
const SwapShare = 4

// Verdict is a host's memory against its floor.
type Verdict struct {
	Roles []config.Role
	Runs  Runs
	Parts []Part
	Floor int64
	// RAM is MemTotal; Swap the swap on that is not zram, which counts
	// toward the floor; ZRAM the zram swap on, which does not, since it
	// keeps what it holds in RAM.
	RAM, Swap, ZRAM int64
	// Counted is RAM and the swap that counts toward the floor.
	Counted int64
}

// Judge is f against the floor of what a host with roles runs. Only swap
// that is on counts, and not zram's, and it counts up to a quarter of the
// floor.
func Judge(f Facts, roles []config.Role, runs Runs) Verdict {
	v := Verdict{Roles: roles, Runs: runs, Parts: runs.Parts(), Floor: runs.Floor(), RAM: f.MemTotal}
	if f.Active {
		v.ZRAM = min(f.ZRAMOn, f.SwapTotal)
		v.Swap = f.SwapTotal - v.ZRAM
	}
	v.Counted = v.RAM + min(v.Swap, v.Floor/SwapShare)
	return v
}

// Short reports whether the host has less than its floor.
func (v Verdict) Short() bool { return v.Counted < v.Floor }

// Line is the verdict in a line.
func (v Verdict) Line() string {
	swap := "no swap on"
	switch {
	case v.Swap > 0 && v.ZRAM > 0:
		swap = fmt.Sprintf("%s of swap and %s of zram", Size(v.Swap), Size(v.ZRAM))
	case v.Swap > 0:
		swap = Size(v.Swap) + " of swap"
	case v.ZRAM > 0:
		swap = Size(v.ZRAM) + " of zram and no other swap"
	}
	what := "what it runs"
	if v.Runs.ByRole {
		what = "its roles"
	}
	if v.Short() {
		return fmt.Sprintf("%s of RAM and %s, under its %s floor for %s", Size(v.RAM), swap, Size(v.Floor), what)
	}
	return fmt.Sprintf("%s of RAM and %s, at least its %s floor for %s", Size(v.RAM), swap, Size(v.Floor), what)
}

// Explain is where the floor comes from and the swap rule, in the words of
// what the host runs.
func (v Verdict) Explain() []string {
	var names, parts []string
	for _, r := range v.Roles {
		names = append(names, string(r))
	}
	for _, p := range v.Parts {
		parts = append(parts, Size(p.Bytes)+" for "+p.Name)
	}
	var slow []string
	if v.Runs.Etcd {
		slow = append(slow, "etcd")
	}
	if v.Runs.Postgres {
		slow = append(slow, "PostgreSQL")
	}
	why := "a process whose working memory is swapped out slows to the speed of the disk"
	switch len(slow) {
	case 1:
		why = slow[0] + " slows to the speed of the disk when what it uses is swapped out"
	case 2:
		why = strings.Join(slow, " and ") + " slow to the speed of the disk when what they use is swapped out"
	}
	need := fmt.Sprintf("What it runs as %s needs %s: %s.", strings.Join(names, ", "), Size(v.Floor), strings.Join(parts, ", "))
	if v.Runs.ByRole {
		need = fmt.Sprintf("Its roles (%s) need %s: %s.", strings.Join(names, ", "), Size(v.Floor), strings.Join(parts, ", "))
	}
	out := []string{
		need,
		fmt.Sprintf("Swap counts toward it up to a quarter of it, %s here: %s, so the rest must be RAM.", Size(v.Floor/SwapShare), why),
	}
	if v.ZRAM > 0 {
		out = append(out, "zram swap does not count: it keeps what it holds in RAM.")
	}
	if v.Runs.Etcd {
		return append(out, "Short of memory, the kernel kills processes, and an etcd member that stalls answers Patroni late and costs the cluster a vote.")
	}
	return append(out, "Short of memory, the kernel kills processes.")
}

// Size is n bytes for a message: MiB under a GiB, GiB to one place above.
func Size(n int64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%d MiB", n/mib)
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

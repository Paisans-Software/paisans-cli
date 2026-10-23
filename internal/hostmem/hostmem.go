// Package hostmem reads a host's RAM and swap, and holds the least memory
// each site role needs. `site admit` reads it in its probe, as root, and
// `doctor` on its own, as the deployment's user: nothing it reads needs
// root, so both read the same facts the same way and judge them by the same
// rules.
package hostmem

import (
	"fmt"
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

// Probe is shell that prints one "key value" line per fact through a
// function say, which the script it is part of defines: the host's RAM and
// the swap it has on, in bytes, from /proc/meminfo, then swap in any form,
// on or not. Every file it reads is readable by any user.
const Probe = `say mem-total "$(awk '$1 == "MemTotal:" { printf "%.0f", $2 * 1024 }' /proc/meminfo)"
say swap-total "$(awk '$1 == "SwapTotal:" { printf "%.0f", $2 * 1024 }' /proc/meminfo)"
if [ "$(awk 'NR > 1' /proc/swaps 2>/dev/null | wc -l)" -gt 0 ]; then say swap-active yes; else say swap-active no; fi
if command -v lsblk >/dev/null 2>&1 && lsblk -nro FSTYPE 2>/dev/null | grep -qx swap; then say swap-partition yes; else say swap-partition no; fi
if awk '$1 !~ /^#/ && $3 == "swap"' /etc/fstab 2>/dev/null | grep -q .; then say swap-fstab yes; else say swap-fstab no; fi
z=no
for f in /etc/systemd/zram-generator.conf /usr/lib/systemd/zram-generator.conf /run/systemd/zram-generator.conf \
  /etc/systemd/zram-generator.conf.d/*.conf /usr/lib/systemd/zram-generator.conf.d/*.conf /run/systemd/zram-generator.conf.d/*.conf; do
  [ -e "$f" ] && z=yes
done
say swap-zram "$z"
if [ -e ` + Swapfile + ` ] || [ -L ` + Swapfile + ` ]; then say swapfile present; else say swapfile absent; fi
`

// Command is Probe on its own, for a caller with no probe script to put it
// in.
const Command = "say() { printf '%s %s\\n' \"$1\" \"$2\"; }\n" + Probe

// Facts are what Probe found. MemTotal is the host's RAM and SwapTotal the
// swap it has on, in bytes, each 0 when unread. Active is any entry in
// /proc/swaps; Partition a block device formatted as swap, FSTab a swap
// line in /etc/fstab, ZRAM a zram-generator configuration and File
// something at /swapfile, each whether it is on or not.
type Facts struct {
	MemTotal, SwapTotal                  int64
	Active, Partition, FSTab, ZRAM, File bool
}

// Read takes one of Probe's lines into f, reporting whether key is one of
// Probe's. A number it printed nothing for is 0, unread; one that is not a
// number is refused, so RAM never reads as none.
func (f *Facts) Read(key, value string) (bool, error) {
	switch key {
	case "mem-total", "swap-total":
		var n int64
		if value != "" {
			var err error
			if n, err = strconv.ParseInt(value, 10, 64); err != nil || n < 0 {
				return true, fmt.Errorf("unreadable %s line %q", key, value)
			}
		}
		if key == "mem-total" {
			f.MemTotal = n
		} else {
			f.SwapTotal = n
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

// Parse reads Command's output. A line it does not know is left out.
func Parse(out string) (Facts, error) {
	var f Facts
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimRight(line, "\r"), " ")
		if _, err := f.Read(key, strings.TrimSpace(value)); err != nil {
			return f, err
		}
	}
	return f, nil
}

// Configured is the swap the host has and has not turned on, each form
// named; nil when swap is on, or when the host has none.
func (f Facts) Configured() []string {
	if f.Active {
		return nil
	}
	var out []string
	for _, c := range []struct {
		has  bool
		what string
	}{{f.Partition, "a swap partition"}, {f.FSTab, "a swap line in /etc/fstab"}, {f.ZRAM, "a zram-generator configuration"}, {f.File, Swapfile + " exists"}} {
		if c.has {
			out = append(out, c.what)
		}
	}
	return out
}

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

var (
	partEtcd     = Part{"etcd", FloorEtcd}
	partPostgres = Part{"PostgreSQL, Patroni and HAProxy", FloorPostgres}
	partGarage   = Part{"Garage", FloorGarage}
	partCaddy    = Part{"Caddy", FloorCaddy}
	partGate     = Part{"oauth2-proxy", FloorGate}
	partUptime   = Part{"the uptime monitor", FloorUptime}
	partApps     = Part{"Pocket ID and an app", FloorApps}
)

// roleParts is what each role runs. A part two roles run is counted once
// on a host that holds both: a witness on a data site runs one etcd, and a
// monitor beside the gateway is served by the gateway's Caddy.
var roleParts = map[config.Role][]Part{
	config.RoleData:    {partEtcd, partPostgres, partGarage},
	config.RoleWitness: {partEtcd},
	config.RoleStorage: {partGarage},
	config.RoleGateway: {partCaddy, partGate},
	config.RoleMonitor: {partCaddy, partUptime},
	config.RoleApps:    {partApps},
}

// Parts is everything a host with roles runs, the host itself first, each
// part once.
func Parts(roles []config.Role) []Part {
	out := []Part{{"the host and its Docker", FloorBase}}
	for _, r := range roles {
		for _, p := range roleParts[r] {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// Floor is the least memory a host with roles needs: the sum of Parts.
func Floor(roles []config.Role) int64 {
	var n int64
	for _, p := range Parts(roles) {
		n += p.Bytes
	}
	return n
}

// SwapShare is the most of a floor swap may make up: a quarter. Swap holds
// what a host keeps and rarely touches, which is what lets it ride out an
// apt run, but etcd and PostgreSQL slow to the speed of the disk when
// what they use is swapped out, so the rest must be RAM.
const SwapShare = 4

// Verdict is a host's memory against its roles' floor.
type Verdict struct {
	Roles     []config.Role
	Parts     []Part
	Floor     int64
	RAM, Swap int64
	// Counted is RAM and the swap that counts toward the floor.
	Counted int64
}

// Judge is f against the floor of roles. Only swap that is on counts, and
// it counts up to a quarter of the floor.
func Judge(f Facts, roles []config.Role) Verdict {
	v := Verdict{Roles: roles, Parts: Parts(roles), Floor: Floor(roles), RAM: f.MemTotal}
	if f.Active {
		v.Swap = f.SwapTotal
	}
	v.Counted = v.RAM + min(v.Swap, v.Floor/SwapShare)
	return v
}

// Short reports whether the host has less than its roles need.
func (v Verdict) Short() bool { return v.Counted < v.Floor }

// Line is the verdict in a line.
func (v Verdict) Line() string {
	swap := "no swap on"
	if v.Swap > 0 {
		swap = Size(v.Swap) + " of swap"
	}
	if v.Short() {
		return fmt.Sprintf("%s of RAM and %s, under the %s its roles need", Size(v.RAM), swap, Size(v.Floor))
	}
	return fmt.Sprintf("%s of RAM and %s, at least the %s its roles need", Size(v.RAM), swap, Size(v.Floor))
}

// Explain is where the floor comes from, and the swap rule.
func (v Verdict) Explain() []string {
	var names, parts []string
	for _, r := range v.Roles {
		names = append(names, string(r))
	}
	for _, p := range v.Parts {
		parts = append(parts, Size(p.Bytes)+" for "+p.Name)
	}
	return []string{
		fmt.Sprintf("Its roles (%s) need %s: %s.", strings.Join(names, ", "), Size(v.Floor), strings.Join(parts, ", ")),
		fmt.Sprintf("Swap counts toward it up to a quarter of it, %s here: etcd and PostgreSQL slow to the speed of the disk when what they use is swapped out, so the rest must be RAM.", Size(v.Floor/SwapShare)),
		"Short of memory, the kernel kills processes, and an etcd member that stalls answers Patroni late and costs the cluster a vote.",
	}
}

// Size is n bytes for a message: MiB under a GiB, GiB to one place above.
func Size(n int64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%d MiB", n/mib)
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

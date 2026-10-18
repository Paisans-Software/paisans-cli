package hostadmit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// swapPath is where --swapfile makes the host's swapfile.
const swapPath = "/swapfile"

// swapMax is the largest swapfile admit makes, whatever the host's RAM.
const swapMax = 16 << 30

// swapSpare is the free space a swapfile must leave on its filesystem.
const swapSpare = 5e9

// swapFilesystems are the filesystems a plain swapfile works on, as stat -f
// names them: ext4 is "ext2/ext3" there. btrfs and zfs need more than
// fallocate and mkswap, and admit makes no swapfile on them.
var swapFilesystems = []string{"ext2/ext3", "xfs"}

// swapSize is the swapfile for a host with mem bytes of RAM: twice that,
// to the nearest GiB and at least one, up to swapMax.
func swapSize(mem int64) int64 {
	gib := max((2*mem+1<<29)>>30, 1)
	return min(gib<<30, swapMax)
}

// gib is n bytes in whole GiB, for messages.
func gib(n int64) string { return fmt.Sprintf("%d GiB", n>>30) }

// swapNeeded is why a host with no swap should have some.
const swapNeeded = "Without swap, a host that runs short of memory has the kernel kill a process, which may be the database, instead of slowing down."

// planSwap warns about a host with no swap, and with --swapfile makes it
// one: twice its RAM up to 16 GiB, at /swapfile, on at boot through
// /etc/fstab. Swap the host has in any form, on or not, is left alone and
// never sized: a partition, a zram configuration or a swap line in fstab
// that is not on is the owner's to turn on, and admit offers nothing
// beside it.
func planSwap(p *planner) error {
	s := p.f.Swap
	if s.Active {
		p.present("swap: on")
		return nil
	}
	var configured []string
	for _, c := range []struct {
		has  bool
		what string
	}{{s.Partition, "a swap partition"}, {s.FSTab, "a swap line in /etc/fstab"}, {s.ZRAM, "a zram-generator configuration"}, {s.File, swapPath + " exists"}} {
		if c.has {
			configured = append(configured, c.what)
		}
	}
	if len(configured) > 0 {
		p.warn("swap: configured but not on ("+strings.Join(configured, ", ")+")",
			swapNeeded+" Turn it on (swapon -a, or the zram service), or remove it and admit again with --swapfile. Admit makes no swapfile beside swap the host already has.")
		return nil
	}
	if s.MemTotal == 0 {
		p.warn("swap: none on this host", swapNeeded+" Admit could not read the host's RAM to size a swapfile, so make one yourself.")
		return nil
	}
	size := swapSize(s.MemTotal)
	if why := cannotSwap(p, size); why != "" {
		if p.in.Flags.Swapfile {
			return fmt.Errorf("--swapfile: %s. Nothing was changed", why)
		}
		p.warn("swap: none on this host", swapNeeded+" Admit cannot make a swapfile here: "+why+".")
		return nil
	}
	if !p.in.Flags.Swapfile {
		p.plan.SwapOffer = size
		p.warn("swap: none on this host; --swapfile makes a swapfile of "+gib(size), fmt.Sprintf("%s --swapfile makes it at %s, twice the host's RAM up to %s, and admit asks at a terminal with --execute.", swapNeeded, swapPath, gib(swapMax)))
		return nil
	}
	tmp := swapPath + ".paisans-tmp"
	p.step(Step{
		Title:    "create a swapfile of " + gib(size),
		Describe: fmt.Sprintf("swap: %s, %s, twice the host's RAM up to %s, on at boot through /etc/fstab", swapPath, gib(size), gib(swapMax)),
		Command: fmt.Sprintf("set -e; umask 077; rm -f %[1]s\n"+
			"fallocate -l %[2]d %[1]s || { rm -f %[1]s; dd if=/dev/zero of=%[1]s bs=1M count=%[3]d status=none; }\n"+
			"chmod 600 %[1]s; mkswap %[1]s >/dev/null; mv %[1]s %[4]s; swapon %[4]s\n"+
			"grep -qs '^%[4]s[[:space:]]' /etc/fstab || printf '%%s\\n' '%[4]s none swap sw 0 0' >> /etc/fstab",
			tmp, size, size>>20, swapPath),
		Kinds: []string{"swap"},
	})
	return nil
}

// cannotSwap is why admit cannot make a swapfile of size on the host, ""
// when it can.
func cannotSwap(p *planner, size int64) string {
	s := p.f.Swap
	if !slices.Contains(swapFilesystems, s.RootFS) {
		fs := ui.Visible(s.RootFS)
		if fs == "" {
			fs = "a filesystem the probe could not name"
		}
		return "/ is on " + fs + ", and admit makes swapfiles on ext4 and xfs alone"
	}
	if s.RootAvail < size+swapSpare {
		return fmt.Sprintf("/ has %.1f GB free, and a swapfile of %s must leave %.0f GB", float64(s.RootAvail)/1e9, gib(size), swapSpare/1e9)
	}
	return ""
}

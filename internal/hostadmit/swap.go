package hostadmit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostmem"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// swapPath is where --swapfile makes the host's swapfile.
const swapPath = hostmem.Swapfile

// SwapMax is the largest swapfile admit makes, whatever the host's RAM.
const SwapMax = 16 << 30

// swapSpare is the free space a swapfile must leave on its filesystem.
const swapSpare = 5e9

// swapFilesystems are the filesystems a plain swapfile works on, as stat -f
// names them: ext4 is "ext2/ext3" there. btrfs and zfs need more than
// fallocate and mkswap, and admit makes no swapfile on them.
var swapFilesystems = []string{"ext2/ext3", "xfs"}

// swapSize is the swapfile for a host with mem bytes of RAM: twice that,
// to the nearest GiB and at least one, up to SwapMax.
func swapSize(mem int64) int64 {
	gib := max((2*mem+1<<29)>>30, 1)
	return min(gib<<30, SwapMax)
}

// GiB is n bytes in whole GiB, for messages.
func GiB(n int64) string { return fmt.Sprintf("%d GiB", n>>30) }

// SwapNeeded is why a host with no swap should have some.
const SwapNeeded = hostmem.SwapNeeded

// planSwap warns about a host with no swap, and with --swapfile makes it
// one: twice its RAM up to 16 GiB, at /swapfile, on at boot through
// /etc/fstab. Swap the host has in any form, on or not, is left alone and
// never sized: a partition, a zram configuration or a swap line in fstab
// that is not on is the owner's to turn on, and admit offers nothing
// beside it, refusing --swapfile. It runs before Docker's packages, so the
// space it checks is read before admit's own installs take any, and a
// host short of memory has its swap while they install.
func planSwap(p *planner) error {
	s := p.f.Swap
	if s.Active {
		p.present("swap: on")
		return nil
	}
	if configured := s.Configured(); len(configured) > 0 {
		what := strings.Join(configured, ", ")
		if p.in.Flags.Swapfile {
			return fmt.Errorf("--swapfile: this host has swap that is not on (%s), and admit makes no swapfile beside it. Turn it on (swapon -a, or the zram service), or remove it, and admit again. Nothing was changed", what)
		}
		p.warn("swap: configured but not on ("+what+")",
			SwapNeeded+" Turn it on (swapon -a, or the zram service), or remove it and admit again to be offered a swapfile. Admit makes no swapfile beside swap the host already has.")
		return nil
	}
	if s.MemTotal == 0 {
		if p.in.Flags.Swapfile {
			return fmt.Errorf("--swapfile: admit could not read the host's RAM to size a swapfile. Make one yourself, and admit again without --swapfile. Nothing was changed")
		}
		p.warn("swap: none on this host", SwapNeeded+" Admit could not read the host's RAM to size a swapfile, so make one yourself.")
		return nil
	}
	size := swapSize(s.MemTotal)
	if why := cannotSwap(p, size); why != "" {
		if p.in.Flags.Swapfile {
			return fmt.Errorf("--swapfile: %s. Nothing was changed", why)
		}
		p.warn("swap: none on this host", SwapNeeded+" Admit cannot make a swapfile here: "+why+".")
		return nil
	}
	if !p.in.Flags.Swapfile {
		p.plan.SwapOffer = size
		p.warn("swap: none on this host; --swapfile makes a swapfile of "+GiB(size), fmt.Sprintf("%s --swapfile makes it at %s, twice the host's RAM up to %s, and admit asks at a terminal with --execute.", SwapNeeded, swapPath, GiB(SwapMax)))
		return nil
	}
	p.swapMade = size
	p.step(Step{
		Title:    "create a swapfile of " + GiB(size),
		Describe: fmt.Sprintf("swap: %s, %s, twice the host's RAM up to %s, on at boot through /etc/fstab", swapPath, GiB(size), GiB(SwapMax)),
		Command:  swapCommand(size),
		Kinds:    []string{"swap"},
	})
	return nil
}

// swapCommand makes a swapfile of size bytes at swapPath: a temporary file
// beside it, removed however the step exits, made a swapfile and moved
// into place only where nothing is there; then the /etc/fstab line, on a
// line of its own, before swapon, so a step that stops part way leaves
// swap that is configured, which the next admit warns about, and never
// swap that is on but gone at the next boot.
func swapCommand(size int64) string {
	tmp := swapPath + ".paisans-tmp"
	return fmt.Sprintf("set -e; umask 077\n"+
		"trap 'rm -f %[1]s' EXIT; trap 'exit 1' HUP INT TERM\n"+
		"rm -f %[1]s\n"+
		"fallocate -l %[2]d %[1]s || { rm -f %[1]s; dd if=/dev/zero of=%[1]s bs=1M count=%[3]d status=none; }\n"+
		"chmod 600 %[1]s; mkswap %[1]s >/dev/null\n"+
		"if [ -e %[4]s ] || [ -L %[4]s ]; then echo '%[4]s appeared since admit read the host; nothing was moved over it' >&2; exit 1; fi\n"+
		"mv %[1]s %[4]s\n"+
		"if ! grep -qs '^%[4]s[[:space:]]' /etc/fstab; then\n"+
		"  if [ -s /etc/fstab ] && [ -n \"$(tail -c1 /etc/fstab)\" ]; then echo >> /etc/fstab; fi\n"+
		"  printf '%%s\\n' '%[4]s none swap sw 0 0' >> /etc/fstab\n"+
		"fi\n"+
		"swapon %[4]s",
		tmp, size, size>>20, swapPath)
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
		return fmt.Sprintf("/ has %.1f GB free, and a swapfile of %s must leave %.0f GB", float64(s.RootAvail)/1e9, GiB(size), swapSpare/1e9)
	}
	return ""
}

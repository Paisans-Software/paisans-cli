package hostprep

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostmem"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// swapsPath lists the swap the kernel has on, a header line and one line
// for each, and any user may read it.
const swapsPath = "/proc/swaps"

// checkSwap warns when the host has no swap on beyond zram, which keeps
// what it holds in RAM, as admit and doctor judge it. It reads only
// /proc/swaps, so swap configured but off, zram among it, reads as none,
// where admit and doctor name it. It plans nothing: making a swapfile is
// root's, so site admit does it, with the owner's yes or --swapfile. A host
// whose transport finds no /proc/swaps says nothing.
func checkSwap(site string, t Transport) (Section, error) {
	var out Section
	content, found, err := t.ReadFile(swapsPath)
	if err != nil {
		return out, fmt.Errorf("reading %s on %s: %w", swapsPath, t.Describe(), err)
	}
	if !found {
		return out, nil
	}
	zram := false
	for _, line := range strings.Split(strings.TrimSpace(content), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if !hostmem.ZRAMDevice.MatchString(fields[0]) {
			out.Present = append(out.Present, "swap: on")
			return out, nil
		}
		zram = true
	}
	hint, why := "swap: none on "+t.Describe(), hostmem.SwapNeeded
	if zram {
		hint, why = "swap: only zram on "+t.Describe(), hostmem.SwapNeeded+" "+hostmem.ZRAMNotEnough
	}
	out.Warnings = append(out.Warnings, Warning{Hint: hint, Detail: why + " " + admitSwap(site)})
	return out, nil
}

// admitSwap is what gives site swap when site prepare finds none on: the
// host's owner turns on what it has, or admits again to be offered a
// swapfile.
func admitSwap(site string) string {
	return fmt.Sprintf("The host's owner turns on the swap it has, or, where it has none, runs sudo %[2]s site admit --request %[1]s.admit again, from %[3]s site request --site %[1]s > %[1]s.admit, which offers a swapfile where it can make one.", site, progname.Default, progname.Cmd())
}

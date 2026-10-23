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
// what it holds in RAM, as admit and doctor judge it. It plans nothing:
// making a swapfile is root's, so site admit does it, with the owner's yes
// or --swapfile. A host whose transport finds no /proc/swaps says nothing.
func checkSwap(t Transport) (Section, error) {
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
	if zram {
		out.Warnings = append(out.Warnings, Warning{Hint: "swap: only zram on " + t.Describe(),
			Detail: hostmem.SwapNeeded + " " + hostmem.ZRAMNotEnough + " The host's owner runs " + progname.Default + " site admit again, which offers a swapfile beside it where it can make one."})
		return out, nil
	}
	out.Warnings = append(out.Warnings, Warning{Hint: "swap: none on " + t.Describe(),
		Detail: hostmem.SwapNeeded + " The host's owner turns on the swap it has, or, where it has none, runs " + progname.Default + " site admit again, which offers a swapfile where it can make one."})
	return out, nil
}

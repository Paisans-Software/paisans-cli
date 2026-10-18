package hostprep

import (
	"fmt"
	"strings"
)

// swapsPath lists the swap the kernel has on, a header line and one line
// for each, and any user may read it.
const swapsPath = "/proc/swaps"

// checkSwap warns when the host has no swap on. It plans nothing: making a
// swapfile is root's, so site admit does it, with the owner's yes or
// --swapfile. A host whose transport finds no /proc/swaps says nothing.
func checkSwap(t Transport) (Section, error) {
	var out Section
	content, found, err := t.ReadFile(swapsPath)
	if err != nil {
		return out, fmt.Errorf("reading %s on %s: %w", swapsPath, t.Describe(), err)
	}
	if !found {
		return out, nil
	}
	if len(strings.Split(strings.TrimSpace(content), "\n")) > 1 {
		out.Present = append(out.Present, "swap: on")
		return out, nil
	}
	out.Warnings = append(out.Warnings, Warning{Hint: "swap: none on " + t.Describe(),
		Detail: "Without swap, a host that runs short of memory has the kernel kill a process, which may be the database, instead of slowing down. The host's owner runs paisans site admit again with --swapfile to make one, or turns on the swap the host has."})
	return out, nil
}

// Package hostarch reads a host's architecture and holds the rules the
// toolkit applies to it. amd64 and arm64 are supported because the stack's
// images are published for those two (docs/specs/2026-10-13-arm64-and-mixed-architecture.md).
package hostarch

import (
	"fmt"
	"strings"
)

// Command prints the architecture in Debian's names (amd64, arm64), the
// same names Docker's apt source and image platforms use.
const Command = "dpkg --print-architecture"

// Runner runs a command on a host: apply.Transport, hostprep.Transport.
type Runner interface {
	Run(command string) (string, error)
}

// Supported reports whether the toolkit deploys to arch.
func Supported(arch string) bool { return arch == "amd64" || arch == "arm64" }

// Parse is what Command printed on site, refused when it is not supported.
func Parse(site, out string) (string, error) {
	arch := strings.TrimSpace(out)
	if !Supported(arch) {
		return "", fmt.Errorf("%s: the host reports %s. The toolkit supports amd64 and arm64.", site, arch)
	}
	return arch, nil
}

// Read asks site's host for its architecture.
func Read(site string, t Runner) (string, error) {
	out, err := t.Run(Command)
	if err != nil {
		return "", fmt.Errorf("%s: reading the architecture (%s): %s: %w", site, Command, strings.TrimSpace(out), err)
	}
	return Parse(site, out)
}

// Mismatch refuses a host that reports another architecture than the one
// recorded for its site. A host replaced under the same name by one of
// another architecture would otherwise be prepared, then fail inside
// replication or be handed a restore it cannot read. Deleting the line is the
// operator saying the change is meant.
func Mismatch(site, recorded, reported string) error {
	if recorded == "" || recorded == reported {
		return nil
	}
	return fmt.Errorf("%s: paisans-stack.yaml records arch %s, and the host reports %s. If this is new hardware, delete the arch line from %s and run again.", site, recorded, reported, site)
}

// Runs reports whether an image published for platforms (as
// imagedigest.Build.Platform names them: amd64, arm64/v8, windows/amd64)
// has a Linux build for arch. A variant does not matter: every arm64 host
// runs arm64/v8.
func Runs(platforms []string, arch string) bool {
	for _, p := range platforms {
		if p == arch || strings.HasPrefix(p, arch+"/") {
			return true
		}
	}
	return false
}

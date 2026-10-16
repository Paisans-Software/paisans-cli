package hostcheck

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The host check accepts Docker from any source except a snap, provided the
// engine and the compose plugin are at least these versions. Each minimum is
// the newest version among the features the toolkit depends on;
// docs/specs/2026-10-11-docker-version-gate.md keeps the full table,
// including the features older than both.
//
// MinEngine, 25.0.0, is set by healthcheck.start_interval, which the
// pocket-id compose template renders. Docker Engine 25.0 release notes,
// 25.0.0 (API 1.44): "Add an additional interval to be used by healthchecks
// during the container start period." Compose refuses the key on an engine
// below API 1.44 (docker/compose#11360, in v2.24.1). The next newest is
// `--format json`, which apply's prune passes to `docker image ls`: Docker
// Engine 23.0 release notes, 23.0.0, "Add the --format=json shorthand
// variant of --format="{{ json . }}"". That one is the CLI's, so the
// minimum is checked against the CLI as well as the daemon.
//
// MinCompose, 2.24.0, is set by the same key: docker/compose v2.24.0
// release notes, "Add support for start_interval" (docker/compose#10939).
// The next newest is `docker compose ps --format json` with the keys
// internal/apply/health.go reads, from v2.21.0 (docker/compose#10918), and
// parseContainers reads the array older releases printed as well.
const (
	MinEngine  = "25.0.0"
	MinCompose = "2.24.0"
)

// leadingVersion is the numeric part a version string starts with, after an
// optional "v": Ubuntu's docker.io reports its engine as "27.5.1" and its
// package as "27.5.1-0ubuntu3~24.04.2", Debian's as "27.5.1+dfsg", and an
// old Compose's --short printed "v2.2.0".
var leadingVersion = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

// parseVersion reads major, minor and patch from the start of a version
// string, ignoring any distribution or prerelease suffix.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	m := leadingVersion.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return v, false
	}
	for i := 0; i < 3; i++ {
		if m[i+1] != "" {
			v[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return v, true
}

// AtLeast reports whether version is min or newer, and false for a version
// that cannot be read.
func AtLeast(version, min string) bool {
	v, ok := parseVersion(version)
	if !ok {
		return false
	}
	m, _ := parseVersion(min)
	for i := 0; i < 3; i++ {
		if v[i] != m[i] {
			return v[i] > m[i]
		}
	}
	return true
}

// dockerRefusal is why the Docker on a host cannot run the toolkit, or nil.
// The source does not matter, except a snap: its confinement keeps it from
// bind mounting under /srv, and snapd refreshes it, restarting the daemon
// and every container, on snapd's schedule rather than the operator's. A
// Docker without a working `docker compose` is not refused here: host
// prepare installs the plugin, or names the packages that keep it from
// working.
func dockerRefusal(host string, d Docker) error {
	for _, p := range d.Packages {
		if p == "snap" {
			return &ui.Problem{
				Hint:    host + " runs Docker from a snap, which the toolkit cannot use",
				Explain: "A snap's confinement keeps Docker from bind mounting under /srv, where every stack keeps its data, and snapd refreshes it on its own schedule, restarting every container. Remove it yourself (snap remove docker), after checking nothing running depends on it, and run host prepare, which installs Docker from Docker's own apt repository.",
			}
		}
	}
	if !d.Present {
		return nil
	}
	var old []string
	if !AtLeast(d.Version, MinEngine) {
		old = append(old, fmt.Sprintf("- Docker Engine %s, and at least %s is needed", orUnknown(d.Version), MinEngine))
	}
	if d.Client != "" && !AtLeast(d.Client, MinEngine) {
		old = append(old, fmt.Sprintf("- the docker CLI %s, and at least %s is needed", d.Client, MinEngine))
	}
	if d.ComposeWorks && !AtLeast(d.Compose, MinCompose) {
		old = append(old, fmt.Sprintf("- Docker Compose %s, and at least %s is needed", orUnknown(d.Compose), MinCompose))
	}
	if len(old) == 0 {
		return nil
	}
	return &ui.Problem{
		Hint:    host + "'s Docker is older than the toolkit needs, so nothing was changed",
		Explain: strings.Join(old, "\n") + "\nUpgrade Docker from wherever it came from, or remove it, after checking nothing running depends on it, and run host prepare, which installs Docker from Docker's own apt repository. Then run again.",
	}
}

func orUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "of a version that could not be read"
	}
	return strings.TrimSpace(v)
}

// parseDocker reads dockerProbe's output: "absent", or the engine's and the
// CLI's versions on one line and "compose <version>" or "compose absent" on
// the next.
func parseDocker(out string) Docker {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	first := strings.Fields(lines[0])
	if len(first) == 0 || first[0] == "absent" {
		return Docker{}
	}
	d := Docker{Present: true, Version: first[0]}
	if len(first) > 1 {
		d.Client = first[1]
	}
	for _, line := range lines[1:] {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "compose "); ok {
			v = strings.TrimSpace(v)
			if v == "absent" {
				d.ComposeMissing = true
			} else {
				d.ComposeWorks = true
				d.Compose = strings.TrimPrefix(v, "v")
			}
		}
	}
	return d
}

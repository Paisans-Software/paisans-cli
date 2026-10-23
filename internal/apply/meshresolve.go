package apply

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// How long to wait for the peers' endpoint names to resolve before WireGuard
// runs, and how often to ask. A record created by `paisans dns init` a moment
// ago is served by the zone's nameservers within seconds; the wait covers that
// gap, not a record that does not exist.
var (
	resolveWait = 30 * time.Second
	resolvePoll = 2 * time.Second
)

// endpointNames are the host names among the peer endpoints of a WireGuard
// file, in order and once each. An endpoint written as an address needs no
// resolving and is left out.
func endpointNames(content string) []string {
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "endpoint") {
			continue
		}
		host, _, err := net.SplitHostPort(strings.TrimSpace(value))
		if err != nil || host == "" || net.ParseIP(host) != nil || seen[host] {
			continue
		}
		seen[host] = true
		names = append(names, host)
	}
	return names
}

// resolveProbe flushes the host's DNS cache and prints each of names that
// does not resolve, one a line; nothing means every name resolves. wg resolves
// an endpoint once and treats "not found" as final, and systemd-resolved
// keeps a "not found" for the zone's negative TTL, so a host that asked
// before the record existed would fail wg-quick for minutes after it does.
// The flush is skipped on a host without resolvectl, and a flush that fails
// (systemd-resolved not running) leaves nothing to flush. getent ahosts asks
// getaddrinfo, as wg does.
func resolveProbe(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = shellQuote(name)
	}
	return "if command -v resolvectl >/dev/null 2>&1; then resolvectl flush-caches >/dev/null 2>&1 || true; fi; " +
		"for n in " + strings.Join(quoted, " ") + `; do getent ahosts "$n" >/dev/null 2>&1 || echo "$n"; done`
}

// resolveMeshEndpoints waits for the peers' endpoint names to resolve on the
// site, flushing its DNS cache before each try: the first lookup after a
// flush caches its own "not found" when the record is not there yet. A name
// that still does not resolve stops the apply before the interface is
// touched. A file whose peers are all addresses asks nothing.
func resolveMeshEndpoints(plan *Plan, t Transport) error {
	names := plan.meshEndpoints
	if len(names) == 0 {
		return nil
	}
	done := plan.step("resolve mesh endpoints")
	done.Detail("flush the DNS cache on %s and wait up to %s for %s to resolve", plan.Site, resolveWait, strings.Join(names, ", "))
	err := waitForNames(plan, t, names)
	finish(done, err)
	return err
}

func waitForNames(plan *Plan, t Transport, names []string) error {
	command := resolveProbe(names)
	attempts := int(resolveWait/resolvePoll) + 1
	var missing []string
	for i := 0; i < attempts; i++ {
		out, err := t.Run(command)
		if err != nil {
			return fmt.Errorf("resolving the mesh endpoints on %s: %w", plan.Site, err)
		}
		missing = strings.Fields(out)
		if len(missing) == 0 {
			return nil
		}
		if i < attempts-1 {
			sleep(resolvePoll)
		}
	}
	return unresolvedProblem(plan, missing)
}

// unresolvedProblem is the operator's problem for endpoint names that do not
// resolve, with the advice meshProblem gives wg-quick failing on one.
func unresolvedProblem(plan *Plan, missing []string) error {
	subject := missing[0] + " does"
	record := "its DNS record"
	if len(missing) > 1 {
		subject = strings.Join(missing[:len(missing)-1], ", ") + " and " + missing[len(missing)-1] + " do"
		record = "their DNS records"
	}
	return &ui.Problem{
		Hint: "mesh: " + plan.Deployment.WireGuardUnit() + " was not started",
		Explain: fmt.Sprintf("%s not resolve from %s; create %s (%s dns init) and apply again.\nNothing that binds the mesh address was started.",
			subject, plan.Site, record, progname.Cmd()),
		Cause: fmt.Errorf("getent ahosts found no address for %s on %s after %s, with the DNS cache flushed before each try",
			strings.Join(missing, ", "), plan.Site, resolveWait),
	}
}

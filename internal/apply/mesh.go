package apply

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// wireguardAddress is the Address line of a WireGuard file, as written
// (Eg: 10.44.0.1/24), or empty when it has none.
func wireguardAddress(content string) string {
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "address") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// meshAddressProbe prints the interface's state and addresses, one line, and
// nothing when the interface does not exist. Like wireguardProbe it prints
// rather than exits non zero, so that an absent interface cannot be confused
// with ssh failing.
func meshAddressProbe(iface string) string {
	return "ip -br addr show dev " + iface + " 2>/dev/null || true"
}

// bringUpMesh runs the plan's WireGuard command and reads the interface back.
// A mesh that did not come up is the operator's problem, explained from the
// unit's journal; a read that failed is returned as it is.
func bringUpMesh(plan *Plan, t Transport, command string) error {
	out, err := t.Run(command)
	if err != nil {
		return meshProblem(plan, t, fmt.Errorf("%s: %w\n%s", plan.WireGuard.Title(plan.Deployment), err, strings.TrimRight(out, "\n")))
	}
	err = meshUp(plan, t)
	if errors.As(err, new(*meshDownError)) {
		return meshProblem(plan, t, err)
	}
	return err
}

// meshUp reads the kernel after WireGuard ran, because the unit's exit status
// is not the interface: a start that reported success over an interface that
// never appeared let the apply start the infrastructure stack, and etcd crash
// looped on an address it could not bind, which the apply then reported as
// the stack's failure. An interface that is not up is a *meshDownError; any
// other error is the read itself failing.
func meshUp(plan *Plan, t Transport) error {
	iface := plan.Deployment.Interface()
	out, err := t.Run(meshAddressProbe(iface))
	if err != nil {
		return fmt.Errorf("reading %s: %w", iface, err)
	}
	fields := strings.Fields(out)
	down := &meshDownError{iface: iface, want: plan.meshAddress}
	switch {
	case len(fields) < 2:
		down.absent = true
		return down
	case fields[1] == "DOWN":
		down.state = fields[1]
		return down
	case plan.meshAddress == "":
		return nil
	}
	for _, address := range fields[2:] {
		if address == plan.meshAddress {
			return nil
		}
	}
	down.have = fields[2:]
	return down
}

// meshDownError is an interface that is not up as the site's file says after
// WireGuard ran: absent, down, or without the site's address.
type meshDownError struct {
	iface  string
	absent bool
	state  string
	want   string
	have   []string
}

func (e *meshDownError) Error() string {
	switch {
	case e.absent:
		return e.iface + " does not exist"
	case e.state != "":
		return e.iface + " is " + e.state
	case len(e.have) == 0:
		return fmt.Sprintf("%s is up without %s", e.iface, e.want)
	}
	return fmt.Sprintf("%s is up with %s, not %s", e.iface, strings.Join(e.have, " "), e.want)
}

// address reports whether the interface is up and only its address is wrong.
func (e *meshDownError) address() bool { return !e.absent && e.state == "" }

// journalLines is how much of the unit's journal a failure reads: enough for
// one attempt of wg-quick, which logs a line per command it runs.
const journalLines = 20

// meshProblem turns a mesh that did not come up into the operator's problem,
// with wg-quick's own error read from the unit's journal. Only the line that
// says what went wrong is in the explanation; the journal is the cause, shown
// with -v. A journal that cannot be read leaves the explanation without it.
func meshProblem(plan *Plan, t Transport, cause error) error {
	d := plan.Deployment
	unit := d.WireGuardUnit()
	journalCommand := fmt.Sprintf("journalctl -u %s -n %d --no-pager", unit, journalLines)
	problem := &ui.Problem{
		Hint:  "mesh: " + unit + " did not come up",
		Cause: cause,
	}
	stopped := "Nothing that binds the mesh address was started."

	// An interface that is up with the wrong address is not wg-quick
	// failing, so its journal has nothing to add.
	var down *meshDownError
	if errors.As(cause, &down) && down.address() {
		problem.Explain = fmt.Sprintf("%s, which every service binds. Restart %s on %s and apply again.\n%s",
			down.Error(), unit, plan.Site, stopped)
		return problem
	}

	journal, err := t.Run(journalCommand)
	if err != nil {
		problem.Explain = fmt.Sprintf("Read why on %s with `%s`, fix it and apply again.\n%s", plan.Site, journalCommand, stopped)
		return problem
	}
	problem.Cause = fmt.Errorf("%w\n%s:\n%s", cause, journalCommand, strings.TrimRight(journal, "\n"))
	line := wgQuickError(journal)
	if name := unresolvedName(line); name != "" {
		problem.Explain = fmt.Sprintf("%s does not resolve from %s; create its DNS record (paisans dns init) and apply again.\n%s", name, plan.Site, stopped)
		return problem
	}
	if line != "" {
		problem.Explain = fmt.Sprintf("wg-quick: %s\n%s", line, stopped)
		return problem
	}
	problem.Explain = fmt.Sprintf("Read why on %s with `%s`, fix it and apply again.\n%s", plan.Site, journalCommand, stopped)
	return problem
}

// wgQuickError is the first line wg-quick printed in its last attempt that is
// not a command it ran ("[#] ..."), without the journal's prefix: what went
// wrong rather than how it was cleaned up. Empty when there is none.
func wgQuickError(journal string) string {
	var attempt []string
	for _, line := range strings.Split(journal, "\n") {
		_, message, ok := strings.Cut(line, " wg-quick[")
		if !ok {
			continue
		}
		if _, message, ok = strings.Cut(message, "]: "); !ok {
			continue
		}
		if strings.HasPrefix(message, "[#] ip link add ") {
			attempt = nil
		}
		attempt = append(attempt, strings.TrimSpace(message))
	}
	for _, message := range attempt {
		if message != "" && !strings.HasPrefix(message, "[#]") {
			return message
		}
	}
	return ""
}

// resolutionErrors are what getaddrinfo's gai_strerror says for a name that
// does not resolve, which wg prints before the endpoint it could not resolve
// (wireguard-tools, src/config.c, parse_endpoint).
var resolutionErrors = []string{
	"Name or service not known",
	"Temporary failure in name resolution",
	"No address associated with hostname",
	"nodename nor servname provided, or not known",
}

// unresolvedName is the host name in wg's line for an endpoint that does not
// resolve (Eg: "Name or service not known: `health.example.org:51820'"),
// empty for any other line.
func unresolvedName(line string) string {
	resolution := false
	for _, e := range resolutionErrors {
		if strings.Contains(line, e) {
			resolution = true
			break
		}
	}
	if !resolution {
		return ""
	}
	_, endpoint, ok := strings.Cut(line, "`")
	if !ok {
		return ""
	}
	endpoint, _, _ = strings.Cut(endpoint, "'")
	if i := strings.LastIndex(endpoint, ":"); i > 0 {
		endpoint = endpoint[:i]
	}
	return strings.Trim(endpoint, "[]")
}

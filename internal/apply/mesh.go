package apply

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
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

// runningStep is what an up interface whose file did not change still
// needs. The helper's put and its sync or restart are separate commands
// from writing the file, so a run that stopped between them leaves the file
// as rendered and the interface on what it ran before. Read back: without
// the file's Address it is restarted, since only wg-quick applies an
// address; on another key or other peers it is synced; otherwise it needs
// nothing. The address is read with ip, which needs no privilege, and the
// key and peers through the helper's show.
func runningStep(site string, d deployment.Deployment, content string, t Transport) (WireGuardStep, error) {
	iface := d.Interface()
	out, err := t.Run(meshAddressProbe(iface))
	if err != nil {
		return WireGuardNone, fmt.Errorf("reading %s: %w", iface, err)
	}
	fields := strings.Fields(out)
	if address := wireguardAddress(content); address != "" && (len(fields) < 3 || !slices.Contains(fields[2:], address)) {
		return WireGuardRestart, nil
	}
	h := WGHelper{T: t, Iface: iface}
	key, err := h.Show("public-key")
	if err != nil {
		return WireGuardNone, helperProblem(site, h, err)
	}
	peers, err := h.Show("peers")
	if err != nil {
		return WireGuardNone, helperProblem(site, h, err)
	}
	want, err := fileKey(content)
	if err != nil {
		return WireGuardNone, fmt.Errorf("%s's mesh file: %w", site, err)
	}
	running := strings.Fields(peers)
	listed := filePeers(content)
	slices.Sort(running)
	slices.Sort(listed)
	if strings.TrimSpace(key) != want || !slices.Equal(running, listed) {
		return WireGuardSync, nil
	}
	return WireGuardNone, nil
}

// fileKey is the public key of a WireGuard file's PrivateKey.
func fileKey(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "PrivateKey" {
			return render.PublicKey(strings.TrimSpace(value))
		}
	}
	return "", fmt.Errorf("it has no PrivateKey")
}

// filePeers is every PublicKey a WireGuard file lists, in its order.
func filePeers(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "PublicKey" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

// bringUpMesh hands the interface the plan's file through the WireGuard
// helper and runs the plan's step, then reads the interface back. A mesh
// that did not come up is the operator's problem, explained from the unit's
// journal; the helper's own refusals are already one; a put that failed
// otherwise started nothing, so it and a read that failed are returned as
// they are.
func bringUpMesh(plan *Plan, t Transport) error {
	if err := handMesh(plan, t, plan.WireGuard); err != nil {
		if errors.As(err, new(*ui.Problem)) || errors.As(err, new(*putError)) {
			return err
		}
		return meshProblem(plan, t, fmt.Errorf("%s: %w", plan.WireGuard.Title(plan.Deployment), err))
	}
	err := meshUp(plan, t)
	if errors.As(err, new(*meshDownError)) {
		return meshProblem(plan, t, err)
	}
	return err
}

// handMesh puts the plan's mesh file through the WireGuard helper, then
// runs step's verb: up, sync or restart. The put comes first every time, so
// the interface is always started, synced or restarted from the file this
// apply wrote, whatever /etc/wireguard held before.
func handMesh(plan *Plan, t Transport, step WireGuardStep) error {
	h := WGHelper{T: t, Iface: plan.Deployment.Interface()}
	if plan.meshConf == "" {
		return fmt.Errorf("%s: this plan holds no mesh file to hand %s", plan.Site, h.Iface)
	}
	if err := h.Put([]byte(plan.meshConf)); err != nil {
		if problem := helperProblem(plan.Site, h, err); problem != err {
			return problem
		}
		return &putError{fmt.Errorf("%s: handing %s its mesh file through the WireGuard helper: %w", plan.Site, h.Iface, err)}
	}
	var err error
	switch step {
	case WireGuardStart:
		err = h.Up()
	case WireGuardSync:
		err = h.Sync()
	case WireGuardRestart:
		err = h.Restart()
	}
	if err != nil {
		return helperProblem(plan.Site, h, err)
	}
	return nil
}

// putError is a put that failed for a reason that is not the helper's,
// such as ssh: nothing was installed and nothing was started, so there is
// no unit whose journal could say more.
type putError struct{ err error }

func (e *putError) Error() string { return e.err.Error() }
func (e *putError) Unwrap() error { return e.err }

// SyncMesh hands the interface the plan's mesh file and syncs its peers in
// place, for a caller resuming a run that wrote the file and stopped before
// the interface had it.
func SyncMesh(plan *Plan, t Transport) error {
	return handMesh(plan, t, WireGuardSync)
}

// helperProblem turns the WireGuard helper's refusals into the operator's
// problem and returns any other error as it is. put refusing the file is
// read first, since sudo can print a warning and still run the helper: it
// names the line, and the usual cause is an endpoint port or mesh subnet
// changed since the owner admitted the deployment; a refusal that says the
// host is wrong is answered by the owner running host admit again. sudo
// refusing to run the helper at all is a host that has not admitted this
// deployment, or has lost the helper or its sudoers line.
func helperProblem(site string, h WGHelper, err error) error {
	const stopped = "Nothing that binds the mesh address was started."
	text := err.Error()
	path, _ := h.Path()
	if line := refusedConf(text); line != "" {
		advice := "The helper installs only what paisans renders, on the ListenPort and inside the mesh subnet the host's owner admitted. When the site's endpoint port or mesh.subnet has changed since, the owner admits the deployment again from a new request."
		if hostRefusal(line) {
			advice = "That is the host, not the mesh file: the host's owner runs `paisans host admit` again, which installs the helper and /etc/wireguard as the helper expects them, then apply again."
		}
		return &ui.Problem{
			Hint:    "mesh: the WireGuard helper refused " + site + "'s mesh file",
			Explain: fmt.Sprintf("%s. %s\n%s", ui.Visible(line), advice, stopped),
			Cause:   err,
		}
	}
	if line := sudoRefusal(text, path); line != "" {
		advice := "`paisans host admit` installs the helper and the one sudoers line that lets this deployment's user run it: the host's owner admits the deployment, then apply again."
		if strings.Contains(line, ttyRefusal) {
			advice = "sudo on this host requires a terminal (requiretty), and apply has none: the host's owner removes that requirement for the helper, then apply again."
		}
		return &ui.Problem{
			Hint:    "mesh: the WireGuard helper did not run on " + site,
			Explain: fmt.Sprintf("sudo would not run %s (%s). %s\n%s", path, ui.Visible(line), advice, stopped),
			Cause:   err,
		}
	}
	return err
}

// sudoRefusals are what sudo prints when it will not run a command, as
// opposed to a warning it prints before running it anyway (Eg: "unable to
// resolve host").
var sudoRefusals = []string{
	"a password is required",
	"a terminal is required",
	"no tty present",
	ttyRefusal,
	" is not allowed to execute ",
	"account validation failure",
	"PAM account management error",
}

// ttyRefusal is what sudo prints when its requiretty option is set, which
// host admit does not answer: the owner lifts it for the helper.
const ttyRefusal = "sorry, you must have a tty to run sudo"

// sudoRefusal is the line sudo printed when it would not run the helper at
// path, such as "sudo: a password is required", "Sorry, user ... is not
// allowed to execute ...", "sudo: <path>: command not found" or "sudo:
// unable to execute <path>: ...", empty when there is none.
func sudoRefusal(out, path string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if path != "" && (line == "sudo: "+path+": command not found" || strings.HasPrefix(line, "sudo: unable to execute "+path+":")) {
			return line
		}
		for _, refusal := range sudoRefusals {
			if strings.Contains(line, refusal) {
				return line
			}
		}
	}
	return ""
}

// hostRefusals are the helper's refusals that say the host is wrong rather
// than the file: what it installs into is not as host admit left it, or the
// helper itself was rendered wrong.
var hostRefusals = []string{" is not a directory", " is a directory"}

// hostRefusal reports whether why, a refusal refusedConf read, is one host
// admit answers rather than a configuration change.
func hostRefusal(why string) bool {
	if why == "the helper was rendered with a bad subnet or port" {
		return true
	}
	if strings.HasPrefix(why, "line ") {
		return false
	}
	for _, r := range hostRefusals {
		if strings.HasSuffix(why, r) {
			return true
		}
	}
	return false
}

// refusedConf is why the helper's put refused a file, from the line it
// prints ("wg-psns-f2a9: line 5: ...; nothing was installed"), without its
// name and ending, empty when there is none.
func refusedConf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		why, ok := strings.CutSuffix(line, "; nothing was installed")
		if !ok {
			continue
		}
		if _, rest, ok := strings.Cut(why, ": "); ok {
			return rest
		}
		return why
	}
	return ""
}

// journalTail is the last n lines of a journal, newlines kept.
func journalTail(journal string, n int) string {
	lines := strings.Split(strings.TrimRight(journal, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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

// journalLines is how much of the unit's journal a failure keeps: enough for
// one attempt of wg-quick, which logs a line per command it runs. The
// helper's log prints the last 200; the rest is older attempts.
const journalLines = 20

// meshProblem turns a mesh that did not come up into the operator's problem,
// with wg-quick's own error read from the unit's journal, which the
// deployment's user reads through the helper's log. Only the line that says
// what went wrong is in the explanation; the journal is the cause, shown
// with -v. A journal that cannot be read leaves the explanation without it.
func meshProblem(plan *Plan, t Transport, cause error) error {
	d := plan.Deployment
	unit := d.WireGuardUnit()
	h := WGHelper{T: t, Iface: d.Interface()}
	logCommand := h.CommandLine("log")
	problem := &ui.Problem{
		Hint:  "mesh: " + unit + " did not come up",
		Cause: cause,
	}
	stopped := "Nothing that binds the mesh address was started."

	// An interface that is up with the wrong address is not wg-quick
	// failing, so its journal has nothing to add.
	var down *meshDownError
	if errors.As(cause, &down) && down.address() {
		problem.Explain = fmt.Sprintf("%s, which every service binds. Run `%s` on %s and apply again.\n%s",
			down.Error(), h.CommandLine("restart"), plan.Site, stopped)
		return problem
	}

	journal, err := h.Log()
	if err != nil {
		problem.Explain = fmt.Sprintf("Read why on %s with `%s`, fix it and apply again.\n%s", plan.Site, logCommand, stopped)
		return problem
	}
	journal = journalTail(journal, journalLines)
	problem.Cause = fmt.Errorf("%w\n%s:\n%s", cause, logCommand, journal)
	line := wgQuickError(journal)
	if name := unresolvedName(line); name != "" {
		problem.Explain = fmt.Sprintf("%s does not resolve from %s; create its DNS record (paisans dns init) and apply again.\n%s", name, plan.Site, stopped)
		return problem
	}
	if line != "" {
		problem.Explain = fmt.Sprintf("wg-quick: %s\n%s", ui.Visible(line), stopped)
		return problem
	}
	problem.Explain = fmt.Sprintf("Read why on %s with `%s`, fix it and apply again.\n%s", plan.Site, logCommand, stopped)
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

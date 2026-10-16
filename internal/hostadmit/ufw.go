package hostadmit

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What --enable-ufw adds to the plan, after the deployment's own rules: an
// allow for every TCP port sshd listens on, tagged sshAllowTag; the
// fallback timer; then ufw enabled. Every rule goes in while ufw is still
// inactive, which ufw accepts and applies the moment it is enabled, so
// there is no instant at which ufw is on and SSH is not allowed. The timer
// is armed before the enable, so a login that something admit cannot see
// still blocks (a cloud firewall, a port sshd will listen on later) is let
// back in when it fires. The command stops it once a new SSH login gets
// through (DisarmUFW).
const (
	// sshAllowTag is the comment on the SSH allows --enable-ufw adds. It is
	// not a deployment's tag, so evict never removes them: they are the
	// owner's way in, whichever deployment's admit added them.
	sshAllowTag = "paisans: ssh"
	// FallbackUnit is the transient timer that turns ufw off again.
	FallbackUnit = "paisans-ufw-fallback"
	// FallbackAfter is how long after it is armed the timer fires.
	FallbackAfter = 10 * time.Minute
	armFallback   = "systemd-run --on-active=600 --unit=" + FallbackUnit + " ufw disable"
	enableUFW     = "ufw --force enable"
	// DisarmUFW stops the fallback timer, run as root once a new SSH login
	// has got through ufw.
	DisarmUFW = "systemctl stop " + FallbackUnit + ".timer"
)

// ufwBeforeMarker is a string only ufwBeforeProbe contains.
const ufwBeforeMarker = "paisans host admit: before enabling ufw"

// ufwBeforeProbe reads, as root, what enabling ufw depends on. The ports
// sshd listens on are read from the sockets themselves, with ss, not from
// sshd -T: on a host whose sshd is socket activated (Ubuntu 24.04's
// default) systemd holds the listening socket and the Port lines sshd -T
// prints say nothing of it, and sshd_config may have changed since sshd
// started. ss shows what a new login connects to now; an active ssh.socket
// or sshd.socket adds what systemd listens on for it. ufw's rules are read
// with `ufw show added`, which lists them while ufw is inactive. Whatever
// it cannot read it says so.
const ufwBeforeProbe = `# ` + ufwBeforeMarker + `
if out=$(ss -Hltnp 2>/dev/null); then printf '%s\n' "$out" | grep -F '("sshd",pid=' | sed 's/^/sshd /'; else echo 'unread listeners (ss -Hltnp)'; fi
for u in ssh.socket sshd.socket; do
  if [ "$(systemctl is-active "$u" 2>/dev/null)" = active ]; then systemctl show -p Listen --value "$u" 2>/dev/null | sed 's/^/socket /'; fi
done
if added=$(ufw show added 2>/dev/null); then printf '%s\n' "$added" | sed -n 's/^ufw /rule /p'; else echo 'unread rules (ufw show added)'; fi
if command -v systemd-run >/dev/null 2>&1; then echo 'systemd-run present'; else echo 'systemd-run absent'; fi
echo "fallback $(systemctl show -p LoadState --value ` + FallbackUnit + `.timer 2>/dev/null) $(systemctl show -p LoadState --value ` + FallbackUnit + `.service 2>/dev/null)"
echo end`

// ufwBefore is what ufwBeforeProbe found: the TCP ports sshd listens on,
// sorted; what it could not read; ufw's rules as `ufw show added` prints
// them without the leading ufw; whether systemd-run is there; and the
// fallback units' load states.
type ufwBefore struct {
	ports       []int
	unread      string
	rulesUnread string
	rules       []string
	systemdRun  bool
	fallback    string
}

// readUFWBefore runs ufwBeforeProbe and reads it.
func readUFWBefore(p *planner) (ufwBefore, error) {
	var b ufwBefore
	out, err := p.t.Run(ufwBeforeProbe)
	if err != nil {
		return b, fmt.Errorf("reading the ports sshd listens on: %w", shown(err))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[len(lines)-1] != "end" {
		return b, fmt.Errorf("reading the ports sshd listens on: the answer was cut short. Nothing was changed")
	}
	seen := map[int]bool{}
	add := func(addr string) {
		if i := strings.LastIndex(addr, ":"); i >= 0 {
			addr = addr[i+1:]
		}
		if n, err := strconv.Atoi(addr); err == nil && n > 0 && n < 65536 && addr == strconv.Itoa(n) && !seen[n] {
			seen[n] = true
			b.ports = append(b.ports, n)
		}
	}
	for _, line := range lines[:len(lines)-1] {
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "unread":
			if strings.HasPrefix(rest, "rules ") {
				b.rulesUnread = hostText(rest)
			} else if b.unread == "" {
				b.unread = hostText(rest)
			}
		case "sshd":
			if f := strings.Fields(rest); len(f) >= 4 {
				add(f[3])
			}
		case "socket":
			if f := strings.Fields(rest); len(f) == 2 && f[1] == "(Stream)" {
				add(f[0])
			}
		case "rule":
			b.rules = append(b.rules, rest)
		case "systemd-run":
			b.systemdRun = rest == "present"
		case "fallback":
			b.fallback = strings.TrimSpace(rest)
		}
	}
	slices.Sort(b.ports)
	return b, nil
}

// UFWInactiveError is admit's refusal of a host whose ufw is installed and
// not active, without --enable-ufw: SSHPorts are the TCP ports sshd
// listens on, and Unread what the probe could not read, "" when it read
// everything. The command asks the owner at a terminal, and builds the
// plan again with --enable-ufw on a yes.
type UFWInactiveError struct {
	SSHPorts []int
	Unread   string
}

func (e *UFWInactiveError) Error() string {
	if len(e.SSHPorts) == 0 {
		why := "found no port sshd listens on"
		if e.Unread != "" {
			why = "could not read the " + e.Unread + ", so it cannot tell which ports sshd listens on"
		}
		return "ufw is installed but not active, and admit " + why + ". It enables ufw only behind an allow for every one of them, so --enable-ufw refuses here too, and admit gives no commands to enable ufw by hand. Enabling ufw without an allow for your SSH port cuts off SSH to this host: allow the port your SSH server listens on with `sudo ufw allow <port>/tcp` before anything enables ufw, then run admit again. Nothing was changed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ufw is installed but not active. The safe way: run admit again with --enable-ufw. It allows every port sshd listens on (%s), arms a timer that turns ufw off again in 10 minutes, enables ufw, and stops the timer once a new SSH login gets through.\n", portList(e.SSHPorts))
	b.WriteString("To enable it by hand instead, run these in this order:\n")
	for i, port := range e.SSHPorts {
		fmt.Fprintf(&b, "  %d. sudo ufw allow %d/tcp\n", i+1, port)
	}
	fmt.Fprintf(&b, "  %d. sudo ufw enable\n", len(e.SSHPorts)+1)
	steps := "step 1"
	switch n := len(e.SSHPorts); {
	case n == 2:
		steps = "steps 1 and 2"
	case n > 2:
		steps = fmt.Sprintf("steps 1 to %d", n)
	}
	fmt.Fprintf(&b, "Enabling ufw without %s cuts off SSH to this host, your own login included. Then run admit again. Nothing was changed", steps)
	return b.String()
}

// portList is ports as ufw names TCP ports, comma separated.
func portList(ports []int) string {
	var out []string
	for _, port := range ports {
		out = append(out, strconv.Itoa(port)+"/tcp")
	}
	return strings.Join(out, ", ")
}

// ufwRule is the comment on a line of `ufw show added`, which cannot hold a
// quote, so the last quoted string is the whole of it.
var ufwRule = regexp.MustCompile(`^(.*) comment '[^']*'$`)

// governs is the rule of rules that governs TCP port, as `ufw allow
// <port>/tcp` or `ufw allow <port>` writes one, and whether it lets the
// port in: allow and limit do, deny and reject do not. "" when none does.
func governs(rules []string, port int) (rule string, allows bool) {
	for _, r := range rules {
		body := r
		if m := ufwRule.FindStringSubmatch(r); m != nil {
			body = m[1]
		}
		action, key := "", ""
		for _, f := range strings.Fields(body) {
			switch {
			case action == "":
				action = f
			case f == "log" || f == "log-all":
			default:
				key += " " + f
			}
		}
		key = strings.TrimSpace(key)
		if key == strconv.Itoa(port)+"/tcp" || key == strconv.Itoa(port) {
			return r, action == "allow" || action == "limit"
		}
	}
	return "", false
}

// planEnableUFW plans, with --enable-ufw, enabling ufw on a host where it
// is installed and not active, after the deployment's rules: the SSH
// allows, the fallback timer, then ufw enabled. It refuses, and enables
// nothing, when it cannot tell which ports sshd listens on, when one of
// them is denied by a rule of the owner's, or when the timer cannot be
// armed.
func planEnableUFW(p *planner, b ufwBefore) error {
	const nothing = " Nothing was changed"
	switch {
	case b.unread != "":
		return fmt.Errorf("--enable-ufw: admit could not read the %s, so it cannot tell which ports sshd listens on, and it never enables ufw without allowing every one of them.%s", b.unread, nothing)
	case b.rulesUnread != "":
		return fmt.Errorf("--enable-ufw: admit could not read the %s, so it cannot tell whether a rule of yours denies SSH, and it never enables ufw blind.%s", b.rulesUnread, nothing)
	case len(b.ports) == 0:
		return fmt.Errorf("--enable-ufw: admit found no port sshd listens on, and it never enables ufw without allowing the port your own login arrives by. Allow your SSH port with `sudo ufw allow <port>/tcp` before anything enables ufw.%s", nothing)
	case !b.systemdRun:
		return fmt.Errorf("--enable-ufw: systemd-run is not on this host, so admit cannot arm the timer that turns ufw off again if SSH is cut off, and it never enables ufw without one.%s", nothing)
	case b.fallback != "not-found not-found":
		return fmt.Errorf("--enable-ufw: %s, the timer that turns ufw off again, is already loaded (%s), most likely from an earlier admit that did not finish, so admit cannot arm it afresh. Check `systemctl status %s.timer`, then run `systemctl stop %s.timer; systemctl reset-failed %s.service` and run admit again.%s",
			FallbackUnit, ui.Visible(b.fallback), FallbackUnit, FallbackUnit, FallbackUnit, nothing)
	}
	for _, port := range b.ports {
		spec := fmt.Sprintf("allow %d/tcp", port)
		switch rule, allows := governs(b.rules, port); {
		case rule != "" && allows:
			p.present(fmt.Sprintf("firewall: SSH on %d/tcp allowed by `ufw %s`", port, ui.Visible(rule)))
			continue
		case rule != "":
			return fmt.Errorf("--enable-ufw: `ufw %s` blocks %d/tcp, which sshd listens on, and admit did not add it. Enabling ufw with it in place would cut off SSH; delete or change it yourself and run admit again.%s", ui.Visible(rule), port, nothing)
		}
		// The deployment's own allow of the same port would be rewritten
		// in place by ours, since ufw keeps one rule per match: ours alone
		// is planned, so evict never takes it away.
		own := "ufw " + spec + " comment '" + p.d.Prefix() + ": "
		if i := slices.IndexFunc(p.plan.Steps, func(s Step) bool { return strings.HasPrefix(s.Command, own) }); i >= 0 {
			p.plan.Steps = slices.Delete(p.plan.Steps, i, i+1)
			p.present(fmt.Sprintf("firewall: %d/tcp allowed by the %s rule, in place of the deployment's own SSH allow", port, sshAllowTag))
		}
		p.step(Step{Title: fmt.Sprintf("allow SSH on %d/tcp", port),
			Describe: fmt.Sprintf("firewall: allow %d/tcp, which sshd listens on, before ufw is enabled; tagged %s, so evict never removes it: it is your own way in", port, sshAllowTag),
			Command:  "ufw " + spec + " comment '" + sshAllowTag + "'", Kinds: []string{"firewall"}})
	}
	p.step(Step{Title: "arm the ufw fallback",
		Describe: fmt.Sprintf("firewall: a timer, %s.timer, that runs ufw disable %d minutes from now, so a login ufw blocks is let back in; admit stops it once a new SSH login gets through", FallbackUnit, int(FallbackAfter/time.Minute)),
		Command:  armFallback, Kinds: []string{"firewall"}})
	p.step(Step{Title: "enable ufw",
		Describe: "firewall: enable ufw, every rule above already in place, with the default policy ufw has (Ubuntu's denies incoming)",
		Command:  enableUFW, Kinds: []string{"firewall"}})
	p.plan.EnablesUFW = true
	p.record.UFWEnabled = true
	return nil
}

// Disarm stops the fallback timer on the host t reaches as root, once a
// new SSH login has got through ufw.
func Disarm(t apply.Transport) error {
	if _, err := t.Run(DisarmUFW); err != nil {
		return fmt.Errorf("stopping %s.timer: %w", FallbackUnit, shown(err))
	}
	return nil
}

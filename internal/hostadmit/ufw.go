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
	// AccuracySec=1s makes it fire on time: a transient timer's default
	// accuracy is a minute.
	armFallback = "systemd-run --on-active=600 --timer-property=AccuracySec=1s --unit=" + FallbackUnit + " ufw disable"
	enableUFW   = "ufw --force enable"
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
// or sshd.socket adds what systemd listens on for it. Only root's sshd
// listeners off loopback count (sshdListener): a per-session sshd holds a
// session's X11 and remote forwards as the logged-in user or on loopback,
// and any user can name a process sshd. ufw's rules are read
// with `ufw show added`, which lists them while ufw is inactive, and IPV6
// as ufw reads it (ipv6Read), without which ufw drops all IPv6 once
// enabled.
// Whatever it cannot read it says so.
var ufwBeforeProbe = `# ` + ufwBeforeMarker + `
if out=$(ss -Hltnpe 2>/dev/null); then printf '%s\n' "$out" | grep -F '("sshd",pid=' | sed 's/^/sshd /'; else echo 'unread listeners (ss -Hltnpe)'; fi
for u in ssh.socket sshd.socket; do
  if [ "$(systemctl is-active "$u" 2>/dev/null)" = active ]; then systemctl show -p Listen --value "$u" 2>/dev/null | sed 's/^/socket /'; fi
done
if added=$(ufw show added 2>/dev/null); then printf '%s\n' "$added" | sed -n 's/^ufw /rule /p'; else echo 'unread rules (ufw show added)'; fi
` + ipv6Read("/etc/default/ufw", "/etc/ufw/ufw.conf") + `
if command -v systemd-run >/dev/null 2>&1; then echo 'systemd-run present'; else echo 'systemd-run absent'; fi
echo "fallback $(systemctl show -p LoadState --value ` + FallbackUnit + `.timer 2>/dev/null) $(systemctl show -p LoadState --value ` + FallbackUnit + `.service 2>/dev/null)"
echo end`

// ufwBefore is what ufwBeforeProbe found: the TCP ports sshd listens on,
// sorted, and whether one of them is on IPv6; what it could not read;
// ufw's rules as `ufw show added` prints them without the leading ufw, and
// those of them that deny or reject; IPV6 in /etc/default/ufw; whether
// systemd-run is there; and the fallback units' load states.
type ufwBefore struct {
	ports       []int
	v6          bool
	unread      string
	rulesUnread string
	rules       []string
	denies      []string
	ipv6        string
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
		// ss prints an IPv6 socket as [addr]:port, or *:port when it takes
		// both families; loopback is never filtered.
		if (strings.HasPrefix(addr, "[") && !strings.HasPrefix(addr, "[::1]")) || strings.HasPrefix(addr, "*:") {
			b.v6 = true
		}
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
			if addr, ok := sshdListener(rest); ok {
				add(addr)
			}
		case "socket":
			if f := strings.Fields(rest); len(f) == 2 && f[1] == "(Stream)" {
				add(f[0])
			}
		case "rule":
			b.rules = append(b.rules, rest)
			// A route rule governs forwarded traffic, not what reaches sshd.
			if action, _, _ := strings.Cut(rest, " "); action == "deny" || action == "reject" {
				b.denies = append(b.denies, rest)
			}
		case "ipv6":
			b.ipv6 = strings.TrimSpace(rest)
		case "systemd-run":
			b.systemdRun = rest == "present"
		case "fallback":
			b.fallback = strings.TrimSpace(rest)
		}
	}
	slices.Sort(b.ports)
	return b, nil
}

// sshdListener is the local address of one `ss -Hltnpe` line of an sshd
// listener, when it is one a login arrives by: root's, since ss -e prints
// uid:N for every other owner, and off loopback, which ufw never filters.
func sshdListener(line string) (string, bool) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return "", false
	}
	for _, field := range f[4:] {
		if strings.HasPrefix(field, "uid:") {
			return "", false
		}
	}
	addr := f[3]
	if strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "[::1]") || strings.HasPrefix(addr, "[::ffff:127.") {
		return "", false
	}
	return addr, true
}

// blocker is why admit will not enable ufw on this host, with
// --enable-ufw or without it, nil when nothing stops it. Each refusal says
// what the owner can do; none gives a command that enables ufw unless it
// is safe to run once the owner has checked what it names.
func (b ufwBefore) blocker() error {
	const head = "ufw is installed but not active, and admit will not enable it: "
	const nothing = " Nothing was changed"
	switch {
	case b.unread != "":
		return fmt.Errorf(head+"it could not read the %s, so it cannot tell which ports sshd listens on, and it never enables ufw without allowing every one of them. Enabling ufw without an allow for your SSH port cuts off SSH to this host: allow the port your SSH server listens on with `sudo ufw allow <port>/tcp` before anything enables ufw, then run admit again.%s", b.unread, nothing)
	case b.rulesUnread != "":
		return fmt.Errorf(head+"it could not read the %s, so it cannot tell whether a rule of yours stops SSH, and it never enables ufw blind.%s", b.rulesUnread, nothing)
	case len(b.ports) == 0:
		return fmt.Errorf(head+"it found no port sshd listens on, and it never enables ufw without allowing the port your own login arrives by. Enabling ufw without an allow for your SSH port cuts off SSH to this host: allow the port your SSH server listens on with `sudo ufw allow <port>/tcp` before anything enables ufw, then run admit again.%s", nothing)
	case len(b.denies) > 0:
		var rules []string
		for _, r := range b.denies {
			rules = append(rules, "  `ufw "+ui.Visible(r)+"`")
		}
		return fmt.Errorf(head+"ufw already holds these rules of yours:\n%s\nufw applies the first rule a connection matches, so they decide what reaches SSH, and admit neither reads every form a rule takes nor overrides yours. Check that none of them blocks the ports sshd listens on (%s), and delete or change any that does; then enable ufw yourself, in this order:\n%s Then run admit again.%s",
			strings.Join(rules, "\n"), portList(b.ports), byHand(b.ports), nothing)
	case b.v6 && b.ipv6 != "yes":
		setting := "is not set"
		if b.ipv6 != "" {
			setting = "is " + ui.Visible(b.ipv6)
		}
		return fmt.Errorf(head+"IPV6, as ufw reads it from /etc/default/ufw and then /etc/ufw/ufw.conf, %s, and sshd listens on IPv6. Enabled so, ufw drops all IPv6 traffic, SSH over IPv6 included, and its allows reach IPv4 alone. Set IPV6=yes in /etc/default/ufw, and in /etc/ufw/ufw.conf if it sets IPV6 too, then run admit again with --enable-ufw.%s", setting, nothing)
	case !b.systemdRun:
		return fmt.Errorf(head+"systemd-run is not on this host, so admit cannot arm the timer that turns ufw off again if SSH is cut off, and it never enables ufw without one. To enable ufw yourself, in this order:\n%s Then run admit again.%s", byHand(b.ports), nothing)
	case b.fallback != "not-found not-found":
		return fmt.Errorf(head+"%s, the timer that turns ufw off again, is already loaded (%s), most likely from an earlier admit that did not finish, so admit cannot arm it afresh. Check `systemctl status %s.timer`, then run `systemctl stop %s.timer; systemctl reset-failed %s.service` and run admit again.%s",
			FallbackUnit, ui.Visible(b.fallback), FallbackUnit, FallbackUnit, FallbackUnit, nothing)
	}
	return nil
}

// byHand is the commands that enable ufw by hand, numbered, every allow
// before the enable, and what enabling without the allows does.
func byHand(ports []int) string {
	var b strings.Builder
	for i, port := range ports {
		fmt.Fprintf(&b, "  %d. sudo ufw allow %d/tcp\n", i+1, port)
	}
	fmt.Fprintf(&b, "  %d. sudo ufw enable\n", len(ports)+1)
	steps := "step 1"
	switch n := len(ports); {
	case n == 2:
		steps = "steps 1 and 2"
	case n > 2:
		steps = fmt.Sprintf("steps 1 to %d", n)
	}
	fmt.Fprintf(&b, "Enabling ufw without %s cuts off SSH to this host, your own login included.", steps)
	return b.String()
}

// UFWInactiveError is admit's refusal, without --enable-ufw, of a host
// whose ufw is installed and not active and which nothing else stops admit
// enabling (ufwBefore.blocker): SSHPorts are the TCP ports sshd listens on,
// never empty. The command asks the owner at a terminal, and builds the
// plan again with --enable-ufw on a yes.
type UFWInactiveError struct {
	SSHPorts []int
}

func (e *UFWInactiveError) Error() string {
	return fmt.Sprintf("ufw is installed but not active. The safe way: run admit again with --enable-ufw. It allows every port sshd listens on (%s), arms a timer that turns ufw off again in 10 minutes, enables ufw, and stops the timer once a new SSH login gets through.\n"+
		"To enable it by hand instead, run these in this order:\n%s Then run admit again. Nothing was changed", portList(e.SSHPorts), byHand(e.SSHPorts))
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
// allows, the fallback timer, then ufw enabled. planFirewall has refused
// already whatever ufwBefore.blocker names.
func planEnableUFW(p *planner, b ufwBefore) error {
	for _, port := range b.ports {
		spec := fmt.Sprintf("allow %d/tcp", port)
		if rule, allows := governs(b.rules, port); allows {
			p.present(fmt.Sprintf("firewall: SSH on %d/tcp allowed by `ufw %s`", port, ui.Visible(rule)))
			continue
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
		Describe: fmt.Sprintf("firewall: a timer, %s.timer, that runs ufw disable %d minutes from now, so a login ufw blocks is let back in; admit stops it once a new SSH login gets through. It does not survive a reboot: a reboot before it fires leaves ufw enabled, so wait for it rather than reboot", FallbackUnit, int(FallbackAfter/time.Minute)),
		Command:  armFallback, Kinds: []string{"firewall"}})
	p.step(Step{Title: "enable ufw",
		Describe: "firewall: enable ufw, every rule above already in place, with the default policy ufw has (Ubuntu's denies incoming). " + CannotSee,
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

// CannotSee is what admit says, in the plan and in the question, it does
// not see before it enables ufw.
const CannotSee = "Admit allows only the TCP ports sshd listens on, and cannot see DROP rules written by hand in /etc/ufw/before.rules, before6.rules or user.rules, nor other ways in such as sslh, dropbear or mosh: if you rely on either, enable ufw by hand after checking them."

// ipv6Read is the probe's line that reads IPV6 as ufw's backend.py
// (_get_defaults) does: defaults, then conf, each line that starts with a
// key and a value of word characters, the key in any case, the value
// lowercased with its quotes taken off, the last one winning. Anything but
// yes leaves ufw's IPv6 off, and admit reads it so.
func ipv6Read(defaults, conf string) string {
	return `v=$(grep -hE '^[Ii][Pp][Vv]6="?[A-Za-z0-9_]' ` + defaults + ` ` + conf + ` 2>/dev/null | tail -n 1 | cut -d= -f2 | tr -d "\"'" | tr '[:upper:]' '[:lower:]'); echo "ipv6 $v"`
}

package hostadmit

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
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
const ufwBeforeMarker = "paisans site admit: before enabling ufw"

// ufwBeforeProbe reads, as root, what enabling ufw depends on. The ports
// sshd listens on are read from the sockets themselves, with ss, not from
// sshd -T: on a host whose sshd is socket activated (Ubuntu 24.04's
// default) systemd holds the listening socket and the Port lines sshd -T
// prints say nothing of it, and sshd_config may have changed since sshd
// started. ss shows what a new login connects to now; an active ssh.socket
// or sshd.socket adds what systemd listens on for it. Only root's sshd
// listeners off loopback count (sshdListener): a per-session sshd holds a
// session's X11 and remote forwards as the logged-in user or on loopback,
// and any user can name a process sshd. Every other listener, TCP and UDP,
// and what receives traffic with no listener, a port the system Docker
// publishes or a DNAT or redirect rule (natProbe), is read too: enabling
// ufw blocks whatever of it no rule allows (exposure). ufw's rules are
// read with `ufw show added`, which lists them while ufw is inactive, and
// IPV6 as ufw reads it (ipv6Read), without which ufw drops all IPv6 once
// enabled. Whatever it cannot read it says so.
var ufwBeforeProbe = `# ` + ufwBeforeMarker + `
` + listenProbe("") + `
for u in ssh.socket sshd.socket; do
  if [ "$(systemctl is-active "$u" 2>/dev/null)" = active ]; then systemctl show -p Listen --value "$u" 2>/dev/null | sed 's/^/socket /'; fi
done
if added=$(ufw show added 2>/dev/null); then printf '%s\n' "$added" | sed -n 's/^ufw /rule /p'; else echo 'unread rules (ufw show added)'; fi
` + ipv6Read("/etc/default/ufw", "/etc/ufw/ufw.conf") + natProbe + `
if command -v systemd-run >/dev/null 2>&1; then echo 'systemd-run present'; else echo 'systemd-run absent'; fi
echo "fallback $(systemctl show -p LoadState --value ` + FallbackUnit + `.timer 2>/dev/null) $(systemctl show -p LoadState --value ` + FallbackUnit + `.service 2>/dev/null)"
echo end`

// ufwBefore is what ufwBeforeProbe found: the TCP ports sshd listens on,
// sorted, and whether one of them is on IPv6; what it could not read;
// ufw's rules as `ufw show added` prints them without the leading ufw, and
// those of them that deny or reject; IPV6 in /etc/default/ufw; whether
// systemd-run is there; the fallback units' load states; and what answers
// from off the host, the probe's listen, listen, container, stopped
// and dnat lines, for exposure to read.
type ufwBefore struct {
	ports       []int
	v6          bool
	unread      string
	rulesUnread string
	// exposureUnread is the first of the rest admit could not read, which
	// says what answers from off the host.
	exposureUnread string
	rules          []string
	denies         []string
	ipv6           string
	systemdRun     bool
	fallback       string
	answering      []string
	// kept and blocked are exposure's: what stays reachable once ufw is
	// on, and what enabling it blocks.
	kept    []kept
	blocked []Blocked
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
			switch {
			case strings.HasPrefix(rest, "rules "):
				b.rulesUnread = hostText(rest)
			case strings.HasPrefix(rest, "listeners "):
				if b.unread == "" {
					b.unread = hostText(rest)
				}
			case b.exposureUnread == "":
				b.exposureUnread = hostText(rest)
			}
		case "listen":
			if s, ok := readSocket(rest); ok && strings.Contains(rest, `("sshd",pid=`) && sshdListener(s) {
				add(s.Local)
			}
			b.answering = append(b.answering, line)
		case "container", "stopped", "dnat":
			b.answering = append(b.answering, line)
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

// sshdListener reports whether s, a listener of sshd's, is one a login
// arrives by: TCP, root's, since ss -e prints uid:N for every other owner,
// and off loopback, which ufw never filters.
func sshdListener(s socket) bool {
	return s.Proto == "tcp" && s.UID == 0 && !s.loopback()
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
	case b.exposureUnread != "":
		return fmt.Errorf(head+"it could not read the %s, so it cannot tell which of your services enabling ufw would block, and it never enables ufw blind.%s", b.exposureUnread, nothing)
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
	Blocked  []Blocked
}

func (e *UFWInactiveError) Error() string {
	blocked := ""
	if len(e.Blocked) > 0 {
		blocked = "Enabling ufw, by admit or by hand, would also block these, which answer from off this host now:\n" + BlockedText(e.Blocked) + "\n"
	}
	return fmt.Sprintf("ufw is installed but not active. The safe way: run admit again with --enable-ufw. It allows every port sshd listens on (%s), arms a timer that turns ufw off again in 10 minutes, enables ufw, and stops the timer once a new SSH login gets through.\n"+
		"%sTo enable it by hand instead, run these in this order:\n%s Then run admit again. Nothing was changed", portList(e.SSHPorts), blocked, byHand(e.SSHPorts))
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
	describe := "firewall: enable ufw, every rule above already in place, with the default policy ufw has (Ubuntu's denies incoming). " + CannotSee
	if len(b.kept) > 0 {
		var list []string
		for _, k := range b.kept {
			list = append(list, k.key+" ("+k.via+"), "+k.why)
		}
		describe += " Stays reachable: " + strings.Join(list, "; ") + "."
	}
	if len(b.blocked) > 0 {
		var list []string
		for _, x := range b.blocked {
			list = append(list, x.Key+" ("+x.What+")")
			p.plan.UFWBlocks = append(p.plan.UFWBlocks, x.Key)
		}
		describe += " Blocks, as --enable-ufw-block-listed accepts: " + strings.Join(list, "; ") + "."
	}
	p.step(Step{Title: "enable ufw", Describe: describe, Command: enableUFW, Kinds: []string{"firewall"}})
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
const CannotSee = "Admit allows only the TCP ports sshd listens on, and names every other port that answers from off this host as it reads it. It cannot see DROP rules written by hand in /etc/ufw/before.rules, before6.rules or user.rules, nor a way in that is not listening as it reads the host, such as sslh, dropbear or mosh started later: if you rely on either, enable ufw by hand after checking them."

// ipv6Read is the probe's line that reads IPV6 as ufw's backend.py
// (_get_defaults) does: defaults, then conf, each line that starts with a
// key and a value of word characters, the key in any case, the value
// lowercased with its quotes taken off, the last one winning. Anything but
// yes leaves ufw's IPv6 off, and admit reads it so.
func ipv6Read(defaults, conf string) string {
	return `v=$(grep -hE '^[Ii][Pp][Vv]6="?[A-Za-z0-9_]' ` + defaults + ` ` + conf + ` 2>/dev/null | tail -n 1 | cut -d= -f2 | tr -d "\"'" | tr '[:upper:]' '[:lower:]'); echo "ipv6 $v"`
}

// Blocked is one thing enabling ufw blocks that answers from off the host
// now: Key names it, a port as ufw names one ("80/tcp", "7000:7001/udp"),
// such a port with " over IPv6" for one only ufw's IPV6 setting keeps, or
// what it is for one whose ports admit cannot read; What is what answers
// on it, as the owner is shown it; Allow is the command that keeps it
// reachable, "" for none.
type Blocked struct {
	Key, What, Allow string
	// NAT is a port received through NAT, which ufw's routed rules decide
	// too.
	NAT bool
}

// UFWBlocksError is admit's refusal, with --enable-ufw and without
// --enable-ufw-block-listed, of a host where enabling ufw blocks what
// answers from off it now.
type UFWBlocksError struct {
	Blocked []Blocked
}

func (e *UFWBlocksError) Error() string {
	return "ufw is installed but not active, and enabling it would block these, which answer from off this host now:\n" +
		BlockedText(e.Blocked) + " Nothing was changed"
}

// BlockedText is blocked as the owner is shown it: BlockedList, then
// BlockedHelp, then the flag that accepts them.
func BlockedText(blocked []Blocked) string {
	return BlockedList(blocked) + BlockedHelp(blocked) + " Once you have checked that every one listed may be blocked, pass --enable-ufw-block-listed with --enable-ufw."
}

// BlockedList is each one of blocked with what answers on it, a line each.
func BlockedList(blocked []Blocked) string {
	var b strings.Builder
	for _, x := range blocked {
		if x.Allow == "" && !x.v6() {
			fmt.Fprintf(&b, "  %s\n", x.What)
		} else {
			fmt.Fprintf(&b, "  %s: %s\n", x.Key, x.What)
		}
	}
	return b.String()
}

// BlockedHelp is how the owner keeps each one of blocked reachable: the
// lines to run before ufw is enabled, then admit again, and what to do
// with one meant to stay unreachable.
func BlockedHelp(blocked []Blocked) string {
	var b strings.Builder
	var allows []string
	var v6, unread, nat bool
	for _, x := range blocked {
		if x.Allow != "" {
			allows = append(allows, "  "+x.Allow+"\n")
		}
		v6 = v6 || x.v6()
		unread = unread || x.Allow == "" && !x.v6()
		nat = nat || x.NAT
	}
	if len(allows) > 0 {
		b.WriteString("For each port whose service must stay reachable, run its line before ufw is enabled, then run admit again:\n" + strings.Join(allows, ""))
	}
	if v6 {
		b.WriteString("ufw's IPV6, read from /etc/default/ufw and then /etc/ufw/ufw.conf, is not yes, so enabling ufw drops all IPv6 traffic, allows included: for a service that must stay reachable over IPv6, set IPV6=yes in /etc/default/ufw, and in /etc/ufw/ufw.conf if it sets IPV6 too.\n")
	}
	if unread {
		b.WriteString("Admit cannot read which ports a NAT rule or a stopped container listed receives on: allow those whose service must stay reachable with `sudo ufw allow <port>/<proto>`.\n")
	}
	if nat {
		b.WriteString("A port received through NAT, as a container's published port is, reaches its service as routed traffic, which ufw's route rules and its DEFAULT_FORWARD_POLICY decide as well: check it from off this host once ufw is on.\n")
	}
	b.WriteString("A service meant to stay unreachable from off this host, such as a database on a public address, needs nothing.")
	return b.String()
}

// v6 reports whether x is a port only ufw's IPV6 setting keeps.
func (x Blocked) v6() bool { return strings.HasSuffix(x.Key, " over IPv6") }

// ufwKept is what ufw's own before.rules let in on a UDP port whatever
// the owner's rules say.
var ufwKept = map[int]string{
	68:   "ufw's before.rules accept DHCP replies to it",
	546:  "ufw's before6.rules accept DHCPv6 replies to it",
	1900: "ufw's before.rules accept SSDP multicast to it",
	5353: "ufw's before.rules accept mDNS multicast to it",
}

// loopbackRule is a NAT rule that matches a loopback destination alone,
// which only the host itself reaches.
var loopbackRule = regexp.MustCompile(`(?:-d |daddr )(?:127\.|::1\b)`)

// answering is one port, or range of ports, that answers from off the
// host now: what serves it, whether through NAT, and which of what serves
// it takes IPv6.
type answering struct {
	span
	via, v6 []string
	nat     bool
}

func (a answering) key() string {
	if a.lo == a.hi {
		return fmt.Sprintf("%d/%s", a.lo, a.proto)
	}
	return fmt.Sprintf("%d:%d/%s", a.lo, a.hi, a.proto)
}

// kept is one port that answers from off the host now and stays
// reachable once ufw is on: what serves it, and why it stays.
type kept struct{ key, via, why string }

// exposure is what enabling ufw does to what answers from off the host
// now, as ufwBeforeProbe read it: what stays reachable and why, and what
// it blocks. A listener on loopback, which ufw never filters, and the
// deployment's own, run as its user or in its subordinate range, answer
// nothing here. What stays reachable is sshd's ports, which admit allows,
// the deployment's own allows, what ufw's before.rules let in, and what a
// rule of the owner's allows (allowedBy), added while ufw is inactive or
// not.
func (p *planner) exposure(b ufwBefore) ([]kept, []Blocked) {
	var all []*answering
	at := map[string]*answering{}
	add := func(s span, via string, nat, v6 bool) {
		if s.proto != "tcp" && s.proto != "udp" {
			return
		}
		s.addr = ""
		a := &answering{span: s}
		if found := at[a.key()]; found != nil {
			a = found
		} else {
			at[a.key()] = a
			all = append(all, a)
		}
		if !slices.Contains(a.via, via) {
			a.via = append(a.via, via)
		}
		if v6 && !slices.Contains(a.v6, via) {
			a.v6 = append(a.v6, via)
		}
		a.nat = a.nat || nat
	}
	var unread []Blocked
	unreadable := func(what string) {
		if !slices.ContainsFunc(unread, func(x Blocked) bool { return x.Key == what }) {
			unread = append(unread, Blocked{Key: what, What: what})
		}
	}
	for _, line := range b.answering {
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "listen":
			sock, ok := readSocket(rest)
			if !ok || sock.loopback() || p.ownUID(sock.UID) {
				continue
			}
			add(span{proto: sock.Proto, lo: sock.Port, hi: sock.Port}, sock.what(), false, sock.v6())
		case "container":
			f := strings.Split(rest, "\t")
			if len(f) < 2 {
				continue
			}
			for _, s := range publishes(f[1]) {
				if !loopback(s.addr) {
					add(s, containerWhat(f), true, false)
				}
			}
		case "stopped":
			f := strings.Fields(rest)
			if len(f) < 3 || f[1] != "always" {
				continue
			}
			name := hostText(strings.TrimPrefix(f[0], "/"))
			list, err := bindings(strings.Join(f[2:], " "))
			if err != nil {
				unreadable("your Docker's stopped container " + name + ", with restart: always, publishes ports admit cannot read, " + hostText(strings.Join(f[2:], " ")) + ", when your Docker restarts")
			}
			for _, s := range list {
				if !loopback(s.addr) {
					add(s, "your Docker's stopped container "+name+", with restart: always, publishes it when your Docker restarts", true, false)
				}
			}
		case "dnat":
			if loopbackRule.MatchString(rest) {
				continue
			}
			rule := hostText(withoutCounters(rest))
			protos := []string{"tcp", "udp"}
			if tcp, udp := strings.Contains(rest, "tcp"), strings.Contains(rest, "udp"); tcp != udp {
				protos = protos[:1]
				if udp {
					protos = []string{"udp"}
				}
			}
			for _, m := range rulePorts(rest) {
				if m.unread || m.negated {
					unreadable("a NAT rule whose ports admit cannot list (a negation, a set, a map, a concatenation or a name), in " + rule)
					break
				}
				for _, s := range m.spans {
					for _, proto := range protos {
						s.proto = proto
						add(s, "a NAT rule in "+rule, true, false)
					}
				}
			}
		}
	}
	slices.SortStableFunc(all, func(x, y *answering) int {
		return cmp.Or(x.lo-y.lo, x.hi-y.hi, strings.Compare(x.proto, y.proto))
	})
	own := hostprep.AdmitRules(p.d, p.in.Request.SSHPort, p.in.Request.ListenPort, p.in.Flags.PublicPorts)
	var keep []kept
	var blocked []Blocked
	for _, a := range all {
		via := strings.Join(a.via, " and ")
		why, ownPort := "", false
		switch {
		case a.proto == "tcp" && a.lo == a.hi && slices.Contains(b.ports, a.lo):
			why, ownPort = "sshd's port, which admit keeps allowed", true
		case a.proto == "udp" && a.lo == a.hi && ufwKept[a.lo] != "":
			why, ownPort = ufwKept[a.lo], true
		}
		for _, r := range own {
			if why == "" && r.Port != 0 && r.Proto == a.proto && a.lo == r.Port && a.hi == r.Port {
				why, ownPort = "the deployment's own allow ("+r.Why+")", true
			}
		}
		if why == "" {
			if rule := allowedBy(b.rules, a.proto, a.lo, a.hi); rule != "" {
				why = "allowed by `ufw " + ui.Visible(rule) + "`"
			}
		}
		if why != "" {
			keep = append(keep, kept{key: a.key(), via: via, why: why})
		} else {
			blocked = append(blocked, Blocked{Key: a.key(), What: strings.Join(a.via, "; "), Allow: "sudo ufw allow " + a.key(), NAT: a.nat})
		}
		if !ownPort && len(a.v6) > 0 && b.ipv6 != "yes" {
			blocked = append(blocked, Blocked{Key: a.key() + " over IPv6", What: strings.Join(a.v6, "; ")})
		}
	}
	return keep, append(blocked, unread...)
}

// allowedBy is the rule of rules, as `ufw show added` prints them less
// the leading ufw, that lets proto's ports lo to hi in from anywhere, ""
// for none: an allow or a limit of a port, a list or a range holding
// them, with that protocol or none, written `allow [in] [log] <ports>
// [/<proto>]` or `allow [in] [log] [from any] to any port <ports> [proto
// <proto>]`. Any other form, narrowed to a source, an interface or an
// address, or naming an application profile or a service, keeps nothing
// reachable from everywhere as admit reads it.
func allowedBy(rules []string, proto string, lo, hi int) string {
	for _, r := range rules {
		body := r
		if m := ufwRule.FindStringSubmatch(r); m != nil {
			body = m[1]
		}
		f := strings.Fields(body)
		if len(f) == 0 || f[0] != "allow" && f[0] != "limit" {
			continue
		}
		f = f[1:]
		if len(f) > 0 && f[0] == "in" {
			f = f[1:]
		}
		if len(f) > 0 && (f[0] == "log" || f[0] == "log-all") {
			f = f[1:]
		}
		if len(f) >= 2 && f[0] == "from" && f[1] == "any" {
			f = f[2:]
		}
		var ports, rp string
		switch {
		case len(f) == 1:
			ports, rp, _ = strings.Cut(f[0], "/")
		case (len(f) == 4 || len(f) == 6 && f[4] == "proto") && f[0] == "to" && f[1] == "any" && f[2] == "port":
			ports = f[3]
			if len(f) == 6 {
				rp = f[5]
			}
		default:
			continue
		}
		if rp != "" && rp != proto {
			continue
		}
		if portsHold(ports, lo, hi) {
			return r
		}
	}
	return ""
}

// portsHold reports whether ports, a list of ports and ranges as ufw
// writes them (80,443 or 7000:7010), holds every port from lo to hi.
func portsHold(ports string, lo, hi int) bool {
	for _, tok := range strings.Split(ports, ",") {
		m := portSpan.FindStringSubmatch(tok)
		if m == nil {
			return false
		}
		if s, ok := spanOf(m[1], m[2]); ok && s.lo <= lo && hi <= s.hi {
			return true
		}
	}
	return false
}

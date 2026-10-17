package hostadmit

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// planFirewall checks that ufw is installed, checks 80 and 443 before the
// public ports are granted, then plans the deployment's tagged ufw rules
// through the host profile. ufw that is installed but not active is
// enabled only with --enable-ufw (planEnableUFW), after every rule; without
// it admit refuses, naming the commands that enable it safely. Enabling it
// over what answers from off the host and no rule allows needs
// --enable-ufw-block-listed as well (exposure). Admit never
// sets ufw's defaults: choosing the policy is the owner's.
func planFirewall(p *planner) error {
	req := p.in.Request
	var before *ufwBefore
	switch {
	case !p.f.UFWInstalled:
		return fmt.Errorf("ufw is not installed. Admit works only on a host whose firewall is ufw, and does not install one: install ufw, then run admit again with --enable-ufw, which allows your SSH port before it enables ufw. Nothing was changed")
	case !p.f.UFWActive:
		b, err := readUFWBefore(p)
		if err != nil {
			return err
		}
		// What would stop --enable-ufw stops admit without it too, so the
		// owner is never asked, or told to pass the flag, only to be refused.
		if err := b.blocker(); err != nil {
			return err
		}
		// What enabling ufw would block that answers from off the host
		// now stops it, unless the owner has checked it may be blocked.
		b.kept, b.blocked = p.exposure(b)
		if !p.in.Flags.EnableUFW {
			return &UFWInactiveError{SSHPorts: b.ports, Blocked: b.blocked}
		}
		if len(b.blocked) > 0 && !p.in.Flags.EnableUFWBlockListed {
			return &UFWBlocksError{Blocked: b.blocked}
		}
		if p.in.blocksApproved {
			var more []string
			for _, x := range b.blocked {
				if !slices.Contains(p.in.approvedBlocks, x.Key) {
					more = append(more, x.Key+" ("+x.What+")")
				}
			}
			if len(more) > 0 {
				return fmt.Errorf("the host changed while admit ran: enabling ufw would now also block %s, which the plan you approved did not name, so ufw was not enabled. Nothing was changed", strings.Join(more, "; "))
			}
		}
		before = &b
	}
	if p.in.Flags.PublicPorts {
		found, err := holders(p)
		if err != nil {
			return err
		}
		if len(found) > 0 {
			var what []string
			for _, h := range found {
				what = append(what, "- "+h.What)
			}
			return fmt.Errorf("--public-ports cannot be granted: something else answers on 80 or 443, so the deployment's Caddy could never take them:\n%s\nStop it, or run this site behind it with web_server.managed_by: external in the deployment's configuration, which needs no low port. Nothing was changed", strings.Join(what, "\n"))
		}
		p.present("public ports: nothing else answers on 80 or 443")
	}
	s, err := p.profile.Firewall(p.t, p.d, hostprep.AdmitRules(p.d, req.SSHPort, req.ListenPort, p.in.Flags.PublicPorts))
	if err != nil {
		// Build names the part already.
		return errors.New(strings.TrimPrefix(shown(err).Error(), "firewall: ") + ". Nothing was changed")
	}
	present := s.Present
	if before == nil {
		s.Present = append(present, "firewall: ufw active, its default policy left to the owner")
		if p.in.enabledUFW {
			s.Present = append(s.Present, "firewall: ufw enabled by this admit")
		}
	} else {
		s.Present = append(present, "firewall: ufw's default policy left to the owner")
	}
	p.section(s)
	prev, err := p.previous()
	if err != nil {
		return err
	}
	p.record.UFWEnabled = p.in.enabledUFW || prev != nil && prev.UFWEnabled
	if before != nil {
		return planEnableUFW(p, *before)
	}
	if p.in.Flags.EnableUFW && !p.in.enabledUFW {
		p.present("firewall: ufw is active already, so --enable-ufw changes nothing")
	}
	return nil
}

// holder is one thing answering on 80 or 443 that is not the deployment.
type holder struct{ What string }

// portsProbe reads, as root, everything that can answer on 80 or 443: a
// TCP listener or a bound UDP socket with its process and uid, a port the
// system Docker publishes, and a DNAT or redirect rule, which a daemon with
// its userland proxy off publishes with no listener at all, so only the
// ruleset shows it. A UDP socket counts because a gateway's egress table
// accepts the deployment's UDP replies from 443, which an owner's UDP
// service there would receive as its peers' packets. The
// ruleset is read with nft; a host without nft is read with iptables-save,
// which shows the same rules a Docker on iptables writes, and a host with
// neither cannot have its ruleset read. A rule is read as NAT only when it
// translates: one matching conntrack's dnat status alone, as Kubernetes'
// and Docker's filter rules do, does not. A stopped container of the
// system Docker with restart: always retakes its ports when that daemon
// restarts, so it holds them too. Whatever it cannot read it says so, and
// admit refuses rather than read it as nothing.
var portsProbe = `# paisans site admit: who holds 80 and 443
` + listenProbe("'( sport = :80 or sport = :443 )'") + natProbe + `
echo end`

// listenProbe reads, as root, every TCP listener and every bound UDP
// socket ss's filter keeps (every one for ""), with its process and uid,
// as listen lines, each a line hostcheck.ParseSocketLine reads.
func listenProbe(filter string) string {
	if filter != "" {
		filter = " " + filter
	}
	return `if out=$(ss -Hltunpe` + filter + ` 2>/dev/null); then printf '%s\n' "$out" | sed -n 's/^./listen &/p'; else echo 'unread listeners (ss -Hltunpe)'; fi`
}

// natProbe reads, as root, what receives traffic with no listener of its
// own: the ports the system Docker's containers publish, running
// (container lines) and stopped (stopped lines), and the DNAT and redirect
// rules (dnat lines), as portsProbe describes.
const natProbe = `
if [ -S /var/run/docker.sock ]; then
  if out=$(` + systemDocker + ` ps --format '{{.Names}}\t{{.Ports}}\t{{.Label "com.docker.compose.project"}}' 2>/dev/null); then printf '%s\n' "$out" | sed -n 's/^./container &/p'; else echo 'unread containers (ps of the system Docker)'; fi
  if ids=$(` + systemDocker + ` ps -aq --filter status=exited --filter status=created 2>/dev/null); then
    if [ -n "$ids" ]; then
      if out=$(` + systemDocker + ` inspect --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}} {{json .HostConfig.PortBindings}}' $ids 2>/dev/null); then printf '%s\n' "$out" | sed -n 's/^./stopped &/p'; else echo 'unread stopped containers (inspect of the system Docker)'; fi
    fi
  else echo 'unread stopped containers (ps of the system Docker)'; fi
fi
if command -v nft >/dev/null 2>&1; then
  if out=$(nft list ruleset 2>/dev/null); then
    printf '%s\n' "$out" | awk '/^table / { t = $2 " " $3 } /^[ \t]*chain / { c = $2 } { l = $0; gsub(/ct status dnat/, "", l) } (l ~ /dnat/ || l ~ /redirect/) && l ~ /dport/ { sub(/^[ \t]+/, ""); print "dnat table " t " chain " c ": " $0 }'
  else echo 'unread ruleset (nft list ruleset)'; fi
else
  read=
  for save in iptables-save ip6tables-save; do
    command -v $save >/dev/null 2>&1 || continue
    read=1
    if out=$($save -t nat 2>/dev/null); then printf '%s\n' "$out" | grep -E -- '-j (DNAT|REDIRECT)' | sed "s/^/dnat $save -t nat: /"; else echo "unread ruleset ($save)"; fi
  done
  [ -n "$read" ] || echo 'unread ruleset (no nft or iptables-save)'
fi`

var (
	ssUID    = regexp.MustCompile(`\buid:(\d+)`)
	ssProc   = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)
	dport    = regexp.MustCompile(`(! )?(?:--)?dports? (!= )?(\{[^}]*\}|\S+)`)
	lowPort  = regexp.MustCompile(`:(80|443)$`)
	portSpan = regexp.MustCompile(`^(\d+)(?:[-:](\d+))?$`)
)

// holders is everything portsProbe found that is not the deployment's own:
// a listener run as the user or in its subordinate range is its own Caddy,
// on a re-run of an admitted gateway. Everything quoted from the host is
// shown, never drawn, and redacted.
func holders(p *planner) ([]holder, error) {
	out, err := p.t.Run(portsProbe)
	if err != nil {
		return nil, fmt.Errorf("reading what answers on 80 and 443: %w", shown(err))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[len(lines)-1] != "end" {
		return nil, fmt.Errorf("reading what answers on 80 and 443: the answer was cut short. Nothing was changed")
	}
	var found []holder
	for _, line := range lines[:len(lines)-1] {
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "unread":
			return nil, fmt.Errorf("reading what answers on 80 and 443: admit could not read the %s, so it cannot tell whether something else holds them. Nothing was changed", hostText(rest))
		case "listen":
			sock, ok := readSocket(rest)
			if !ok || sock.Port != 80 && sock.Port != 443 || p.ownUID(sock.UID) {
				continue
			}
			found = append(found, holder{sock.what()})
		case "container":
			f := strings.Split(rest, "\t")
			if len(f) < 2 || !publishesLow(f[1]) {
				continue
			}
			found = append(found, holder{containerWhat(f)})
		case "stopped":
			f := strings.Fields(rest)
			if len(f) < 3 || f[1] != "always" || !bindsLow(strings.Join(f[2:], " ")) {
				continue
			}
			found = append(found, holder{"your Docker's stopped container " + hostText(strings.TrimPrefix(f[0], "/")) + " publishes 80 or 443 and, with restart: always, takes them again when your Docker restarts"})
		case "dnat":
			switch holds, unresolved := ruleHoldsLow(rest); {
			case unresolved:
				found = append(found, holder{"a NAT rule whose ports admit cannot read (a set, a map, a concatenation or a name), so it may hold 80 or 443, in " + hostText(withoutCounters(rest))})
			case holds:
				found = append(found, holder{"a NAT rule in " + hostText(withoutCounters(rest))})
			}
		}
	}
	return found, nil
}

// socket is one listen line of a probe, as hostcheck reads it.
type socket struct{ hostcheck.SocketLine }

// readSocket reads one listen line, without its kind.
func readSocket(rest string) (socket, bool) {
	s, ok, err := hostcheck.ParseSocketLine(rest)
	return socket{s}, ok && err == nil && s.Port > 0 && s.Port <= 65535
}

// what is the socket as the owner is shown it, quoted from the host.
func (s socket) what() string {
	on := hostText(s.Local)
	if s.Proto == "udp" {
		on += "/udp"
	}
	if s.Process == "" {
		return fmt.Sprintf("a process (uid %d) listens on %s", s.UID, on)
	}
	return fmt.Sprintf("%s (pid %d, uid %d) listens on %s", hostText(s.Process), s.PID, s.UID, on)
}

// loopback reports whether the socket is bound to loopback alone, which
// ufw never filters: a loopback address, or the lo device.
func (s socket) loopback() bool {
	return loopback(s.Address) || strings.Contains(s.Local, "%lo:")
}

// loopback reports whether addr, as hostcheck.BindAddress normalises one,
// is a loopback address.
func loopback(addr string) bool {
	a, err := netip.ParseAddr(addr)
	return err == nil && a.IsLoopback()
}

// families are the address families the socket takes: 4 and 6 for *,
// which ss prints for a socket of both, 6 for an IPv6 address, and 4 for
// an IPv4 one, mapped into IPv6 or not.
func (s socket) families() []int {
	switch {
	case strings.HasPrefix(s.Local, "*"):
		return []int{4, 6}
	case strings.HasPrefix(s.Local, "[") && !strings.Contains(s.Address, "."):
		return []int{6}
	}
	return []int{4}
}

// v6 reports whether the socket takes IPv6: bound to an IPv6 address, or
// to *, which ss prints for a socket of both families.
func (s socket) v6() bool {
	return strings.HasPrefix(s.Local, "*") || strings.HasPrefix(s.Local, "[") && !strings.Contains(s.Address, ".")
}

// containerWhat is a container line's fields as the owner is shown them.
func containerWhat(f []string) string {
	what := "your Docker's container " + hostText(f[0]) + " publishes " + hostText(f[1])
	if len(f) > 2 && f[2] != "" {
		what += ", in compose project " + hostText(f[2])
	}
	return what
}

// nftCounter is an nft rule's counter as nft lists it, with its values.
var nftCounter = regexp.MustCompile(` counter packets [0-9]+ bytes [0-9]+`)

// withoutCounters is a rule as quoted to the owner: its counter's values
// change from one run to the next and say nothing of what the rule holds.
func withoutCounters(rule string) string { return nftCounter.ReplaceAllString(rule, "") }

// span is a port, or a range of ports from lo to hi, with its protocol
// and, for a published one, the address it is published on.
type span struct {
	proto, addr string
	lo, hi      int
}

func (s span) has(port int) bool { return s.lo <= port && port <= s.hi }

// spanOf is the span a port or a range as text names, false for neither.
func spanOf(from, to string) (span, bool) {
	lo, err := strconv.Atoi(from)
	if err != nil || lo <= 0 || lo > 65535 {
		return span{}, false
	}
	hi := lo
	if to != "" {
		if hi, err = strconv.Atoi(to); err != nil || hi < lo || hi > 65535 {
			return span{}, false
		}
	}
	return span{lo: lo, hi: hi}, true
}

// published is one host port, or range, in docker ps's Ports column, with
// the address it is published on and its protocol.
var published = regexp.MustCompile(`([^\s,]*):(\d+)(?:-(\d+))?->[^\s,]*?/(\w+)`)

// publishes is the host ports docker ps's Ports column publishes.
func publishes(ports string) []span {
	var out []span
	for _, m := range published.FindAllStringSubmatch(ports, -1) {
		if s, ok := spanOf(m[2], m[3]); ok {
			s.addr, s.proto = hostcheck.BindAddress(m[1]), m[4]
			out = append(out, s)
		}
	}
	return out
}

// bindings is the host ports docker inspect's PortBindings bind
// (hostcheck.ParseBindings).
func bindings(raw string) ([]span, error) {
	list, err := hostcheck.ParseBindings(raw)
	if err != nil {
		return nil, err
	}
	out := make([]span, 0, len(list))
	for _, b := range list {
		out = append(out, span{proto: b.Proto, addr: b.Address, lo: b.Lo, hi: b.Hi})
	}
	return out, nil
}

// bindsLow reports whether docker inspect's PortBindings bind 80 or 443 on
// the host; bindings admit cannot read may.
func bindsLow(raw string) bool {
	list, err := bindings(raw)
	return err != nil || slices.ContainsFunc(list, holdsLow)
}

// publishesLow reports whether docker ps's Ports column publishes 80 or
// 443 on the host, alone or within a range.
func publishesLow(ports string) bool { return slices.ContainsFunc(publishes(ports), holdsLow) }

// portMatch is one destination port match of a NAT rule: the spans it
// names, whether it is negated, and unread when its operand is anything
// but ports and ranges (a named set, a map, a concatenation or a
// service's name).
type portMatch struct {
	spans           []span
	negated, unread bool
}

// rulePorts is every destination port match of a DNAT or redirect rule:
// one port, a set, a range (nft's 1-1000, iptables' 1:1000), or a
// negation (nft's !=, iptables' ! --dport), which matches every port it
// does not name.
func rulePorts(rule string) []portMatch {
	var out []portMatch
	for _, m := range dport.FindAllStringSubmatch(rule, -1) {
		pm := portMatch{negated: m[1] != "" || m[2] != ""}
		tokens := strings.FieldsFunc(m[3], func(r rune) bool { return strings.ContainsRune("{}, ", r) })
		pm.unread = len(tokens) == 0
		for _, tok := range tokens {
			s := portSpan.FindStringSubmatch(tok)
			if s == nil {
				pm.unread = true
				break
			}
			if sp, ok := spanOf(s[1], s[2]); ok {
				pm.spans = append(pm.spans, sp)
			} else {
				pm.unread = true
				break
			}
		}
		out = append(out, pm)
	}
	return out
}

// ruleHoldsLow reports whether a DNAT or redirect rule matches 80 or 443
// as its destination port (rulePorts). A negation holds 80 or 443 unless
// it names both. A match admit cannot read is taken to hold them,
// unresolved.
func ruleHoldsLow(rule string) (holds, unresolved bool) {
	for _, m := range rulePorts(rule) {
		if m.unread {
			return true, true
		}
		names := func(port int) bool { return slices.ContainsFunc(m.spans, func(s span) bool { return s.has(port) }) }
		if m.negated && !(names(80) && names(443)) || !m.negated && (names(80) || names(443)) {
			return true, false
		}
	}
	return false, false
}

// holdsLow reports whether s holds 80 or 443.
func holdsLow(s span) bool { return s.has(80) || s.has(443) }

// ownUID reports whether uid is the deployment's user or in its
// subordinate range.
func (p *planner) ownUID(uid int) bool {
	if !p.f.User.Exists {
		return false
	}
	if uid == p.f.User.UID {
		return true
	}
	for _, r := range p.f.SubUID[p.in.Request.User] {
		if int64(uid) >= r.Start && int64(uid) < r.Start+r.Count {
			return true
		}
	}
	return false
}

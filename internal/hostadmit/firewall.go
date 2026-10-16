package hostadmit

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// planFirewall checks that ufw is installed and active, checks 80 and 443
// before the public ports are granted, then plans the deployment's tagged
// ufw rules through the host profile. Admit never enables ufw and never
// sets its defaults: choosing a firewall and its policy is the owner's.
func planFirewall(p *planner) error {
	req := p.in.Request
	switch {
	case !p.f.UFWInstalled:
		return fmt.Errorf("ufw is not installed. Admit works only on a host whose firewall is ufw, and does not install or enable one: install ufw, allow your own SSH port, run `sudo ufw enable`, then run admit again. Nothing was changed")
	case !p.f.UFWActive:
		return fmt.Errorf("ufw is installed but not active. Admit does not enable it: allow your own SSH port, run `sudo ufw enable`, then run admit again. Nothing was changed")
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
	s, err := p.profile.Firewall(p.t, p.d, hostprep.AdmitRules(p.d, req.SSHPort, req.ListenPort, p.in.Flags.PublicPorts), false, "host admit")
	if err != nil {
		// Build names the part already.
		return errors.New(strings.TrimPrefix(shown(err).Error(), "firewall: ") + ". Nothing was changed")
	}
	// A matching rule without the paisans comment is the owner's, most
	// often the SSH allow they added before enabling ufw. Host prepare
	// adopts one as its own; admit adds only its tagged rules and leaves
	// the owner's as it is, which the profile has already reported present.
	var steps []hostprep.Step
	for _, st := range s.Steps {
		if st.Label != "adopt" {
			steps = append(steps, st)
		}
	}
	s.Steps = steps
	// The profile says why it leaves the policy alone in host prepare's
	// terms, a shared host; admit leaves it alone on every host.
	var present []string
	for _, line := range s.Present {
		if !strings.HasPrefix(line, "firewall: default policy and enabled state left alone") {
			present = append(present, line)
		}
	}
	s.Present = append(present, "firewall: ufw active, its default policy left to the owner")
	p.section(s)
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
const portsProbe = `# paisans host admit: who holds 80 and 443
if out=$(ss -Hltnpe '( sport = :80 or sport = :443 )' 2>/dev/null); then printf '%s\n' "$out" | sed -n 's/^./listen &/p'; else echo 'unread listeners (ss)'; fi
if out=$(ss -Hlunpe '( sport = :80 or sport = :443 )' 2>/dev/null); then printf '%s\n' "$out" | sed -n 's/^./listen-udp &/p'; else echo 'unread UDP sockets (ss)'; fi
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
fi
echo end`

var (
	ssUID     = regexp.MustCompile(`\buid:(\d+)`)
	ssProc    = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)
	published = regexp.MustCompile(`:(\d+)(?:-(\d+))?->`)
	dport     = regexp.MustCompile(`(! )?(?:--)?dports? (!= )?(\{[^}]*\}|\S+)`)
	lowPort   = regexp.MustCompile(`:(80|443)$`)
	portSpan  = regexp.MustCompile(`^(\d+)(?:[-:](\d+))?$`)
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
		case "listen", "listen-udp":
			f := strings.Fields(rest)
			if len(f) < 4 || !lowPort.MatchString(f[3]) {
				continue
			}
			// ss prints no uid for a socket root holds.
			uid := 0
			if m := ssUID.FindStringSubmatch(rest); m != nil {
				uid, _ = strconv.Atoi(m[1])
			}
			if p.ownUID(uid) {
				continue
			}
			on := hostText(f[3])
			if kind == "listen-udp" {
				on += "/udp"
			}
			what := fmt.Sprintf("a process (uid %d) listens on %s", uid, on)
			if m := ssProc.FindStringSubmatch(rest); m != nil {
				what = fmt.Sprintf("%s (pid %s, uid %d) listens on %s", hostText(m[1]), m[2], uid, on)
			}
			found = append(found, holder{what})
		case "container":
			f := strings.Split(rest, "\t")
			if len(f) < 2 || !publishesLow(f[1]) {
				continue
			}
			what := "your Docker's container " + hostText(f[0]) + " publishes " + hostText(f[1])
			if len(f) > 2 && f[2] != "" {
				what += ", in compose project " + hostText(f[2])
			}
			found = append(found, holder{what})
		case "stopped":
			f := strings.Fields(rest)
			if len(f) < 3 || f[1] != "always" || !bindsLow(strings.Join(f[2:], " ")) {
				continue
			}
			found = append(found, holder{"your Docker's stopped container " + hostText(strings.TrimPrefix(f[0], "/")) + " publishes 80 or 443 and, with restart: always, takes them again when your Docker restarts"})
		case "dnat":
			switch holds, unresolved := ruleHoldsLow(rest); {
			case unresolved:
				found = append(found, holder{"a NAT rule whose ports are in a set or map admit cannot read, so it may hold 80 or 443, in " + hostText(rest)})
			case holds:
				found = append(found, holder{"a NAT rule in " + hostText(rest)})
			}
		}
	}
	return found, nil
}

// hostPort is a host port in docker inspect's PortBindings, a port or a
// range.
var hostPort = regexp.MustCompile(`"HostPort":"(\d+)(?:-(\d+))?"`)

// bindsLow reports whether docker inspect's PortBindings bind 80 or 443 on
// the host.
func bindsLow(bindings string) bool {
	for _, m := range hostPort.FindAllStringSubmatch(bindings, -1) {
		if holdsLow(m[1], m[2]) {
			return true
		}
	}
	return false
}

// publishesLow reports whether docker ps's Ports column publishes 80 or
// 443 on the host, alone or within a range.
func publishesLow(ports string) bool {
	for _, m := range published.FindAllStringSubmatch(ports, -1) {
		if holdsLow(m[1], m[2]) {
			return true
		}
	}
	return false
}

// ruleHoldsLow reports whether a DNAT or redirect rule matches 80 or 443
// as its destination port: one port, a set, a range (nft's 1-1000,
// iptables' 1:1000), or a negation (nft's !=, iptables' ! --dport), which
// matches every port it does not name, so it holds 80 or 443 unless it
// names both. A port in a named set or a map (@name, map, vmap) is not one
// admit can read, so the rule is taken to hold them, unresolved.
func ruleHoldsLow(rule string) (holds, unresolved bool) {
	for _, m := range dport.FindAllStringSubmatch(rule, -1) {
		if op := m[3]; strings.HasPrefix(op, "@") || op == "map" || op == "vmap" {
			return true, true
		}
		names := map[int]bool{}
		for _, tok := range strings.FieldsFunc(m[3], func(r rune) bool { return strings.ContainsRune("{}, ", r) }) {
			if s := portSpan.FindStringSubmatch(tok); s != nil {
				for _, port := range []int{80, 443} {
					names[port] = names[port] || portIn(s[1], s[2], port)
				}
			}
		}
		negated := m[1] != "" || m[2] != ""
		if negated && !(names[80] && names[443]) || !negated && (names[80] || names[443]) {
			return true, false
		}
	}
	return false, false
}

// portIn reports whether the port from, or the range from to to, holds port.
func portIn(from, to string, port int) bool {
	lo, err := strconv.Atoi(from)
	if err != nil {
		return false
	}
	hi := lo
	if to != "" {
		if hi, err = strconv.Atoi(to); err != nil {
			return false
		}
	}
	return lo <= port && port <= hi
}

// holdsLow reports whether the port from, or the range from to to, holds 80
// or 443.
func holdsLow(from, to string) bool { return portIn(from, to, 80) || portIn(from, to, 443) }

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

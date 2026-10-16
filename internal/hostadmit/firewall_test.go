package hostadmit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const portsMarker = "paisans host admit: who holds 80 and 443"

// gateway is the fixture's gateway site's flags.
var gateway = hostadmit.Flags{Watchdog: true, PublicPorts: true}

var firewallCases = addCases(
	goldenCase{name: "firewall-active-ufw", site: "vm", host: home},
	goldenCase{name: "firewall-active-ufw-with-rules", site: "vm", host: func() *fakeHost {
		return home().answer("ufw show added", "ufw present\nstatus active\nrule allow 22/tcp\n")
	}},
	goldenCase{name: "firewall-ufw-absent-refused", site: "vm", host: func() *fakeHost { return home().with("ufw", "absent").with("ufw-active", "no") }},
	goldenCase{name: "firewall-ufw-inactive-refused", site: "vm", host: inactiveUFW(sshdOn22)},
	goldenCase{name: "firewall-admitted", site: "home-a", flags: gateway, host: func() *fakeHost { return admittedHost(requestFor("home-a"), gateway) }},
	goldenCase{name: "ports-free", site: "home-a", flags: gateway, host: home},
	goldenCase{name: "ports-process-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(portsMarker, `listen LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("nginx",pid=901,fd=6)) ino:1 sk:1`+"\nend\n")
	}},
	goldenCase{name: "ports-container-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().with("system-socket", "present").answer(portsMarker, "container shop-web-1\t0.0.0.0:443->443/tcp, [::]:443->443/tcp\tshop\nend\n")
	}},
	goldenCase{name: "ports-dnat-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(portsMarker, "dnat table ip nat chain DOCKER: iifname != \"docker0\" tcp dport 443 counter packets 0 bytes 0 dnat to 172.17.0.2:443\nend\n")
	}},
	// What the probe printed on Ubuntu 24.04 with a system Docker whose
	// userland proxy is off, publishing 443 for compose project shop.
	goldenCase{name: "ports-docker-without-proxy-refused", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().with("system-socket", "present").answer(portsMarker,
			`listen LISTEN 0      4096   0.0.0.0:443 0.0.0.0:* users:(("dockerd",pid=3848,fd=37)) ino:3382880 sk:1 cgroup:/ <->`+"\n"+
				"container shop-web-1\t0.0.0.0:443->80/tcp\tshop\n"+
				"dnat table ip nat chain DOCKER: tcp dport 443 counter packets 0 bytes 0 dnat to 172.18.0.2:80\nend\n")
	}},
	goldenCase{name: "ports-own-caddy", site: "home-a", flags: gateway, host: ownCaddy},
	goldenCase{name: "ports-elsewhere", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(portsMarker, "container shop-web-1\t0.0.0.0:8443->443/tcp\tshop\n"+
			"dnat table ip nat chain DOCKER: iifname != \"docker0\" tcp dport 8443 counter packets 0 bytes 0 dnat to 172.17.0.2:443\n"+
			"dnat table ip nat chain DOCKER: iifname != \"docker0\" tcp dport 8080 counter packets 0 bytes 0 dnat to 172.17.0.2:80\nend\n")
	}},
)

// ownCaddy is an admitted gateway whose Caddy, the deployment's own, runs
// as its user (uid 1001) on 443 and in its subordinate range on 80, as
// admit finds it on a re-run.
func ownCaddy() *fakeHost {
	return home().answer(portsMarker, `listen LISTEN 0 4096 *:443 *:* users:(("caddy",pid=2201,fd=9)) uid:1001 ino:2 sk:2`+"\n"+
		`listen LISTEN 0 4096 *:80 *:* users:(("caddy",pid=2201,fd=8)) uid:165536 ino:3 sk:3`+"\nend\n")
}

func TestFirewallPlans(t *testing.T) { runGolden(t, firewallCases) }

// The deployment's own Caddy, run as its user or in its subordinate range,
// is not a holder; any other user's listener on the same ports is.
func TestOwnListenerIsNotAHolder(t *testing.T) {
	if _, err := hostadmit.Build(ownCaddy(), input(t, "home-a", gateway)); err != nil {
		t.Fatalf("the deployment's own Caddy refused the grant: %v", err)
	}
	other := home().answer(portsMarker, `listen LISTEN 0 4096 *:443 *:* users:(("caddy",pid=2301,fd=9)) uid:1002 ino:2 sk:2`+"\nend\n")
	_, err := hostadmit.Build(other, input(t, "home-a", gateway))
	if err == nil || !strings.Contains(err.Error(), "caddy") || !strings.Contains(err.Error(), "managed_by: external") {
		t.Fatalf("another user's Caddy on 443: %v", err)
	}
}

// Every shape a holder of 80 or 443 takes refuses the grant: a port range
// that spans one, a set or a range of ports in a rule, and a redirect as
// well as a dnat. A listener ss prints without a uid is root's.
func TestEveryHolderShapeRefuses(t *testing.T) {
	for name, answer := range map[string]string{
		"published range": "container shop-web-1\t0.0.0.0:79-81->79-81/tcp\t\n",
		"dnat set":        "dnat table ip nat chain DOCKER: tcp dport { 22, 443 } dnat to 10.0.0.2\n",
		"dnat range":      "dnat table ip nat chain DOCKER: tcp dport 1-1000 dnat to 10.0.0.2\n",
		"redirect":        "dnat table ip nat chain PREROUTING: tcp dport 80 redirect to :8080\n",
		"iptables":        "dnat iptables-save -t nat: -A DOCKER ! -i docker0 -p tcp -m tcp --dport 443 -j DNAT --to-destination 172.17.0.2:443\n",
		"root listener":   `listen LISTEN 0 511 [::]:80 [::]:* users:(("apache2",pid=7,fd=4)) ino:1 sk:1` + "\n",
		// As ss -Hlunpe printed it on Debian, for a UDP socket bound to 443.
		"udp listener": `listen-udp UNCONN 0      0      0.0.0.0:443 0.0.0.0:* users:(("openvpn",pid=331,fd=3)) ino:3663985 sk:3001 cgroup:/ <->` + "\n",
		"udp dnat":     "dnat table ip nat chain PREROUTING: udp dport 443 dnat to 10.0.0.2\n",
		// As docker inspect prints a stopped container's restart policy
		// and bindings, which it takes again when the daemon restarts.
		"stopped, restart always": `stopped /shop-web-1 always {"443/tcp":[{"HostIp":"","HostPort":"443"}]}` + "\n",
		"iptables negation":       "dnat iptables-save -t nat: -A PREROUTING -p tcp -m tcp ! --dport 22 -j DNAT --to-destination 10.0.0.2\n",
	} {
		h := home().with("system-socket", "present").answer(portsMarker, answer+"end\n")
		_, err := hostadmit.Build(h, input(t, "home-a", gateway))
		if err == nil || !strings.Contains(err.Error(), "--public-ports cannot be granted") {
			t.Errorf("%s: %v", name, err)
		}
		if name == "root listener" && err != nil && !strings.Contains(err.Error(), "uid 0") {
			t.Errorf("a listener with no uid is not named root's: %v", err)
		}
		if name == "udp listener" && err != nil && !strings.Contains(err.Error(), "openvpn (pid 331, uid 0) listens on 0.0.0.0:443/udp") {
			t.Errorf("a UDP listener is not named as one: %v", err)
		}
	}
}

// A stopped container that does not restart on its own does not hold 80
// or 443, nor does one that binds neither.
func TestAStoppedContainerHoldsOnlyWithRestartAlways(t *testing.T) {
	for _, answer := range []string{
		`stopped /shop-web-1 no {"443/tcp":[{"HostIp":"","HostPort":"443"}]}`,
		`stopped /shop-web-1 unless-stopped {"443/tcp":[{"HostIp":"","HostPort":"443"}]}`,
		`stopped /shop-web-1 always {"80/tcp":[{"HostIp":"","HostPort":"8080"}]}`,
	} {
		h := home().with("system-socket", "present").answer(portsMarker, answer+"\nend\n")
		if _, err := hostadmit.Build(h, input(t, "home-a", gateway)); err != nil {
			t.Errorf("%s: %v", answer, err)
		}
	}
}

// What a host prints in a holder is shown, never drawn: an escape in a
// container's name reaches the owner as text.
func TestAHoldersNameIsShownNotDrawn(t *testing.T) {
	h := home().with("system-socket", "present").answer(portsMarker, "container evil\x1b[2J\t0.0.0.0:443->443/tcp\tshop\nend\n")
	_, err := hostadmit.Build(h, input(t, "home-a", gateway))
	if err == nil || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("the escape reached the owner: %q", err)
	}
}

// An answer that stops before its end is refused, never read as nothing
// holding the ports.
func TestACutShortHolderAnswerRefuses(t *testing.T) {
	h := home().answer(portsMarker, "")
	if _, err := hostadmit.Build(h, input(t, "home-a", gateway)); err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Fatalf("a cut-short answer: %v", err)
	}
}

// The probe reads UDP sockets on 80 and 443 as well as TCP listeners: a
// gateway's egress table accepts UDP replies from 443, which are forged into
// an owner's UDP service there, so one refuses the grant.
func TestTheHolderProbeReadsUDP(t *testing.T) {
	h := home()
	if _, err := hostadmit.Build(h, input(t, "home-a", gateway)); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.ran {
		if strings.Contains(c, portsMarker) {
			for _, want := range []string{"ss -Hltnpe '( sport = :80 or sport = :443 )'", "ss -Hlunpe '( sport = :80 or sport = :443 )'", "listen-udp"} {
				if !strings.Contains(c, want) {
					t.Errorf("the probe does not carry %q:\n%s", want, c)
				}
			}
			return
		}
	}
	t.Fatal("the holder probe did not run")
}

// The deployment's own Caddy answers HTTP/3 on UDP 443 as its user, and is
// not a holder.
func TestOwnUDPListenerIsNotAHolder(t *testing.T) {
	h := home().answer(portsMarker, `listen-udp UNCONN 0 0 *:443 *:* users:(("caddy",pid=2201,fd=10)) uid:1001 ino:4 sk:4`+"\nend\n")
	if _, err := hostadmit.Build(h, input(t, "home-a", gateway)); err != nil {
		t.Fatalf("the deployment's own Caddy on UDP 443 refused the grant: %v", err)
	}
}

// Without --public-ports admit never asks who holds 80 and 443.
func TestHoldersAreReadOnlyForThePublicPorts(t *testing.T) {
	h := home()
	if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{})); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.ran {
		if strings.Contains(c, portsMarker) {
			t.Fatalf("holders read for a site without the public ports")
		}
	}
}

// The owner's own SSH allow, which they add before enabling ufw, stays
// theirs: admit never rewrites a rule it did not add.
func TestTheOwnersSSHAllowIsLeftAlone(t *testing.T) {
	h := home().answer("ufw show added", "ufw present\nstatus active\nrule allow 22/tcp\n")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "22/tcp") {
			t.Errorf("a step touches the owner's SSH allow: %s", s.Command)
		}
	}
}

// What the shared ufw planner says in admit's plan names admit, not host
// prepare: a rule admit did not add, one it added and no longer gives, and
// an old SSH allow it keeps.
func TestFirewallTextNamesAdmit(t *testing.T) {
	h := home().answer("ufw show added", "ufw present\nstatus active\n"+
		"rule deny 51820/udp\n"+
		"rule allow 22 comment 'paisans-f2a9: ssh, the bootstrap route'\n"+
		"rule allow 8080/tcp comment 'paisans-f2a9: something given before'\n")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, line := range strings.Split(plan.Text(), "\n") {
		if !strings.Contains(line, "firewall:") {
			continue
		}
		if strings.Contains(line, "host prepare") {
			t.Errorf("admit's plan says host prepare: %s", line)
		}
		if strings.Contains(line, "host admit") {
			named++
		}
	}
	if named != 3 {
		t.Errorf("%d firewall lines name host admit, want 3:\n%s", named, plan.Text())
	}
}

// An owner's deny on the SSH port is refused in admit's terms: admit never
// enables the firewall, and the deny keeps the operators out.
func TestAnOwnersSSHDenyIsRefusedInAdmitsTerms(t *testing.T) {
	h := home().answer("ufw show added", "ufw present\nstatus active\nrule deny 22/tcp\n")
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || strings.Contains(err.Error(), "Enabling") || strings.Contains(err.Error(), "firewall: firewall:") || !strings.Contains(err.Error(), "admit again. Nothing was changed") || !strings.Contains(err.Error(), "operators") {
		t.Errorf("Build = %v", err)
	}
}

// A NAT rule whose ports are in a named set or a map refuses, naming the
// rule and why it cannot be read.
func TestANATRuleOnASetRefuses(t *testing.T) {
	h := home().answer(portsMarker, "dnat table ip nat chain PREROUTING: tcp dport @web dnat ip to 10.0.0.2\nend\n")
	_, err := hostadmit.Build(h, input(t, "home-a", gateway))
	if err == nil || !strings.Contains(err.Error(), "ports admit cannot read") || !strings.Contains(err.Error(), "tcp dport @web") {
		t.Errorf("Build = %v", err)
	}
}

// With --public-ports, HTTP/3's 443/udp is opened beside 80 and 443/tcp,
// tagged like them, since the egress table lets the deployment's Caddy
// answer from it; evict removes it with the rest.
func TestThePublicPortsOpenUDP443(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "watch", hostadmit.Flags{PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	const allow = "ufw allow 443/udp comment 'paisans-f2a9: public ports, HTTP/3'"
	if !slices.ContainsFunc(plan.Steps, func(s hostadmit.Step) bool { return s.Command == allow }) {
		t.Errorf("no step runs %q:\n%s", allow, plan.Text())
	}
	evict, err := hostadmit.BuildEvict(evicted("watch", hostadmit.Flags{PublicPorts: true})(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evict.Steps, func(s hostadmit.Step) bool { return strings.HasPrefix(s.Command, "ufw delete allow 443/udp") }) {
		t.Errorf("evict leaves 443/udp open:\n%s", evict.Text())
	}
}

// A quoted nft rule is shown without its live counters, which change
// between two runs and say nothing of what holds the port.
func TestAQuotedNATRuleHasNoCounters(t *testing.T) {
	h := home().answer(portsMarker, "dnat table ip nat chain DOCKER: iifname != \"docker0\" tcp dport 443 counter packets 12 bytes 720 dnat to 172.17.0.2:80\n"+
		"dnat table ip nat chain PRE: tcp dport @web counter packets 3 bytes 180 dnat to 10.0.0.2\nend\n")
	_, err := hostadmit.Build(h, input(t, "watch", hostadmit.Flags{PublicPorts: true}))
	if err == nil {
		t.Fatal("admit took the ports a NAT rule holds")
	}
	for _, want := range []string{`a NAT rule in table ip nat chain DOCKER: iifname != "docker0" tcp dport 443 dnat to 172.17.0.2:80`, "in table ip nat chain PRE: tcp dport @web dnat to 10.0.0.2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not quote %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "counter packets") {
		t.Errorf("the refusal quotes counters:\n%v", err)
	}
}

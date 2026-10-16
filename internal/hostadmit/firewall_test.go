package hostadmit_test

import (
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
	goldenCase{name: "firewall-ufw-inactive-refused", site: "vm", host: func() *fakeHost { return home().with("ufw-active", "no") }},
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
	} {
		h := home().with("system-socket", "present").answer(portsMarker, answer+"end\n")
		_, err := hostadmit.Build(h, input(t, "home-a", gateway))
		if err == nil || !strings.Contains(err.Error(), "--public-ports cannot be granted") {
			t.Errorf("%s: %v", name, err)
		}
		if name == "root listener" && err != nil && !strings.Contains(err.Error(), "uid 0") {
			t.Errorf("a listener with no uid is not named root's: %v", err)
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

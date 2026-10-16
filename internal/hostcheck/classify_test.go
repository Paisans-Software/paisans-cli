package hostcheck_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

func check(t *testing.T, site string, h *fakeHost) *hostcheck.Report {
	t.Helper()
	r, err := hostcheck.Run(fixture(t), site, h)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// shown is what the report shows under the host check's step with
// --verbose, and the refusal it returns, which is where its conflicts are
// listed.
func shown(r *hostcheck.Report) (*ui.Recorder, string) {
	rec := &ui.Recorder{Verbose_: true}
	r.Show(rec)
	refusal := ""
	if err := r.Refusal(); err != nil {
		refusal = err.Error()
	}
	return rec, refusal
}

func printed(r *hostcheck.Report) string {
	rec, refusal := shown(r)
	return rec.Lines() + refusal
}

func wantClass(t *testing.T, r *hostcheck.Report, class hostcheck.Class) {
	t.Helper()
	if r.Class != class {
		t.Fatalf("class %s, want %s:\n%s", r.Class, class, printed(r))
	}
}

// wantLine is a line the operator is shown: one the host check marks with
// --verbose, as the recorder writes it, or, for a conflict, part of the
// refusal printed in full.
func wantLine(t *testing.T, r *hostcheck.Report, line string) {
	t.Helper()
	rec, refusal := shown(r)
	if !strings.Contains(rec.Lines(), line) && !strings.Contains(refusal, line) {
		t.Errorf("the report has no line %q:\n%s", line, printed(r))
	}
}

// An Ubuntu server with Docker installed and nothing else is clean: sshd and
// the stub resolver are root's, chrony and the DHCP client system users',
// all the base system, and Docker's own networks are Docker's.
func TestAnEmptyDockerInstallIsClean(t *testing.T) {
	r := check(t, "home-a", cleanHost())
	wantClass(t, r, hostcheck.Clean)
	if len(r.Foreign) != 0 || len(r.Conflicts) != 0 {
		t.Errorf("foreign %q, conflicts %v", r.Foreign, r.Conflicts)
	}
	if err := r.Refusal(); err != nil {
		t.Errorf("a clean host is refused: %v", err)
	}
	if r.Shared() {
		t.Error("a clean host reads as shared")
	}
	wantLine(t, r, "detail: home-a (ubuntu@host.example.org) is clean")
	wantLine(t, r, "done: docker 27.3.1")
}

// The report marks what the check found the way host prepare marks its
// plan: done for each finding, and done and not paisans for what another
// hand put on the host. It shows only with --verbose, under the host
// check's own line, and never with a padded label.
func TestShowMarksWhatTheCheckFound(t *testing.T) {
	r := check(t, "home-a", caddyHost())
	quiet := &ui.Recorder{}
	r.Show(quiet)
	if len(quiet.Events) != 0 {
		t.Errorf("the report shows without --verbose:\n%s", quiet.Lines())
	}

	var b strings.Builder
	r.Show(ui.NewPlain(&b, true))
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"      home-a (ubuntu@host.example.org) is shared\n",
		"  ok   docker: 27.3.1",
		"  ok   container web-caddy-1 (compose project web): not paisans\n",
		"  ok   shared: ufw's default policy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		for _, label := range []string{"docker ", "foreign ", "note ", "shared "} {
			if strings.HasPrefix(strings.TrimSpace(line), label) {
				t.Errorf("a padded label: %q", line)
			}
		}
	}
}

// A host already running this deployment is clean: the host network
// Postgres is ours through its owner, a user in the deployment's subordinate
// range, the published app port through the user's own port forwarder and
// the container's binding, and root's WireGuard socket through the
// psns-f2a9.conf the manifest records.
func TestOurOwnDeploymentIsClean(t *testing.T) {
	var appPort int
	for _, l := range claims(t, "home-a").Listeners {
		if l.Key == "apps.blog" {
			appPort = l.Port
		}
	}
	h := cleanHost()
	h.files["/srv/paisans/f2a9/.paisans-manifest.json"] = `{"version":1,"files":[{"path":"srv/paisans/f2a9/psns-f2a9.conf","sha256":"x","mode":"0600"}]}`
	h.answers["ip -o link"] += "4: psns-f2a9: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000\\    link/none\n"
	h.answers["ip -j route"] = `[{"dst":"default","dev":"eth0"},{"dst":"10.44.0.0/24","dev":"psns-f2a9"},{"dst":"172.18.0.0/16","dev":"br-` + id("5")[:12] + `"}]`
	h.answers["docker inspect"] = fmt.Sprintf(`{"id":%q,"name":"/paisans-f2a9-infra-patroni-1","pid":1500,"labels":{"com.docker.compose.project":"paisans-f2a9-infra","community.paisans.deployment":"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},"ports":{}}
{"id":%q,"name":"/paisans-f2a9-blog-app-1","pid":1700,"labels":{"com.docker.compose.project":"paisans-f2a9-blog","community.paisans.deployment":"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},"ports":{"8080/tcp":[{"HostIp":"10.44.0.1","HostPort":"%d"}]}}
`, infraID, talkID, appPort)
	h.answers["docker network inspect"] = defaultNetworks + fmt.Sprintf(`{"id":%q,"name":"paisans-f2a9-blog_default","labels":{"com.docker.compose.project":"paisans-f2a9-blog","community.paisans.deployment":"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},"ipam":[{"Subnet":"172.18.0.0/16"}]}
`, id("5"))
	h.answers["docker volume inspect"] = `{"name":"` + id("9") + `","labels":{"com.docker.volume.anonymous":""}}
`
	h.answers["ss -Hltnue"] = baseSockets + fmt.Sprintf(`tcp LISTEN 0 4096 10.44.0.1:5432 0.0.0.0:* uid:166534 ino:1 sk:20 <->
tcp LISTEN 0 4096 127.0.0.1:5432 0.0.0.0:* uid:166534 ino:2 sk:21 <->
udp UNCONN 0 0 0.0.0.0:51820 0.0.0.0:* ino:0 sk:22 <->
tcp LISTEN 0 4096 10.44.0.1:%d 0.0.0.0:* uid:1001 ino:3 sk:23 <->
`, appPort)
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Clean)
}

// Another paisans deployment on the same host is not this one's, though its
// names start paisans- too: the deployment label decides, so its container
// and network make the host shared, and a container with a paisans- name and
// no label at all is anybody's.
func TestAnotherDeploymentOnTheHostIsForeign(t *testing.T) {
	h := cleanHost()
	h.answers["docker inspect"] = fmt.Sprintf(`{"id":%q,"name":"/paisans-0c1d-talk-app-1","pid":0,"labels":{"com.docker.compose.project":"paisans-0c1d-talk","community.paisans.deployment":"0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"},"ports":{}}
{"id":%q,"name":"/paisans-talk-app-1","pid":0,"labels":{"com.docker.compose.project":"paisans-talk"},"ports":{}}
`, infraID, talkID)
	h.answers["docker network inspect"] = defaultNetworks + fmt.Sprintf(`{"id":%q,"name":"paisans-0c1d-talk_default","labels":{"com.docker.compose.project":"paisans-0c1d-talk","community.paisans.deployment":"0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"},"ipam":[{"Subnet":"172.19.0.0/16"}]}
`, id("6"))
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Shared)
	for _, line := range []string{
		"done: container paisans-0c1d-talk-app-1 (compose project paisans-0c1d-talk, paisans deployment 0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5) not paisans",
		"done: container paisans-talk-app-1 (compose project paisans-talk) not paisans",
		"done: docker network paisans-0c1d-talk_default (compose project paisans-0c1d-talk, paisans deployment 0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5) not paisans",
	} {
		wantLine(t, r, line)
	}
}

// Another user's Caddy on 80 and 443 beside a site that claims neither makes
// the host shared, which is allowed.
func TestAForeignCaddyBesideASiteThatDoesNotClaimItIsShared(t *testing.T) {
	r := check(t, "home-a", caddyHost())
	wantClass(t, r, hostcheck.Shared)
	if !r.Shared() {
		t.Error("Shared() is false")
	}
	for _, line := range []string{
		"done: container web-caddy-1 (compose project web) not paisans",
		"done: container shop-app-1 (compose project shop) not paisans",
		"done: *:80/tcp held by a listener of another user's (uid 1002) not paisans",
		"done: docker network shop_default (compose project shop) not paisans",
		"done: volume shop_data (compose project shop) not paisans",
		"done: shared ufw's default policy",
	} {
		wantLine(t, r, line)
	}
	if err := r.Refusal(); err != nil {
		t.Errorf("a shared host is refused: %v", err)
	}
}

// The same Caddy against a gateway is a conflict on both ports, each line
// naming the claim, the key and the holder, and nothing proceeds.
func TestAForeignCaddyConflictsWithAGateway(t *testing.T) {
	r := check(t, "edge", caddyHost())
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "*:80/tcp (Caddy): claimed by sites.edge.roles (gateway), held by a listener of another user's (uid 1002)")
	wantLine(t, r, "*:443/tcp (Caddy): claimed by sites.edge.roles (gateway), held by a listener of another user's (uid 1002)")
	if len(r.Conflicts) != 2 {
		t.Errorf("conflicts %v, want 80 and 443 once each", r.Conflicts)
	}
	err := r.Refusal()
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("refusal %v", err)
	}
}

// A Postgres another user runs on the host's 5432 conflicts with a data
// site, on the mesh address and on loopback, which the toolkit's binds both
// need, and makes the host shared besides.
func TestAForeignPostgresConflictsWithADataSite(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = baseSockets + `tcp LISTEN 0 244 0.0.0.0:5432 0.0.0.0:* uid:1003 ino:1 sk:20 <->
`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "10.44.0.1:5432/tcp (Postgres): claimed by sites.home-a.roles (data), held by a listener of another user's (uid 1003)")
	wantLine(t, r, "127.0.0.1:5432/tcp (Postgres): claimed by sites.home-a.roles (data), held by a listener of another user's (uid 1003)")
	if len(r.Foreign) != 1 {
		t.Errorf("foreign %q, want the Postgres", r.Foreign)
	}
}

// A system user's listener, as a distribution's own Postgres runs, is the
// base system's: it does not make a host shared, and on a claim it is still a
// conflict.
func TestASystemUsersListenerIsTheBaseSystems(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = baseSockets + `tcp LISTEN 0 244 0.0.0.0:5432 0.0.0.0:* uid:113 ino:1 sk:20 <->
`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "10.44.0.1:5432/tcp (Postgres): claimed by sites.home-a.roles (data), held by a listener of a system user's (uid 113)")
	if len(r.Foreign) != 0 {
		t.Errorf("a system user's listener was counted as foreign: %q", r.Foreign)
	}
	wantClass(t, check(t, "edge", h), hostcheck.Clean)
}

// A loopback listener does not make a host shared, but one on an address
// and port a site binds still stops that bind.
func TestALoopbackListenerOnAClaimIsStillAConflict(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = baseSockets + `tcp LISTEN 0 244 127.0.0.1:5432 0.0.0.0:* uid:1003 ino:1 sk:20 <->
`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	if len(r.Conflicts) != 1 || !strings.HasPrefix(r.Conflicts[0].Resource, "127.0.0.1:5432/tcp") {
		t.Errorf("conflicts %v, want the loopback bind alone", r.Conflicts)
	}
	if len(r.Foreign) != 0 {
		t.Errorf("a loopback listener was counted as foreign: %q", r.Foreign)
	}
}

// A port a foreign container publishes conflicts even with no listener:
// Docker without its userland proxy has none, and a stopped container binds
// again when it starts.
func TestAForeignPublishedPortConflictsWithoutAListener(t *testing.T) {
	h := cleanHost()
	h.answers["docker inspect"] = fmt.Sprintf(`{"id":%q,"name":"/proxy-nginx-1","pid":0,"labels":{"com.docker.compose.project":"proxy"},"ports":{"443/tcp":[{"HostIp":"","HostPort":"443"}]}}
`, shopID)
	r := check(t, "edge", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "*:443/tcp (Caddy): claimed by sites.edge.roles (gateway), held by container proxy-nginx-1 (compose project proxy)")
}

// A psns-f2a9 the toolkit's manifest does not record is somebody else's VPN, and
// so is the udp socket on 51820 that comes with it.
func TestAWireGuardTheToolkitDidNotWriteConflicts(t *testing.T) {
	h := cleanHost()
	h.answers["ip -o link"] += "4: psns-f2a9: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420\\    link/none\n"
	h.answers["ss -Hltnue"] = baseSockets + "udp UNCONN 0 0 0.0.0.0:51820 0.0.0.0:* ino:0 sk:20 <->\n"
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "interface psns-f2a9: claimed by mesh, held by an interface this deployment did not write")
	wantLine(t, r, "*:51820/udp (WireGuard): claimed by mesh, held by a listener of root's")
}

// A foreign Docker network or a route over the mesh subnet would capture
// mesh traffic, or have its own captured.
func TestSomethingOverlappingTheMeshConflicts(t *testing.T) {
	h := cleanHost()
	h.answers["docker network inspect"] = defaultNetworks + fmt.Sprintf(`{"id":%q,"name":"lab_default","labels":{"com.docker.compose.project":"lab"},"ipam":[{"Subnet":"10.44.0.0/16"}]}
`, id("6"))
	h.answers["ip -j route"] = `[{"dst":"default","dev":"eth0"},{"dst":"10.44.0.0/16","dev":"br-` + id("6")[:12] + `"},{"dst":"10.44.0.128/25","dev":"tun0"}]`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "subnet 10.44.0.0/16: claimed by mesh.subnet, held by docker network lab_default (compose project lab)")
	wantLine(t, r, "route 10.44.0.128/25 dev tun0: claimed by mesh.subnet, held by the host's routing table")
	if len(r.Conflicts) != 2 {
		t.Errorf("conflicts %v: the network's own bridge route is the network, reported once", r.Conflicts)
	}
}

// A route broader than the mesh, such as a provider's private network
// (10.0.0.0/8 via its gateway), is less specific than the route psns-f2a9 adds,
// so the mesh still wins and nothing is captured. It is noted, not refused.
// A route equal to the mesh or inside it would capture mesh traffic, and
// conflicts.
func TestABroaderRouteIsANoteNotAConflict(t *testing.T) {
	h := cleanHost()
	h.answers["ip -j route"] = `[{"dst":"default","dev":"eth0"},{"dst":"10.0.0.0/8","gateway":"10.0.0.1","dev":"enp7s0"}]`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Clean)
	wantLine(t, r, "done: route 10.0.0.0/8 dev enp7s0 contains the mesh subnet 10.44.0.0/24")

	h.answers["ip -j route"] = `[{"dst":"10.44.0.0/24","dev":"tun0"}]`
	wantClass(t, check(t, "home-a", h), hostcheck.Conflicted)
}

// Routes are read from every table, so a VPN that keeps its routes in one
// of its own, Tailscale's 52, is seen and named with its table. The local
// table's own addresses and a table's throw routes hold no network.
func TestARouteInAnotherTableConflicts(t *testing.T) {
	h := cleanHost()
	delete(h.answers, "ip -j route")
	h.answers["ip -j route show table all"] = `[{"dst":"default","dev":"eth0"},{"type":"throw","dst":"10.44.0.0/16","table":"52"},{"type":"local","dst":"10.44.0.9","dev":"eth1","table":"local"}]`
	wantClass(t, check(t, "home-a", h), hostcheck.Clean)

	h.answers["ip -j route show table all"] = `[{"dst":"10.44.0.0/24","dev":"tailscale0","table":"52"}]`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	if !strings.Contains(fmt.Sprint(r.Conflicts), "route 10.44.0.0/24 dev tailscale0 table 52") {
		t.Errorf("the conflict does not name the table: %+v", r.Conflicts)
	}
}

// An IPv4-mapped address is the IPv4 address, so a listener ss prints as
// [::ffff:127.0.0.1] holds 127.0.0.1 and conflicts with a claim there.
func TestAnIPv4MappedListenerIsItsIPv4Address(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = baseSockets + `tcp LISTEN 0 244 [::ffff:127.0.0.1]:5432 *:* uid:1003 ino:1 sk:20 <->
`
	r := check(t, "home-a", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "127.0.0.1:5432/tcp (Postgres): claimed by sites.home-a.roles (data), held by a listener of another user's (uid 1003)")
	if len(r.Foreign) != 0 {
		t.Errorf("a mapped loopback listener was counted as foreign: %q", r.Foreign)
	}
}

// Another user's Caddy on 80 and 443 is no conflict for a monitor
// behind the operator's own web server, which claims neither: it may well be
// that web server. For a monitor running the toolkit's Caddy it is a conflict
// on both ports.
func TestAForeignCaddyAndTheTwoWebServerModes(t *testing.T) {
	wantClass(t, check(t, "porch", caddyHost()), hostcheck.Shared)
	r := check(t, "watch", caddyHost())
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "*:80/tcp (Caddy): claimed by sites.watch.roles (monitor), held by a listener of another user's (uid 1002)")
	wantLine(t, r, "*:443/tcp (Caddy): claimed by sites.watch.roles (monitor), held by a listener of another user's (uid 1002)")
}

// An external monitor's compose network is pinned, so a foreign network or a
// route over it is a conflict named after the web_server block, as one over the
// mesh is.
func TestSomethingOverlappingTheWebServerNetworkConflicts(t *testing.T) {
	h := cleanHost()
	h.answers["docker network inspect"] = defaultNetworks + fmt.Sprintf(`{"id":%q,"name":"lab_default","labels":{"com.docker.compose.project":"lab"},"ipam":[{"Subnet":"10.255.255.0/24"}]}
`, id("6"))
	h.answers["ip -j route"] = `[{"dst":"default","dev":"eth0"},{"dst":"10.255.255.0/24","dev":"br-` + id("6")[:12] + `"},{"dst":"10.255.255.0/29","dev":"tun0"}]`
	r := check(t, "porch", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "subnet 10.255.255.0/24: claimed by sites.porch.web_server, held by docker network lab_default (compose project lab)")
	wantLine(t, r, "route 10.255.255.0/29 dev tun0: claimed by sites.porch.web_server, held by the host's routing table")
	// The same network on a site with no external monitor is nobody's claim.
	wantClass(t, check(t, "watch", h), hostcheck.Shared)
}

// The pinned network belongs to one compose project, paisans-<token>-<app>. Another
// of the toolkit's own projects holding it, such as the stack an app left
// behind when it was renamed, makes compose refuse the new one mid apply
// ("Pool overlaps"), so it is a conflict naming the stale stack. The app's
// own network holding it is what an apply leaves, and is clean.
func TestAStaleToolkitStackOnTheWebServerNetworkConflicts(t *testing.T) {
	stale := cleanHost()
	stale.answers["docker network inspect"] = defaultNetworks + fmt.Sprintf(`{"id":%q,"name":"paisans-f2a9-health_default","labels":{"com.docker.compose.project":"paisans-f2a9-health","community.paisans.deployment":"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},"ipam":[{"Subnet":"10.255.255.0/29"}]}
`, id("7"))
	r := check(t, "porch", stale)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "subnet 10.255.255.0/29: claimed by sites.porch.web_server, held by docker network paisans-f2a9-health_default (compose project paisans-f2a9-health)")
	wantLine(t, r, "docker compose -p paisans-f2a9-health down")

	own := cleanHost()
	own.answers["docker network inspect"] = defaultNetworks + fmt.Sprintf(`{"id":%q,"name":"paisans-f2a9-porch-status_default","labels":{"com.docker.compose.project":"paisans-f2a9-porch-health","community.paisans.deployment":"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"},"ipam":[{"Subnet":"10.255.255.0/29"}]}
`, id("8"))
	wantClass(t, check(t, "porch", own), hostcheck.Clean)
}

// The deployment's own listeners, its user's UID or one in its subordinate
// range, are ours on a re-apply; root's and another user's are taken.
func TestTheUsersOwnListenersAreOurs(t *testing.T) {
	inv := &hostcheck.Inventory{Host: "vm", Self: hostcheck.Self{UID: 1001, SubStart: 165536, SubCount: 65536}, Sockets: []hostcheck.Socket{
		{Proto: "tcp", Port: 80, UID: 1001},
		{Proto: "tcp", Port: 443, UID: 165536 + 70},
	}}
	if r := hostcheck.Classify(gatewayClaims(t), inv); r.Refusal() != nil {
		t.Errorf("the deployment's own Caddy refused: %v", r.Refusal())
	}
	for _, uid := range []int{0, 1002, 165536 + 65536} {
		inv.Sockets = []hostcheck.Socket{{Proto: "tcp", Port: 443, UID: uid}}
		r := hostcheck.Classify(gatewayClaims(t), inv)
		if r.Refusal() == nil || !strings.Contains(r.Refusal().Error(), "443") {
			t.Errorf("uid %d on 443 was not refused: %v", uid, r.Refusal())
		}
	}
}

// gatewayClaims are the claims of the fixture's gateway site.
func gatewayClaims(t *testing.T) hostcheck.Claims { return claims(t, "edge") }

// Another deployment's Caddy reads as that deployment's container, as any of
// its containers does: its root and the host's registry are not this user's
// to read.
func TestAnotherDeploymentsCaddyKeepsThePlainWording(t *testing.T) {
	h := cleanHost()
	h.answers["docker inspect"] = fmt.Sprintf(`{"id":%q,"name":"/paisans-566c-infra-caddy-1","pid":812,"labels":{"com.docker.compose.project":"paisans-566c-infra","com.docker.compose.service":"caddy","community.paisans.deployment":"566c1a2b-3c4d-4e5f-8a6b-7c8d9e0f1a2b"},"ports":{"443/tcp":[{"HostIp":"","HostPort":"443"}]}}
`, caddyID)
	r := check(t, "edge", h)
	wantClass(t, r, hostcheck.Conflicted)
	wantLine(t, r, "*:443/tcp (Caddy): claimed by sites.edge.roles (gateway), held by container paisans-566c-infra-caddy-1 (compose project paisans-566c-infra, paisans deployment 566c1a2b-3c4d-4e5f-8a6b-7c8d9e0f1a2b)")
}

// A conflict refuses with a short hint, one line per conflict, and what to
// do; printed, each conflict is its own indented line.
func TestAConflictRefusalIsAProblem(t *testing.T) {
	r := check(t, "edge", caddyHost())
	err := r.Refusal()
	var p *ui.Problem
	if !errors.As(err, &p) {
		t.Fatalf("not a ui.Problem: %v", err)
	}
	if p.Hint != r.Host+" holds 2 things edge claims, so nothing was changed" {
		t.Errorf("hint: %q", p.Hint)
	}
	lines := strings.Split(p.Explain, "\n")
	if len(lines) != 3 || lines[0] != "- "+r.Conflicts[0].String() || lines[1] != "- "+r.Conflicts[1].String() || !strings.HasPrefix(lines[2], "Move what holds each one") {
		t.Errorf("explanation:\n%s", p.Explain)
	}
	var b strings.Builder
	ui.PrintError(&b, err, false)
	out := b.String()
	if !strings.HasPrefix(out, "FAIL "+p.Hint+"\n  - *:80/tcp (Caddy): claimed by sites.edge.roles (gateway), held by a listener of another user's (uid 1002)\n  - *:443/tcp") {
		t.Errorf("printed:\n%s", out)
	}
}

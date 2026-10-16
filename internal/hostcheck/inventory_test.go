package hostcheck_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

func inspect(t *testing.T, h *fakeHost) *hostcheck.Inventory {
	t.Helper()
	inv, err := hostcheck.Inspect(h, fixtureDeployment)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// Every fact the spec lists is read, from one probe each.
func TestInspectReadsEveryFact(t *testing.T) {
	h := caddyHost()
	inv := inspect(t, h)
	if inv.Host != "ubuntu@host.example.org" {
		t.Errorf("host %q", inv.Host)
	}
	if !inv.Docker.Present || inv.Docker.Version != "27.3.1" || strings.Join(inv.Docker.Packages, ",") != "docker-ce" {
		t.Errorf("docker %+v", inv.Docker)
	}
	if len(inv.Containers) != 2 {
		t.Fatalf("containers %+v", inv.Containers)
	}
	if c := inv.Containers[0]; c.ID != caddyID || c.Name != "web-caddy-1" || c.Project != "web" || c.PID != 812 || len(c.Bindings) != 0 {
		t.Errorf("caddy %+v", c)
	}
	if b := inv.Containers[1].Bindings; len(b) != 1 || b[0].String() != "203.0.113.10:8081/tcp" || b[0].Owner != "shop-app-1" {
		t.Errorf("shop's bindings %+v", b)
	}
	if !reflect.DeepEqual(inv.Volumes, []hostcheck.Volume{{Name: "shop_data", Project: "shop"}}) {
		t.Errorf("volumes %+v", inv.Volumes)
	}
	if len(inv.Networks) != 4 || inv.Networks[3].Name != "shop_default" || inv.Networks[3].Project != "shop" || !reflect.DeepEqual(inv.Networks[3].Subnets, []string{"192.0.2.0/24"}) {
		t.Errorf("networks %+v", inv.Networks)
	}
	if !reflect.DeepEqual(inv.Links, []string{"lo", "eth0", "docker0"}) {
		t.Errorf("links %v", inv.Links)
	}
	if len(inv.Routes) != 3 || inv.Routes[2] != (hostcheck.Route{Dst: "172.17.0.0/16", Dev: "docker0"}) {
		t.Errorf("routes %+v", inv.Routes)
	}
	if inv.Manifest || inv.ManifestWireGuard {
		t.Errorf("a host with no manifest read as having one")
	}
	found := false
	for _, s := range inv.Sockets {
		if s == (hostcheck.Socket{Proto: "tcp", Address: "", Port: 80, UID: 1002}) {
			found = true
		}
	}
	if !found {
		t.Errorf("caddy's *:80 was not read: %+v", inv.Sockets)
	}
	for _, c := range h.ran {
		for _, change := range []string{" rm ", "ufw default", "ufw --force", "ufw enable", "systemctl start", "systemctl stop"} {
			if strings.Contains(c, change) {
				t.Errorf("the inventory changed something: %s", c)
			}
		}
	}
}

// ss prints every form of "any address" and an interface scope; each is
// read as render.Listener has it, with "" for any address.
func TestSocketsAreReadAsListeners(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = `tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* ino:1 sk:1 <->
tcp LISTEN 0 4096 [::]:5432 [::]:* uid:113 ino:2 sk:2 v6only:1 <->
tcp LISTEN 0 4096 *:80 *:* uid:1001 ino:3 sk:3 <->
udp UNCONN 0 0 127.0.0.53%lo:53 0.0.0.0:* ino:4 sk:4 <->
udp UNCONN 0 0 0.0.0.0:51820 0.0.0.0:* ino:5 sk:5 <->
tcp LISTEN 0 4096 [::1]:2379 [::]:* uid:165600 ino:6 sk:6 <->
tcp LISTEN 0 4096 [::ffff:10.44.0.1]:8080 *:* uid:1001 ino:7 sk:7 <->
tcp LISTEN 0 4096 [::ffff:0.0.0.0]:8081 *:* uid:1001 ino:8 sk:8 <->
`
	var got []string
	for _, s := range inspect(t, h).Sockets {
		got = append(got, fmt.Sprintf("%s %d", s.Listener(), s.UID))
	}
	want := []string{"*:22/tcp 0", "*:5432/tcp 113", "*:80/tcp 1001", "127.0.0.53:53/udp 0", "*:51820/udp 0", "::1:2379/tcp 165600", "10.44.0.1:8080/tcp 1001", "*:8081/tcp 1001"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The manifest is how a psns-f2a9 the toolkit wrote is told from one it did not.
func TestTheManifestSaysWhetherWireGuardIsOurs(t *testing.T) {
	h := cleanHost()
	h.files["/srv/paisans/f2a9/.paisans-manifest.json"] = `{"version":1,"files":[{"path":"srv/paisans/f2a9/psns-f2a9.conf","sha256":"x","mode":"0600"}]}`
	inv := inspect(t, h)
	if !inv.Manifest || !inv.ManifestWireGuard {
		t.Errorf("manifest %v, wireguard %v", inv.Manifest, inv.ManifestWireGuard)
	}
	h.files["/srv/paisans/f2a9/.paisans-manifest.json"] = `{"version":1,"files":[{"path":"srv/paisans/f2a9/infra/compose.yaml","sha256":"x","mode":"0644"}]}`
	if inv := inspect(t, h); !inv.Manifest || inv.ManifestWireGuard {
		t.Errorf("manifest %v, wireguard %v", inv.Manifest, inv.ManifestWireGuard)
	}
}

// Where Docker came from is reported: Docker's own package, Ubuntu's, or
// the snap.
func TestDockersPackageIsRead(t *testing.T) {
	h := cleanHost()
	h.answers["dpkg-query"] = "docker.io install ok installed\nName    Version  Rev   Tracking       Publisher   Notes\ndocker  27.2.0   2963  latest/stable  canonical✓  -\n"
	if got := strings.Join(inspect(t, h).Docker.Packages, ","); got != "docker.io,snap" {
		t.Errorf("packages %q", got)
	}
}

// A host without Docker has no containers to look for, and is not asked.
func TestNoDockerSkipsTheContainerProbes(t *testing.T) {
	h := cleanHost()
	h.answers["docker version"] = "absent\n"
	if inv := inspect(t, h); inv.Docker.Present {
		t.Error("docker read as present")
	}
	for _, c := range h.ran {
		if strings.Contains(c, "xargs") {
			t.Errorf("probed a Docker that is not there: %s", c)
		}
	}
}

// Docker installed and not answering is "could not look", which is not
// "looked and found nothing".
func TestADockerThatDoesNotAnswerIsAnError(t *testing.T) {
	h := cleanHost()
	h.fail = map[string]error{"docker version": errors.New("exit status 1")}
	if _, err := hostcheck.Inspect(h, fixtureDeployment); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("got %v", err)
	}
}

// Any probe that fails stops the inventory with what it was reading.
func TestAFailedProbeIsAnError(t *testing.T) {
	for _, probe := range []string{`echo "uid`, "ss -Hltnue", "ip -j route", "docker inspect"} {
		h := cleanHost()
		h.fail = map[string]error{probe: errors.New("exit status 1")}
		if _, err := hostcheck.Inspect(h, fixtureDeployment); err == nil {
			t.Errorf("%s failed and the inventory went on", probe)
		}
	}
}

// Every probe runs in the C locale: dpkg and ss translate
// what they print, and the parsers read the English words.
func TestEveryProbeRunsInTheCLocale(t *testing.T) {
	h := caddyHost()
	inspect(t, h)
	for _, c := range h.ran {
		if !strings.HasPrefix(c, "export LC_ALL=C; ") {
			t.Errorf("a probe runs in the host's locale: %s", c)
		}
	}
}

// The inventory runs as the deployment's user: no probe asks for root, and
// none reads what only root may (ufw, firewalld, the registry, other users'
// process tables).
func TestTheInventoryRunsAsTheDeploymentsUser(t *testing.T) {
	h := cleanHost()
	inv, err := hostcheck.Inspect(h, fixtureDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Self != (hostcheck.Self{UID: 1001, SubStart: 165536, SubCount: 65536}) {
		t.Errorf("self %+v", inv.Self)
	}
	for _, c := range h.ran {
		for _, root := range []string{"ufw", "firewalld", "/proc/", "ss -Hltnup", "registry.json", "sudo"} {
			if strings.Contains(c, root) {
				t.Errorf("a probe reads %q, which needs root: %s", root, c)
			}
		}
	}
}

// ss -e prints uid:N for a socket of any user but root.
func TestSocketsCarryTheirOwner(t *testing.T) {
	h := cleanHost()
	h.answers["ss -Hltnue"] = "tcp LISTEN 0 4096 0.0.0.0:443 0.0.0.0:* uid:165536 ino:2 sk:1\ntcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* ino:3 sk:2\n"
	inv, err := hostcheck.Inspect(h, fixtureDeployment)
	if err != nil {
		t.Fatal(err)
	}
	want := []hostcheck.Socket{{Proto: "tcp", Port: 443, UID: 165536}, {Proto: "tcp", Port: 22, UID: 0}}
	if !reflect.DeepEqual(inv.Sockets, want) {
		t.Errorf("sockets %+v", inv.Sockets)
	}
}

// A self probe that names no uid is an error: read as uid 0, it would make
// every listener of root's this deployment's.
func TestASelfProbeWithoutAUIDIsAnError(t *testing.T) {
	h := cleanHost()
	h.answers[`echo "uid`] = ""
	if _, err := hostcheck.Inspect(h, fixtureDeployment); err == nil || !strings.Contains(err.Error(), "who the probes run as") {
		t.Fatalf("got %v", err)
	}
}

// A user with no subordinate range is still read, its range empty.
func TestAUserWithoutASubordinateRange(t *testing.T) {
	h := cleanHost()
	h.answers[`echo "uid`] = "uid 1001\nsubuid \n"
	if self := inspect(t, h).Self; self != (hostcheck.Self{UID: 1001}) {
		t.Errorf("self %+v", self)
	}
}

// A container, volume or network removed between the listing and its
// inspect is gone, not an error: its inspect's complaint is discarded and
// what did answer is read.
func TestTheProbesTolerateWhatVanishesMidway(t *testing.T) {
	h := cleanHost()
	inspect(t, h)
	for _, c := range h.ran {
		if strings.Contains(c, "xargs -r docker") && !strings.HasSuffix(c, "2>/dev/null || true") {
			t.Errorf("an inspect fails the gate when something vanishes: %s", c)
		}
	}
}

// fixtureDeployment is the example configuration's deployment.
var fixtureDeployment = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

// A container's compose service and networks, the manifest's files and the
// host's own Caddy site blocks are read for the ownership report.
func TestInspectReadsWhatTheOwnershipReportNeeds(t *testing.T) {
	h := cleanHost()
	h.answers["docker inspect"] = fmt.Sprintf(`{"id":%q,"name":"/blog-ghost-1","pid":913,"labels":{"com.docker.compose.project":"blog","com.docker.compose.service":"ghost"},"networks":{"web":{},"blog_default":{}},"ports":{}}
`, shopID)
	h.answers["/srv/paisans/f2a9/caddy.d'"] = "/srv/paisans/f2a9/caddy.d/blog.caddy\n/srv/paisans/f2a9/caddy.d/a.caddy\n"
	h.files["/srv/paisans/f2a9/.paisans-manifest.json"] = `{"version":1,"files":[{"path":"srv/paisans/f2a9/docs/.env","sha256":"x","mode":"0600","leftover":true,"leftover_since":"2026-10-01T00:00:00Z"}]}`
	inv := inspect(t, h)
	if c := inv.Containers[0]; c.Service != "ghost" || !reflect.DeepEqual(c.Networks, []string{"blog_default", "web"}) {
		t.Errorf("container %+v", c)
	}
	if !reflect.DeepEqual(inv.HostSites, []string{"/srv/paisans/f2a9/caddy.d/a.caddy", "/srv/paisans/f2a9/caddy.d/blog.caddy"}) {
		t.Errorf("host sites %v", inv.HostSites)
	}
	if len(inv.ManifestFiles) != 1 || !inv.ManifestFiles[0].Leftover || inv.ManifestFiles[0].LeftoverSince != "2026-10-01T00:00:00Z" {
		t.Errorf("manifest files %+v", inv.ManifestFiles)
	}
}

func TestContainerCarriesItsImageID(t *testing.T) {
	h := caddyHost()
	h.answers["docker inspect"] = `{"id":"abc","name":"/paisans-f2a9-talk-app-1","image":"sha256:1111","pid":0,"labels":{},"networks":{},"ports":{}}` + "\n"
	inv := inspect(t, h)
	if len(inv.Containers) != 1 || inv.Containers[0].Image != "sha256:1111" {
		t.Fatalf("got %+v", inv.Containers)
	}
}

func TestTheContainerProbeAsksForTheImage(t *testing.T) {
	h := caddyHost()
	inspect(t, h)
	if !strings.Contains(strings.Join(h.ran, "\n"), `"image":{{json .Image}}`) {
		t.Error("the container probe does not ask docker inspect for .Image")
	}
}

// The sites a manifest says were pending are read with it.
func TestTheInventoryReadsTheManifestsPendingSites(t *testing.T) {
	h := cleanHost()
	h.files["/srv/paisans/f2a9/.paisans-manifest.json"] = `{"version":1,"files":[],"pending":["home-c"]}`
	inv := inspect(t, h)
	if strings.Join(inv.ManifestPending, ",") != "home-c" {
		t.Errorf("pending %v", inv.ManifestPending)
	}
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/doctor"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/patroni"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// doctorHost answers doctor's read commands from a table, records every
// command it is sent, and refuses every write.
type doctorHost struct {
	name    string
	down    bool
	answers map[string]string
	files   map[string]string
	sent    *[]string
	writes  *[]string
}

func (h doctorHost) Describe() string { return "ubuntu@" + h.name }
func (h doctorHost) Run(command string) (string, error) {
	*h.sent = append(*h.sent, h.name+": "+command)
	if h.down {
		return "ssh: connect to host " + h.name + " port 22: Operation timed out\n",
			fmt.Errorf("%w after 3 attempts: exit status 255", apply.ErrUnreachable)
	}
	for prefix, out := range h.answers {
		if strings.HasPrefix(command, prefix) {
			return out, nil
		}
	}
	return "", fmt.Errorf("unexpected command %q", command)
}
func (h doctorHost) RunInput(command, _ string) (string, error) {
	*h.writes = append(*h.writes, "RunInput "+command)
	return "", errors.New("doctor sends no stdin")
}
func (h doctorHost) ReadFile(path string) (string, bool, error) {
	*h.sent = append(*h.sent, h.name+": read "+path)
	if h.down {
		return "", false, fmt.Errorf("%w after 3 attempts: exit status 255", apply.ErrUnreachable)
	}
	content, ok := h.files[path]
	return content, ok, nil
}
func (h doctorHost) WriteFile(path string, _ string, _ uint32) error {
	*h.writes = append(*h.writes, "WriteFile "+path)
	return errors.New("doctor writes nothing")
}

// readOnly is every command doctor may send, exactly or by prefix. Anything
// else, and above all a patronictl failover, fails the test.
func readOnly(cfg *config.Config) (exact []string, prefixes []string) {
	exact = []string{
		doctor.ReachCommand,
		doctor.ClockCommand,
		doctor.EtcdHealthCommand(cfg),
		doctor.EtcdVersionCommand,
		doctor.SyncCommand(cfg.Deployment()),
		doctor.PatroniLogCommand(cfg.Deployment()),
		doctor.ContainersCommand(cfg.Deployment()),
		mesh.LinkCommand(cfg.Deployment().Interface()),
		mesh.RouteCommand,
		mesh.AddrCommand,
		mesh.NetworksCommand,
		mesh.UserDaemonCommand,
		patroni.ClusterCommand(cfg.Deployment(), "10.44.0.1:8008"),
		patroni.ClusterCommand(cfg.Deployment(), "10.44.0.2:8008"),
		doctor.CaddyLogCommand(cfg.Deployment()),
		doctor.UserCommand,
		doctor.DockerCommand,
		doctor.LingerCommand,
		doctor.SudoCommand,
	}
	prefixes = []string{
		"docker inspect --format '{\"State\":{{json .State}},\"RestartCount\":{{.RestartCount}}}' ",
		"docker logs --tail 20 ",
		"if [ ! -f '/srv/paisans/f2a9/auth/compose.yaml' ]; then echo absent; ",
	}
	exact = append(exact, hostcheck.ReadCommands(cfg.Deployment())...)
	exact = append(exact, "read "+cfg.Deployment().Manifest(), "read "+admittedPath(cfg))
	// Each gated app's settings read, which blueprints.LiveSettings promises
	// prints only the named settings.
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		live, ok := blueprints.LiveSettingsFor(app.Blueprint)
		var names []string
		for _, s := range blueprints.GateSettingsFor(app) {
			names = append(names, s.Name)
		}
		if ok && len(names) > 0 {
			exact = append(exact, doctor.SettingsCommand(cfg.Deployment(), name, live.Service(), live.ReadCommand(names)))
		}
	}
	return exact, prefixes
}

// sudoHelperOnly is sudo -l's answer for a user host admit gave the helper
// and nothing else.
func sudoHelperOnly(user, host, helper string) string {
	return "Matching Defaults entries for " + user + " on " + host + ":\n" +
		"    env_reset, mail_badpass, use_pty\n\n" +
		"User " + user + " may run the following commands on " + host + ":\n" +
		"    " + admitrules.HelperRule(helper) + "\n"
}

// admitHosts makes each fake host one whose owner admitted its site as the
// configuration declares it, with a sound rootless setup, unless the test
// gave it another answer already: the record, a user in no privileged
// group, its own rootless Docker 29.9.1, linger on with the manager after
// the mesh, and sudo for the WireGuard helper alone.
func admitHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost) {
	t.Helper()
	helper, err := apply.WGHelper{Iface: cfg.Deployment().Interface()}.Path()
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range cfg.SiteNames() {
		s := cfg.Sites[site]
		h, ok := hosts[s.SSHHost()]
		if !ok {
			continue
		}
		if h.files == nil {
			h.files = map[string]string{}
		}
		if _, given := h.files[admittedPath(cfg)]; !given {
			h.files[admittedPath(cfg)] = admittedRecord(t, cfg, site)
		}
		if h.answers == nil {
			h.answers = map[string]string{}
		}
		for command, answer := range map[string]string{
			doctor.UserCommand:   "uid 1000\nuser " + s.SSHUser() + "\ngroups " + s.SSHUser() + "\n",
			doctor.DockerCommand: "host unix:///run/user/1000/docker.sock\nengine 29.9.1\nsecurity [\"name=seccomp,profile=builtin\",\"name=rootless\",\"name=cgroupns\"]\n",
			doctor.LingerCommand: "linger yes\nmesh-order yes\n",
			doctor.SudoCommand:   sudoHelperOnly(s.SSHUser(), site, helper),
		} {
			if _, given := h.answers[command]; !given {
				h.answers[command] = answer
			}
		}
		hosts[s.SSHHost()] = h
	}
}

func withDoctorHosts(t *testing.T, cfg *config.Config, hosts map[string]doctorHost) {
	t.Helper()
	admitHosts(t, cfg, hosts)
	saved, savedNow, savedAtOnce := doctorTransport, doctorNow, doctorAtOnce
	// One check at a time, in the graph's order, so the hosts' shared
	// record of what they were sent is written by one goroutine.
	doctorAtOnce = 1
	doctorTransport = func(tr apply.SSHTransport) apply.Transport {
		h, ok := hosts[tr.Host]
		if !ok {
			t.Fatalf("doctor reached an unexpected host %q", tr.Host)
		}
		return h
	}
	doctorNow = func() time.Time { return time.Unix(1_760_000_000, 0) }
	t.Cleanup(func() { doctorTransport, doctorNow, doctorAtOnce = saved, savedNow, savedAtOnce })
}

// quietCaddyLog is a caddy container with no certificate line in its log.
const quietCaddyLog = "now 1760000000\nstarted 2025-10-09T08:00:00Z\n"

// stuckHosts are the four sites of the stuck case: home-b led and is down,
// home-a is a replica /sync no longer names, and vm overlaps the mesh.
func stuckHosts(cfg *config.Config, sent, writes *[]string) map[string]doctorHost {
	inspect := "docker inspect "
	homeA := map[string]string{
		doctor.ReachCommand:                                        "",
		doctor.ClockCommand:                                        "1760000000.100000000\n",
		doctor.EtcdHealthCommand(cfg):                              `[{"endpoint":"http://10.44.0.1:2379","health":true,"took":"4ms"},{"endpoint":"http://10.44.0.2:2379","health":false,"took":"5s","error":"context deadline exceeded"},{"endpoint":"http://10.44.0.3:2379","health":true,"took":"4ms"}]`,
		doctor.EtcdVersionCommand:                                  `{"etcdserver":"3.5.16","etcdcluster":"3.5.0"}`,
		doctor.SyncCommand(cfg.Deployment()):                       `{"leader":"home-b","quorum":0,"sync_standby":null}` + "\n",
		patroni.ClusterCommand(cfg.Deployment(), "10.44.0.1:8008"): `{"members":[{"name":"home-a","role":"replica","state":"running","timeline":2,"lag":1048576}],"scope":"paisans"}`,
		doctor.PatroniLogCommand(cfg.Deployment()):                 "patroni-1  | INFO: following a different leader because i am not the healthiest node\n",
		doctor.ContainersCommand(cfg.Deployment()):                 `{"Names":"paisans-f2a9-infra-patroni-1","State":"running","Status":"Up 5 minutes","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}` + "\n" + `{"Names":"paisans-f2a9-talk-app-1","State":"restarting","Status":"Restarting (1) 3 seconds ago","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01,com.docker.compose.project=paisans-f2a9-talk"}`,
		inspect:        `{"State":{"Status":"restarting","Restarting":true,"ExitCode":1,"Error":""},"RestartCount":9}`,
		"docker logs ": "booting\nSQLSTATE[08006] [7] connection to server at \"127.0.0.1\", port 5000 failed: Connection refused\n",
		"if [ ! -f '/srv/paisans/f2a9/auth/compose.yaml' ]": "standby\n",
		mesh.LinkCommand("psns-f2a9"):                       `[{"ifname":"psns-f2a9","flags":["POINTOPOINT","NOARP","UP","LOWER_UP"]}]`,
		mesh.RouteCommand:                                   `[{"dst":"10.44.0.0/24","dev":"psns-f2a9"}]`,
		mesh.AddrCommand:                                    `[{"ifname":"psns-f2a9","addr_info":[{"family":"inet","local":"10.44.0.1","prefixlen":24}]}]`,
		mesh.NetworksCommand:                                "[]",
		mesh.UserDaemonCommand:                              "absent\n",
	}
	vm := map[string]string{
		mesh.LinkCommand("psns-f2a9"):              `[{"ifname":"psns-f2a9","flags":["POINTOPOINT","NOARP","UP","LOWER_UP"]}]`,
		mesh.RouteCommand:                          `[{"dst":"10.44.0.0/24","dev":"psns-f2a9"},{"dst":"10.44.0.0/16","dev":"wg9"}]`,
		mesh.AddrCommand:                           "[]",
		mesh.NetworksCommand:                       "[]",
		mesh.UserDaemonCommand:                     "absent\n",
		doctor.ReachCommand:                        "",
		doctor.ClockCommand:                        "1760000000.000000000\n",
		doctor.ContainersCommand(cfg.Deployment()): `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up 2 days","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`,
		doctor.CaddyLogCommand(cfg.Deployment()):   quietCaddyLog,
	}
	watch := map[string]string{
		doctor.ReachCommand:                        "",
		doctor.ClockCommand:                        "1760000000.000000000\n",
		doctor.ContainersCommand(cfg.Deployment()): `{"Names":"paisans-f2a9-health-app-1","State":"running","Status":"Up 2 days","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`,
		doctor.CaddyLogCommand(cfg.Deployment()):   quietCaddyLog,
	}
	return map[string]doctorHost{
		"home-a.local":      {name: "home-a.local", answers: homeA, sent: sent, writes: writes},
		"home-b.local":      {name: "home-b.local", down: true, sent: sent, writes: writes},
		"vm.example.org":    {name: "vm.example.org", answers: vm, sent: sent, writes: writes},
		"watch.example.org": {name: "watch.example.org", answers: watch, sent: sent, writes: writes},
	}
}

// The stuck case, end to end: home-b led and is switched off, home-a is a
// replica /sync no longer names. doctor says so, puts starting home-b first
// and the forced failover second, exits non zero, and every command any host
// received is a read.
func TestDoctorDiagnosesTheStuckReplicaAndOnlyReads(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, stuckHosts(cfg, &sent, &writes))

	rec := withRecorder(t, true)
	runErr := runDoctor([]string{"--config", fixtureConfig()})
	out := rec.Lines()
	if runErr == nil || !strings.Contains(runErr.Error(), "marked FAIL") {
		t.Fatalf("doctor should fail on a cluster with no primary, got %v\n%s", runErr, out)
	}

	for _, want := range []string{
		"detail: 4 site(s), 3 reached",
		"done: check mesh home-a psns-f2a9 is up",
		"done: check mesh home-a nothing on the host overlaps the mesh subnet 10.44.0.0/24",
		"fail: check mesh vm the mesh subnet 10.44.0.0/24 overlaps route 10.44.0.0/16 dev wg9",
		"fail: check reach home-b ssh to ubuntu@home-b.local did not answer (Operation timed out)",
		"warned: check etcd quorum: 2 of 3 members healthy (needs 2), asked from home-a",
		"fail: check patroni no primary: home-a is a replica and will not promote while home-b, which led, is gone",
		"fail: check containers home-a paisans-f2a9-talk-app-1 restarting (exit 1, restarted 9 time(s))",
		"It cannot reach its database",
		"fail: check pocket-id auth no active instance, so sign in is down",
		"home-b   unreachable did not answer ssh (see reach)",
		"There is no database primary (see patroni)",
		"Not checked because of this: admission, user, docker, docker engine, linger, sudo rights, mesh, patroni, containers, settings talk, clocks, leftovers on home-b.",
		"done: check admission home-a admitted as ubuntu, covering its roles",
		"done: check user home-a ubuntu is in no privileged group",
		"done: check docker home-a its rootless Docker answers at unix:///run/user/1000/docker.sock",
		"done: check docker engine home-a Docker Engine 29.9.1",
		"done: check linger home-a its manager starts after the mesh at boot",
		"done: check sudo rights home-a sudo lets ubuntu run the WireGuard helper and nothing else",
		"section: failed, deepest first:",
		"doctor changes nothing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	start := strings.Index(out, "1. Start home-b (ssh ubuntu@home-b.local)")
	force := strings.Index(out, "2. Only if home-b is lost permanently")
	command := strings.Index(out, "patronictl -c /home/postgres/postgres.yml failover --candidate home-a --force")
	warning := strings.Index(out, "WARNING: every commit home-b made")
	if start < 0 || !(start < force && force < command && command < warning) {
		t.Errorf("want start home-b, the forced failover, its command, then its warning, in that order:\n%s", out)
	}

	// The failures again at the end, deepest first.
	closing := out[strings.Index(out, "section: failed, deepest first:"):]
	last := -1
	for _, title := range []string{"check reach home-b", "check mesh vm", "check patroni", "check containers home-a", "check pocket-id auth"} {
		i := strings.Index(closing, "fail: "+title)
		if i < last {
			t.Errorf("%s is out of order in the closing list:\n%s", title, closing)
		}
		last = i
	}

	exact, prefixes := readOnly(cfg)
	for _, s := range sent {
		command := s[strings.Index(s, ": ")+2:]
		allowed := false
		for _, e := range exact {
			allowed = allowed || command == e
		}
		for _, p := range prefixes {
			allowed = allowed || strings.HasPrefix(command, p)
		}
		if !allowed || strings.Contains(command, "patronictl") {
			t.Errorf("doctor sent a command outside its read allow list: %s", s)
		}
	}
	if len(writes) > 0 {
		t.Errorf("doctor wrote: %v", writes)
	}
	// home-b was asked once, and then left alone.
	var toB []string
	for _, s := range sent {
		if strings.HasPrefix(s, "home-b.local: ") {
			toB = append(toB, s)
		}
	}
	if len(toB) != 1 {
		t.Errorf("an unreachable site is asked once, got %v", toB)
	}
}

func TestDoctorRefusesAnUndeclaredSite(t *testing.T) {
	err := runDoctor([]string{"--config", fixtureConfig(), "--site", "nowhere"})
	if err == nil || !strings.Contains(err.Error(), `declares no site "nowhere"`) {
		t.Fatalf("err = %v", err)
	}
}

// --site narrows every check to the named sites, and the ones left out are
// never contacted.
func TestDoctorSiteNarrowsTheHostsReached(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, map[string]doctorHost{
		"vm.example.org": {name: "vm.example.org", sent: &sent, writes: &writes, answers: map[string]string{
			doctor.ReachCommand:                        "",
			doctor.ClockCommand:                        "1760000000.000000000\n",
			doctor.ContainersCommand(cfg.Deployment()): `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up","Labels":"community.paisans.deployment=f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}`,
			doctor.EtcdVersionCommand:                  `{"etcdserver":"3.5.16","etcdcluster":"3.5.0"}`,
			"docker compose -f /srv/paisans/f2a9/infra/compose.yaml exec -T etcd etcdctl": `[{"endpoint":"http://10.44.0.1:2379","health":true},{"endpoint":"http://10.44.0.2:2379","health":true},{"endpoint":"http://10.44.0.3:2379","health":true}]`,
		}},
	})
	rec := withRecorder(t, true)
	runErr := runDoctor([]string{"--config", fixtureConfig(), "--site", "vm"})
	out := rec.Lines()
	for _, s := range sent {
		if !strings.HasPrefix(s, "vm.example.org: ") {
			t.Errorf("reached a site outside --site: %s", s)
		}
	}
	if !strings.Contains(out, "detail: 1 site(s), 1 reached") || !strings.Contains(out, "no database site reached") || !strings.Contains(out, "not asked (outside --site)") {
		t.Errorf("output:\n%s", out)
	}
	if runErr == nil {
		t.Error("a deployment whose database could not be read is not a pass")
	}
}

// A record host admit could not have written fails admission with how its
// owner recovers: move the record aside, since admit will not write over
// one it cannot read, then request and admit again.
func TestDoctorSaysToMoveAnUnreadableRecordAside(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	path := admittedPath(cfg)
	var sent, writes []string
	withDoctorHosts(t, cfg, map[string]doctorHost{
		"vm.example.org": {name: "vm.example.org", sent: &sent, writes: &writes,
			answers: map[string]string{doctor.ReachCommand: ""},
			files:   map[string]string{path: "{not a record"}},
	})
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "vm"})
	out := rec.Lines()
	move := strings.Index(out, "`sudo mv "+path+" "+path+".unreadable`")
	request := strings.Index(out, "`paisans host request --site vm > vm.admit`")
	admit := strings.Index(out, "`sudo paisans host admit --request vm.admit`")
	if !strings.Contains(out, "fail: check admission vm its admission record cannot be read") || move < 0 || !(move < request && request < admit) {
		t.Errorf("want the failure, then move it aside, request and admit, in that order:\n%s", out)
	}
}

// The leftovers check: on the gateway, a stack of this deployment the
// configuration no longer renders is a warning, a site block in the host's
// own directory is info, a foreign container is not reported, and every
// command it sent is a read.
func TestDoctorReportsLeftoversAndForeignCaddyUsers(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	id := cfg.Deployment().ID
	probes := hostcheck.ReadCommands(cfg.Deployment())
	answers := map[string]string{
		doctor.ReachCommand:                        "",
		doctor.ClockCommand:                        "1760000000.000000000\n",
		doctor.ContainersCommand(cfg.Deployment()): `{"Names":"paisans-f2a9-infra-etcd-1","State":"running","Status":"Up","Labels":"community.paisans.deployment=` + id + `"}`,
		doctor.EtcdVersionCommand:                  `{"etcdserver":"3.5.16","etcdcluster":"3.5.0"}`,
		"docker compose -f /srv/paisans/f2a9/infra/compose.yaml exec -T etcd etcdctl": `[{"endpoint":"http://10.44.0.1:2379","health":true},{"endpoint":"http://10.44.0.2:2379","health":true},{"endpoint":"http://10.44.0.3:2379","health":true}]`,
		mesh.LinkCommand("psns-f2a9"): `[{"ifname":"psns-f2a9","flags":["POINTOPOINT","NOARP","UP","LOWER_UP"]}]`,
		mesh.RouteCommand:             `[{"dst":"10.44.0.0/24","dev":"psns-f2a9"}]`,
		mesh.AddrCommand:              "[]",
		mesh.NetworksCommand:          "[]",
		mesh.UserDaemonCommand:        "absent\n",
		probes[0]:                     "uid 1001\nsubuid paisans-f2a9:165536:65536\n",
		probes[1]:                     "27.3.1\n",
		probes[2]:                     "docker-ce install ok installed\n",
		probes[3]: `{"id":"a","name":"/paisans-f2a9-infra-caddy-1","pid":10,"labels":{"community.paisans.deployment":"` + id + `","com.docker.compose.project":"paisans-f2a9-infra","com.docker.compose.service":"caddy"},"networks":{"web":{}},"ports":{}}
{"id":"b","name":"/paisans-f2a9-old-app-1","pid":11,"labels":{"community.paisans.deployment":"` + id + `","com.docker.compose.project":"paisans-f2a9-old","com.docker.compose.service":"app"},"networks":{"paisans-f2a9-old_default":{}},"ports":{}}
{"id":"c","name":"/blog-ghost-1","pid":12,"labels":{"com.docker.compose.project":"blog"},"networks":{"web":{}},"ports":{}}
`,
		probes[4]:                                "",
		probes[5]:                                "",
		probes[6]:                                "",
		probes[7]:                                "1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536\n",
		probes[8]:                                "[]",
		probes[9]:                                "/srv/paisans/f2a9/caddy.d/blog.caddy\n",
		doctor.CaddyLogCommand(cfg.Deployment()): quietCaddyLog,
	}
	var sent, writes []string
	withDoctorHosts(t, cfg, map[string]doctorHost{
		"vm.example.org": {name: "vm.example.org", sent: &sent, writes: &writes, answers: answers},
	})
	rec := withRecorder(t, true)
	_ = runDoctor([]string{"--config", fixtureConfig(), "--site", "vm"})
	out := rec.Lines()
	for _, want := range []string{
		"leftovers",
		"warned: check leftovers vm 1 thing(s) of this deployment left over",
		"stack old (compose project paisans-f2a9-old, 1 of 1 running): paisans-f2a9-old-app-1",
		"apply leaves them in place.",
		"`paisans app remove old` takes old's off every site, once a whole apply has run on each; it keeps its data unless given --delete-data.",
		"done: check leftovers vm 1 foreign thing(s) rely on this deployment's Caddy",
		"site block /srv/paisans/f2a9/caddy.d/blog.caddy, served by this deployment's Caddy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "blog-ghost-1") {
		t.Errorf("a foreign container is reported:\n%s", out)
	}
	exact, prefixes := readOnly(cfg)
	for _, s := range sent {
		command := s[strings.Index(s, ": ")+2:]
		allowed := false
		for _, e := range exact {
			allowed = allowed || command == e
		}
		for _, p := range prefixes {
			allowed = allowed || strings.HasPrefix(command, p)
		}
		if !allowed {
			t.Errorf("doctor sent a command outside its read allow list: %s", s)
		}
	}
	if len(writes) > 0 {
		t.Errorf("doctor wrote: %v", writes)
	}
}

// doctor --execute takes out a config: key that repeats a value gating
// renders, and leaves one that contradicts it; validate then refuses only the
// contradiction, and says doctor removes the repeat while it is there.
func TestDoctorExecuteRemovesOnlyRedundantGateConfig(t *testing.T) {
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "      KBIN_META_TITLE: A place to talk\n"
	if !strings.Contains(string(data), anchor) {
		t.Fatal("the fixture's talk config moved; update this test")
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	body := strings.Replace(string(data), anchor, anchor+"      # Before gating wrote it.\n      MBIN_USE_FEDERATION_ALLOW_LIST: true\n      MBIN_PRIVATE_INSTANCE: false\n", 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range validate.Check(before).Refusals() {
		if f.Key == "apps.talk.config.MBIN_USE_FEDERATION_ALLOW_LIST" && !strings.Contains(f.Message, "paisans doctor --execute") {
			t.Errorf("the refusal of the repeat does not name doctor --execute: %s", f.Message)
		}
		if f.Key == "apps.talk.config.MBIN_PRIVATE_INSTANCE" && strings.Contains(f.Message, "doctor --execute") {
			t.Errorf("the refusal of the contradiction offers doctor --execute, which would not touch it: %s", f.Message)
		}
	}

	if err := removeRedundantGateConfig(stdoutReporter(), path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(body, "      # Before gating wrote it.\n      MBIN_USE_FEDERATION_ALLOW_LIST: true\n", "", 1)
	if string(after) != want {
		t.Errorf("doctor --execute changed more than the one key:\n%s", after)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var refused []string
	for _, f := range validate.Check(cfg).Refusals() {
		refused = append(refused, f.Key)
	}
	if strings.Join(refused, ",") != "apps.talk.config.MBIN_PRIVATE_INSTANCE" {
		t.Errorf("after doctor --execute, refused %v, want only the contradiction", refused)
	}
}

// busyHost answers every command after a moment, and counts how many it
// runs at once, and how many every host runs at once.
type busyHost struct {
	name      string
	mu        *sync.Mutex
	now, peak *int
	all, top  *int
}

func (h busyHost) Describe() string { return "ubuntu@" + h.name }
func (h busyHost) Run(string) (string, error) {
	h.mu.Lock()
	*h.now++
	*h.all++
	*h.peak = max(*h.peak, *h.now)
	*h.top = max(*h.top, *h.all)
	h.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	h.mu.Lock()
	*h.now--
	*h.all--
	h.mu.Unlock()
	return "", nil
}
func (h busyHost) RunInput(command, _ string) (string, error) { return h.Run(command) }
func (h busyHost) ReadFile(string) (string, bool, error) {
	_, err := h.Run("")
	return "", false, err
}
func (h busyHost) WriteFile(string, string, uint32) error { return errors.New("doctor writes nothing") }

// Checks run at once: a site never runs more commands at a time than the
// per site bound, which keeps under sshd's sessions per connection, and
// sites are not bounded against each other.
func TestDoctorBoundsCommandsPerSiteOnly(t *testing.T) {
	saved, savedBound, savedAtOnce := doctorTransport, doctorSiteCommands, doctorAtOnce
	t.Cleanup(func() { doctorTransport, doctorSiteCommands, doctorAtOnce = saved, savedBound, savedAtOnce })
	doctorSiteCommands, doctorAtOnce = 2, 0
	var mu sync.Mutex
	all, top := 0, 0
	hosts := map[string]busyHost{}
	for _, name := range []string{"home-a.local", "home-b.local", "vm.example.org", "watch.example.org"} {
		hosts[name] = busyHost{name: name, mu: &mu, now: new(int), peak: new(int), all: &all, top: &top}
	}
	doctorTransport = func(tr apply.SSHTransport) apply.Transport {
		h, ok := hosts[tr.Host]
		if !ok {
			t.Fatalf("doctor reached an unexpected host %q", tr.Host)
		}
		return h
	}
	_ = withRecorder(t, false)
	_ = runDoctor([]string{"--config", fixtureConfig()})
	busiest := 0
	for name, h := range hosts {
		if *h.peak > 2 {
			t.Errorf("%s ran %d commands at once, over the bound of 2", name, *h.peak)
		}
		busiest = max(busiest, *h.peak)
	}
	if busiest < 2 || top <= 2 {
		t.Errorf("the checks did not run at once: at most %d on one site, %d in all", busiest, top)
	}
}

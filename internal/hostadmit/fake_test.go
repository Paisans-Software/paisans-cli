package hostadmit_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/registry"
)

// probeMarker is a string only admit's own probe contains.
const probeMarker = "paisans host admit: read only"

// fakeHost is a host as admit's probes see it: the probe's facts as key and
// value lines, files read with ReadFile, and an answer for each other probe,
// found by a substring of its command. Nothing runs a shell; what is under
// test is what admit plans from what a host says.
type fakeHost struct {
	facts   [][2]string
	files   map[string]string
	answers map[string]string
	ran     []string
	inputs  []string
	writes  map[string]uint32
	// onRun, when set, sees every command Execute runs, so a test can make
	// the host change as a real one would.
	onRun func(h *fakeHost, command string)
}

func (h *fakeHost) Describe() string { return "fake" }

func (h *fakeHost) Run(command string) (string, error) { return h.run(command, "") }

func (h *fakeHost) RunInput(command, stdin string) (string, error) { return h.run(command, stdin) }

func (h *fakeHost) run(command, stdin string) (string, error) {
	h.ran = append(h.ran, command)
	h.inputs = append(h.inputs, stdin)
	if h.onRun != nil {
		h.onRun(h, command)
	}
	if strings.Contains(command, probeMarker) {
		var b strings.Builder
		for _, kv := range h.facts {
			fmt.Fprintf(&b, "%s %s\n", kv[0], kv[1])
		}
		b.WriteString("end\n")
		return b.String(), nil
	}
	// The longest matching key wins, so a specific answer overrides a
	// general one whatever the map's order.
	keys := make([]string, 0, len(h.answers))
	for k := range h.answers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j]) || len(keys[i]) == len(keys[j]) && keys[i] < keys[j]
	})
	for _, k := range keys {
		if strings.Contains(command, k) {
			return h.answers[k], nil
		}
	}
	return "", nil
}

func (h *fakeHost) ReadFile(path string) (string, bool, error) {
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *fakeHost) WriteFile(path, content string, mode uint32) error {
	h.files[path] = content
	h.writes[path] = mode
	return nil
}

// with sets key to value, replacing every line the key had.
func (h *fakeHost) with(key, value string) *fakeHost {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv[0] != key {
			kept = append(kept, kv)
		}
	}
	h.facts = append(kept, [2]string{key, value})
	return h
}

// add adds a line for a key that repeats.
func (h *fakeHost) add(key, value string) *fakeHost {
	h.facts = append(h.facts, [2]string{key, value})
	return h
}

// withFile sets a "file" fact: path with the checksum of content, a mode
// and an owner, as the probe prints a file admit writes.
func (h *fakeHost) withFile(path, content, mode string, uid int) *fakeHost {
	h.dropKeyed("file", path)
	return h.add("file", fmt.Sprintf("%s %s %s %d %d", path, sum(content), mode, uid, uid))
}

// withDir sets a "dir" fact as ls -ldn prints it: mode, uid, gid.
func (h *fakeHost) withDir(path, mode string, uid, gid int) *fakeHost {
	h.dropKeyed("dir", path)
	return h.add("dir", fmt.Sprintf("%s %s %d %d", path, mode, uid, gid))
}

func (h *fakeHost) dropKeyed(key, path string) {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv[0] == key && strings.HasPrefix(kv[1], path+" ") {
			continue
		}
		kept = append(kept, kv)
	}
	h.facts = kept
}

func (h *fakeHost) file(path, content string) *fakeHost {
	h.files[path] = content
	return h
}

func (h *fakeHost) answer(substring, out string) *fakeHost {
	h.answers[substring] = out
	return h
}

func sum(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

const ubuntu2404 = "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nID=ubuntu\nID_LIKE=debian\nUBUNTU_CODENAME=noble\n"

// newBlank is a VPS fresh from Ubuntu 24.04: no Docker, AppArmor
// restricting unprivileged user namespaces with the rootlesskit profile
// Ubuntu's apparmor package loads, no
// deployment user, no file admit writes, ufw installed and active, one
// public interface, systemd-resolved, and nothing on 80 or 443.
func newBlank() *fakeHost {
	h := &fakeHost{files: map[string]string{
		"/etc/os-release":  ubuntu2404,
		"/etc/resolv.conf": "nameserver 127.0.0.53\noptions edns0 trust-ad\n",
	}, answers: map[string]string{}, writes: map[string]uint32{}}
	for _, kv := range [][2]string{
		{"engine", "absent"}, {"pkg", "docker-ce absent"}, {"pkg", "docker.io absent"},
		{"pkg", "docker-ce-rootless-extras absent"}, {"pkg", "uidmap absent"}, {"pkg", "nftables installed"},
		{"setuptool", "absent"}, {"snap", "absent"}, {"system-socket", "absent"},
		{"userns-restrict", "1"}, {"rootlesskit-profile", "loaded"},
		{"passwd", "absent"}, {"home", "absent 0 /home/paisans-f2a9"}, {"sudo", "absent"}, {"subuid", "ubuntu:100000:65536"}, {"subgid", "ubuntu:100000:65536"},
		{"br_netfilter", "absent"}, {"iptables-module", "nf_tables loaded"}, {"forward-policy", "ACCEPT"},
		{"port-start", "1024"}, {"ip-forward", "0"},
		{"default-dev", "eth0"}, {"lan", "203.0.113.10/24"},
		{"ufw", "installed"}, {"ufw-active", "yes"},
		{"egress-enabled", "no"}, {"egress-table", "absent"},
		{"data-path", "/home"}, {"data-fs", "ext2/ext3"}, {"data-avail", "42949672960"},
	} {
		h.add(kv[0], kv[1])
	}
	h.withDir("/etc/wireguard", "absent", 0, 0).withDir("/etc/paisans", "absent", 0, 0).
		withDir("/srv/paisans", "absent", 0, 0).withDir("/srv/paisans/f2a9", "absent", 0, 0)
	// hostprep's probes, as host prepare's tests answer them.
	h.answer("docker compose version", "compose absent\npkg nftables install ok installed\npkg ufw install ok installed\nkeyring absent\narch amd64\n").
		answer("RuntimeWatchdogUSec", "device absent\nsystemd 0\ndaemon absent\n").
		answer("ufw show added", "ufw present\nstatus active\n").
		answer(userStateMarker, idleUser).
		answer(portsMarker, "end\n").
		answer("is-enabled", "enabled \nactive \n").
		answer("ip -j route", "[]").answer("ip -j addr", "[]").answer("docker network", "[]")
	return h
}

// withUser is h once useradd has made paisans-f2a9, uid 1001, with its own
// subordinate range after ubuntu's, its manager running under linger.
func (h *fakeHost) withUser() *fakeHost {
	return h.with("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash").with("home", "dir 1001 /home/paisans-f2a9").
		with("groups", "paisans-f2a9").with("group", "paisans-f2a9").
		with("linger", "yes").with("manager", "active").with("daemon", "absent").
		add("subuid", "paisans-f2a9:165536:65536").add("subgid", "paisans-f2a9:165536:65536").
		with("data-path", "/home/paisans-f2a9")
}

// fixture is the admission package's own: one site per role mix, token
// f2a9. home-a is data, apps and the gateway with an endpoint; home-b data
// alone as user community on port 2222; vm the witness, dialled; status a
// monitor behind the owner's web server; watch a Caddy monitor with an
// endpoint; store storage alone.
func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "admission", "testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// request is site's request as the owner reads it from the file.
func request(t *testing.T, site string) admission.Request {
	t.Helper()
	built, err := admission.Build(fixture(t), site)
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Parse(admission.Encode(built))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// requestFor is request without a *testing.T, for a fixture's builders.
// The fixture always builds, so an error is a broken test and panics.
func requestFor(site string) admission.Request {
	cfg, err := config.Load(filepath.Join("..", "admission", "testdata", "paisans-stack.yaml"))
	if err != nil {
		panic(err)
	}
	built, err := admission.Build(cfg, site)
	if err != nil {
		panic(err)
	}
	req, err := admission.Parse(admission.Encode(built))
	if err != nil {
		panic(err)
	}
	return req
}

// admitted is the time every golden plan is drawn at.
var admitted = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func itoa(n int) string { return strconv.Itoa(n) }

// previousRecord is the record an earlier admit of site wrote, with
// dataDir, encoded as admit writes it.
func previousRecord(t *testing.T, site, dataDir string) string {
	t.Helper()
	req := request(t, site)
	rec := admission.NewRecord(req, req.Grants, "", admitted)
	rec.DataDir = dataDir
	data, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func input(t *testing.T, site string, flags hostadmit.Flags) hostadmit.Input {
	t.Helper()
	req := request(t, site)
	return hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Flags: flags, Now: admitted}
}

// registryWith is a host registry holding one entry, home-a's claim made
// for deployment id at address: what a claim of another site of id leaves.
func registryWith(id, address string) string {
	_, e := registry.ForRequest(requestFor("home-a"), admitted)
	e.Address = address
	return encodeRegistry(id, e)
}

// registryOf is the registry admit's claim of req leaves on a host no
// other deployment has claimed.
func registryOf(req admission.Request) string {
	id, e := registry.ForRequest(req, admitted)
	return encodeRegistry(id, e)
}

func encodeRegistry(id string, e registry.Entry) string {
	data, err := registry.Encode(registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{id: e}})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// admittedHost is the fake host once admit of req with flags has run: the
// plan built on home() (communityHome() for home-b's user), each of its
// steps applied as a real host would change. Each file step's file is
// present as written, root's, with its mode; each directory `install -d`
// made; the user's daemon installed and running with the daemon.json
// written; the keys and sidecar in place; ufw holding each rule added; the
// sysctls applied; the egress table loaded; the registry claimed; the
// record written. It is the one fully admitted host every re-run test
// starts from, and TestASecondRunPlansNothing keeps it honest: a fact it
// forgets is a step the second run plans.
func admittedHost(req admission.Request, flags hostadmit.Flags) *fakeHost {
	h := home()
	if req.User == "community" {
		h = communityHome()
	}
	if flags.Watchdog {
		h.answer("RuntimeWatchdogUSec", "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon absent\n")
	}
	in := hostadmit.Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Flags: flags, Now: admitted}
	plan, err := hostadmit.Build(h, in)
	if err != nil {
		panic(fmt.Sprintf("admitting %s: %v", req.Site, err))
	}
	uid := 1001
	if req.User == "community" {
		uid = 1002
	}
	var rules []string
	daemonJSON, keys := "", ""
	for _, s := range plan.Steps {
		if s.File != nil {
			h.withFile(s.File.Path, s.File.Content, fmt.Sprintf("%o", s.File.Mode), 0)
			h.files[s.File.Path] = s.File.Content
		}
		for _, f := range strings.Split(s.Command, " && ") {
			switch {
			case strings.HasPrefix(f, "install -d -m "):
				w := strings.Fields(f) // install -d -m MODE [-o U -g G] PATH
				owner := 0
				if len(w) > 5 && w[5] != "root" {
					owner = uid
				}
				h.withDir(w[len(w)-1], modeString(w[3]), owner, owner)
			case strings.HasPrefix(f, "ufw allow "):
				rules = append(rules, "rule "+strings.TrimPrefix(f, "ufw "))
			case strings.HasPrefix(f, "mv -f /etc/sudoers.d/"):
				w := strings.Fields(f) // mv -f TMP FINAL || ...
				h.withFile(w[3], h.files[w[2]], "440", 0)
			}
		}
		if strings.Contains(s.Command, "daemon.json") {
			daemonJSON = s.Input
		}
		if strings.Contains(s.Command, "authorized_keys") {
			keys = s.Input
		}
	}
	h.answer("ufw show added", "ufw present\nstatus active\n"+strings.Join(rules, "\n")+"\n").
		with("br_netfilter", "loaded").with("egress-enabled", "yes").with("egress-table", "present").
		answer(userStateMarker, "daemon active\nunit present\ndaemon-json "+sum(daemonJSON)+"\ncontainers 0\nimages 0\nvolumes 0\nend\n").
		answer(keysMarker, keysAfter(keys))
	if flags.PublicPorts {
		h.with("port-start", "80")
	}
	if req.Endpoint {
		h.with("ip-forward", "1")
	}
	path, _ := admission.Path(req.Token())
	h.add("record", path)
	return h.file(registry.Path, registryOf(req))
}

// modeString is an octal directory mode as ls -ldn prints it: 750 is
// drwxr-x---.
func modeString(octal string) string {
	n, err := strconv.ParseUint(octal, 8, 32)
	if err != nil {
		panic(err)
	}
	out := []byte("d")
	for shift := 6; shift >= 0; shift -= 3 {
		bits := n >> shift
		for i, c := range "rwx" {
			if bits&(4>>i) != 0 {
				out = append(out, byte(c))
			} else {
				out = append(out, '-')
			}
		}
	}
	return string(out)
}

// keysAfter is what the keys probe prints once the keys step has run with
// stdin on a host whose user had no authorized_keys: the keys appended,
// the marker, then the sidecar written.
func keysAfter(stdin string) string {
	keys, sidecar, _ := strings.Cut(stdin, "--- sidecar ---\n")
	return keys + "paisans-sidecar\n" + sidecar
}

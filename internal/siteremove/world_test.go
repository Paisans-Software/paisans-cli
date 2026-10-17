package siteremove_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/apply/applytest"
	"github.com/paisans-software/paisans-cli/internal/apply/meshfake"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/patronitest"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

const (
	ourID   = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	otherID = "0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"
	root    = "/srv/paisans/f2a9"
	wgConf  = "/srv/paisans/f2a9/psns-f2a9.conf"
	// wgHelper is the start of every command sent to the WireGuard helper
	// for the fixture's interface, and wgInstalled is where its put
	// installs the mesh file on a real host.
	wgHelper    = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	wgInstalled = "/etc/wireguard/psns-f2a9.conf"
	record      = "/srv/paisans/f2a9/.paisans/authorized_keys.owned"
	keysAt      = "/home/ubuntu/.ssh/authorized_keys"
	alice       = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
)

var dep = deployment.Deployment{ID: ourID}

// world is a running deployment in maps: five hosts, etcd's membership,
// Patroni's members, Garage's layout and what each HAProxy serves. Every
// gate in site remove is about what a command on some host answers, so a
// fake that answers from shared state is the same test as five machines with
// the failures made reachable.
type world struct {
	t          *testing.T
	cfg        *config.Config
	secrets    *config.Secrets
	configPath string
	hosts      map[string]*host

	etcd     []apply.EtcdMember
	nextID   uint64
	etcdDown map[string]bool
	// healthOut, when set, is what `endpoint health` prints instead of the
	// members' health, with exit status 1.
	healthOut string
	// patroniLog wraps every patronictl answer in the log lines patronictl
	// writes when it cannot reach one of etcd's endpoints.
	patroniLog bool
	// clusterID is the cluster ID every etcd reports in its member list's
	// header; 0 leaves it out, as an etcdctl that prints none would.
	clusterID uint64

	members  []map[string]any
	syncMode bool

	layout map[string]string // short ID to zone
	staged map[string]bool   // short IDs staged for removal
	// assigned are role assignments staged and not applied, short ID to
	// zone, as an interrupted storage add or a hand-run `garage layout
	// assign` leaves them.
	assigned map[string]string
	// redundancy is a zone redundancy staged and not applied, empty when
	// none is.
	redundancy string
	version    int
	moving     int
	resyncing  int
	garageDown map[string]bool

	served map[string]string

	// log is every command on every host, in order, as "site: command".
	log []string
	// recreated is every site whose Patroni was recreated, in order.
	recreated []string
	// recordRace makes every write of the deployment record find it changed
	// by another command meanwhile.
	recordRace bool
	// activePocket is the site whose Pocket ID instance is active.
	activePocket string

	// Knobs that break one thing.
	failOnce string
	// failOut is what the command failOnce fails prints, when set.
	failOut     string
	unreachable map[string]bool
	caddyBroken bool
	// neverStreams is a site whose recreated Patroni never streams again,
	// while its member key in etcd, which the old process wrote, still says
	// streaming.
	neverStreams string
	// unhealthy names an app stack whose container has exited.
	unhealthy string

	// cutoff is the site a removal with --host-gone cuts off: once no other
	// host's running interface lists its key, a leader it held fails over, as
	// Patroni's does when the leader key expires.
	cutoff string
	// skips is every `garage layout skip-dead-nodes` run, its arguments.
	skips []string
}

type host struct {
	w        *world
	name     string
	files    map[string]string
	commands []string
	wgUp     bool
	// wgRun is the mesh file the running interface was last handed by the
	// WireGuard helper, nil while it runs the file on disk.
	wgRun     *string
	patroniUp bool
	etcdUp    bool
	// etcdContainer is whether the etcd container runs, which a removed
	// member's does until it is stopped.
	etcdContainer bool
	// etcdHosts is ETCD3_HOSTS as the running Patroni has it.
	etcdHosts string
	// ownCluster, when set, is the separate cluster this host's etcd
	// founded on its own, which its member list reports instead of the
	// world's.
	ownCluster *etcdView
	// starting is how many more times the recreated Patroni's REST API
	// answers that Postgres is starting.
	starting   int
	containers []hostcheck.Container
	networks   []hostcheck.Network
	volumes    []hostcheck.Volume
	rules      []string
	// images are the image IDs present on the host.
	images map[string]bool
	// caddyLoads is every configuration `caddy reload` loaded, in order.
	caddyLoads []string
	// gone are the image IDs `docker image rm` removed, which the image
	// probe then answers absent.
	gone map[string]bool
	// reads is every path ReadFile was asked for, in order.
	reads []string
	// filesAnswer, when set, rewrites the answer to the probe that reads the
	// manifest's files.
	filesAnswer func(string) (string, error)
	// loginRoot makes the login user root, as --ssh root@host does, and
	// loginName another unprivileged user than the deployment's.
	loginRoot bool
	loginName string
}

// userUnits is the systemd directory of the world's login user, ubuntu.
const userUnits = "/home/ubuntu/.config/systemd/user"

func (h *host) Describe() string { return h.name }

func (h *host) ReadFile(path string) (string, bool, error) {
	if h.w.unreachable[h.name] {
		return "", false, fmt.Errorf("%s: %w", h.name, apply.ErrUnreachable)
	}
	h.reads = append(h.reads, path)
	c, ok := h.files[path]
	return c, ok, nil
}

// calls is how many times anything reached the host: every command and every
// file read, each of which is an ssh connection on a real one.
func (h *host) calls() int { return len(h.commands) + len(h.reads) }

func (h *host) WriteFile(path, content string, mode uint32) error {
	h.files[path] = content
	return nil
}

func (h *host) RunInput(command, stdin string) (string, error) {
	if path, ok := strings.CutPrefix(command, "cat > '"); ok {
		h.commands = append(h.commands, command)
		h.w.log = append(h.w.log, h.name+": "+command)
		h.files[strings.TrimSuffix(path, "'")] = stdin
		return "", nil
	}
	if command == wgHelper+"put" {
		h.commands = append(h.commands, command)
		h.w.log = append(h.w.log, h.name+": "+command)
		h.files[wgInstalled] = stdin
		return "", nil
	}
	return h.Run(command)
}

// caddyFile is the host file a `docker exec <id> caddy ... --config <path>`
// reads: the container must be one of the host's, running, and the path one
// its mounts reach.
func (h *host) caddyFile(command string) (string, error) {
	m := regexp.MustCompile(`^docker exec '([^']+)' caddy \w+ --config '([^']+)' --adapter caddyfile$`).FindStringSubmatch(command)
	if m == nil {
		return "", fmt.Errorf("not a caddy command: %s", command)
	}
	running := false
	for _, c := range h.containers {
		running = running || (c.ID == m[1] && c.PID > 0)
	}
	if !running {
		return "", fmt.Errorf("Error response from daemon: container %s is not running", m[1])
	}
	var hostPath string
	switch {
	case m[2] == "/etc/caddy/Caddyfile":
		hostPath = root + "/infra/caddy/Caddyfile"
	case strings.HasPrefix(m[2], "/etc/caddy/snippets/"):
		hostPath = root + "/infra/caddy/snippets/" + strings.TrimPrefix(m[2], "/etc/caddy/snippets/")
	default:
		return "", fmt.Errorf("%s is not mounted", m[2])
	}
	content, ok := h.files[hostPath]
	if !ok {
		return "", fmt.Errorf("open %s: no such file", m[2])
	}
	return content, nil
}

// keptPaths are the paths a find with -prune names, the kept ones.
func keptPaths(command string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`-path '([^']+)'`).FindAllStringSubmatch(command, -1) {
		out = append(out, m[1])
	}
	return out
}

func isKept(p string, kept []string) bool {
	for _, k := range kept {
		if p == k || strings.HasPrefix(p, k+"/") {
			return true
		}
	}
	return false
}

// keptProbe answers the probe of the deployment's directory with Caddy kept:
// every file not under a kept path, and the directories above them, as find
// prints them.
func (h *host) keptProbe(command string) string {
	kept := keptPaths(command)
	var b strings.Builder
	dirs := map[string]bool{}
	for _, p := range h.sortedFiles() {
		if !strings.HasPrefix(p, root+"/") {
			continue
		}
		for d := path.Dir(p); d != root && strings.HasPrefix(d, root+"/"); d = path.Dir(d) {
			if !dirs[d] && !isKept(d, kept) {
				dirs[d] = true
				b.WriteString("d " + d + "\n")
			}
		}
		if !isKept(p, kept) {
			b.WriteString("f " + p + "\n")
		}
	}
	return b.String()
}

// deleteBesideKept deletes what the loops over each directory above a kept
// path delete: each entry of the directory whose name the loop does not
// keep, judged in the deepest directory a loop names.
func (h *host) deleteBesideKept(command string) {
	keep := map[string][]string{}
	for _, m := range regexp.MustCompile(`for f in '([^']+)'/\*[^;]*; do [^;]*;( case "\$\{f##\*/\}" in ([^)]*)\))?`).FindAllStringSubmatch(command, -1) {
		var names []string
		for _, n := range strings.Split(m[3], "|") {
			if n = strings.Trim(n, "'"); n != "" {
				names = append(names, n)
			}
		}
		keep[m[1]] = names
	}
	for p := range h.files {
		deepest := ""
		for d := range keep {
			if strings.HasPrefix(p, d+"/") && len(d) > len(deepest) {
				deepest = d
			}
		}
		if deepest == "" {
			continue
		}
		if !contains(keep[deepest], strings.SplitN(strings.TrimPrefix(p, deepest+"/"), "/", 2)[0]) {
			delete(h.files, p)
		}
	}
}

func (h *host) hostSites() []string {
	var out []string
	for _, p := range h.sortedFiles() {
		if strings.HasPrefix(p, sitesDir+"/") && strings.HasSuffix(p, ".caddy") {
			out = append(out, p)
		}
	}
	return out
}

func (h *host) ran(sub string) int {
	n := 0
	for _, c := range h.commands {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

var (
	memberRemove = regexp.MustCompile(`member remove ([0-9a-f]+)`)
	switchover   = regexp.MustCompile(`switchover --leader (\S+) --candidate (\S+)`)
	memberDel    = regexp.MustCompile(`del /service/paisans/members/(\S+)`)
	serverRe     = regexp.MustCompile(`(?m)^\s+server (\S+) `)
	peerRe       = regexp.MustCompile(`(?m)^PublicKey = (\S+)`)
	manifestLine = regexp.MustCompile(`^m='([^']+)'; if \[ -f "\$m" \]; then (h=\$\(sha256sum|c=\$\(base64) `)
	fileLine     = regexp.MustCompile(`^f='([^']+)'; .* = '([0-9a-f]+)' \]`)
	layoutRemove = regexp.MustCompile(`^layout remove ([0-9a-f]+)$`)
	layoutApply  = regexp.MustCompile(`^layout apply --version (\d+)$`)
)

func (h *host) Run(command string) (string, error) {
	w := h.w
	h.commands = append(h.commands, command)
	w.log = append(w.log, h.name+": "+command)
	if w.unreachable[h.name] {
		return "ssh: connect to host port 22: Operation timed out\n", fmt.Errorf("%s: %w after 3 attempts: exit status 255", h.name, apply.ErrUnreachable)
	}
	if w.failOnce != "" && strings.Contains(command, w.failOnce) {
		w.failOnce = ""
		if w.failOut != "" {
			return w.failOut, errors.New("exit status 1")
		}
		return "failed by the test", errors.New("exit status 1")
	}
	if out, ok, err := meshfake.ChainedRead(command, h.Run); ok {
		return out, err
	}
	// admission.Mine: the login's name, then every admission record.
	if strings.HasPrefix(command, `echo "user $(id -un)"; for f in `+admission.Dir+`/admitted.`) {
		login := "ubuntu"
		switch {
		case h.loginRoot:
			login = "root"
		case h.loginName != "":
			login = h.loginName
		}
		out := "user " + login + "\n"
		for _, p := range h.sortedFiles() {
			if strings.HasPrefix(p, admission.Dir+"/admitted.") && strings.HasSuffix(p, ".json") {
				out += p + "\n"
			}
		}
		return out, nil
	}
	switch field, _ := strings.CutPrefix(command, wgHelper+"show "); field {
	case "allowed-ips", "persistent-keepalive", "listen-port":
		out, _ := meshfake.ShowRunning(field, h.runningWG())
		return out, nil
	}
	// the deployment's lock, which the keys take: it leaves its lock file
	// in the state directory, as on a host, and the command after it is
	// what runs.
	if dep := (deployment.Deployment{ID: "f2a9"}); strings.HasPrefix(command, dep.StateLockCommand("the keys")+"; ") {
		h.files[dep.StatePath("lock")] = ""
		command = strings.TrimPrefix(command, dep.StateLockCommand("the keys")+"; ")
	}
	quoted := func(s string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
	switch {
	case command == "true":
		return "", nil

	// the manifest's files: a sum or the content of each, or gone
	case strings.HasPrefix(command, "m='"):
		lines := strings.Split(strings.TrimSpace(command), "\n")
		if lines[len(lines)-1] != "echo end" {
			return "", fmt.Errorf("the manifest read has no end: %q", command)
		}
		var out strings.Builder
		for _, line := range lines[:len(lines)-1] {
			m := manifestLine.FindStringSubmatch(line)
			if m == nil {
				return "", fmt.Errorf("unreadable manifest read line %q", line)
			}
			content, ok := h.files[m[1]]
			switch {
			case !ok:
				out.WriteString("gone\n")
			case strings.HasPrefix(m[2], "c="):
				fmt.Fprintf(&out, "file %s\n", base64.StdEncoding.EncodeToString([]byte(content)))
			default:
				fmt.Fprintf(&out, "sum %s\n", sum(content))
			}
		}
		out.WriteString("end\n")
		if h.filesAnswer != nil {
			return h.filesAnswer(out.String())
		}
		return out.String(), nil

	// copies on the host, for a kept Caddy's Caddyfile
	case strings.HasPrefix(command, "cp -p -- '"):
		q := quoted(command)
		c, ok := h.files[q[0]]
		if !ok {
			return "cp: cannot stat", errors.New("exit status 1")
		}
		h.files[q[1]] = c
		return "", nil
	case strings.HasPrefix(command, "cat -- '"):
		q := quoted(command)
		h.files[q[1]] = h.files[q[0]]
		return "", nil
	case strings.HasPrefix(command, "if [ -f '") && strings.Contains(command, "then cat -- '"):
		q := quoted(command)
		c, ok := h.files[q[0]]
		if !ok {
			return "", errors.New("exit status 4")
		}
		h.files[q[2]] = c
		return "", nil
	case strings.HasPrefix(command, "rm -f -- '") && strings.Count(command, "'") > 2:
		for _, f := range quoted(command) {
			delete(h.files, f)
		}
		return "", nil

	// apply's plumbing around a stack action, for the monitor's reseed
	case strings.HasPrefix(command, "rm -f '"):
		delete(h.files, strings.Trim(strings.TrimPrefix(command, "rm -f "), "'"))
		return "", nil
	case strings.HasPrefix(command, "for n in "):
		list, _, _ := strings.Cut(strings.TrimPrefix(command, "for n in "), "; do")
		var b strings.Builder
		for _, q := range strings.Fields(list) {
			fmt.Fprintf(&b, "absent %s\n", strings.Trim(q, "'"))
		}
		return b.String(), nil
	case strings.HasPrefix(command, "for r in ") && strings.Contains(command, ".Config.Volumes"):
		list, _, _ := strings.Cut(strings.TrimPrefix(command, "for r in "), "; do")
		var b strings.Builder
		for _, q := range strings.Fields(list) {
			fmt.Fprintf(&b, "volumes %s null\n", strings.Trim(q, "'"))
		}
		return b.String(), nil
	case strings.HasPrefix(command, "for r in "):
		list, _, _ := strings.Cut(strings.TrimPrefix(command, "for r in "), "; do")
		var b strings.Builder
		for _, q := range strings.Fields(list) {
			ref := strings.Trim(q, "'")
			if h.gone["sha256:"+hexSum(ref)] {
				fmt.Fprintf(&b, "absent %s\n", ref)
				continue
			}
			fmt.Fprintf(&b, "present sha256:%s %s\n", hexSum(ref), ref)
		}
		return b.String(), nil
	case command == "docker image ls --no-trunc --format json":
		return "", nil
	case strings.HasSuffix(command, root+"/health/compose.yaml restart"):
		return "", nil

	// patroni.env on a replica, and the witness's etcd container
	case strings.Contains(command, "exec -T patroni printenv ETCD3_HOSTS"):
		if !h.patroniUp {
			return "service \"patroni\" is not running", errors.New("exit status 1")
		}
		return h.etcdHosts + "\n", nil
	case strings.HasSuffix(command, "up -d --no-deps --force-recreate patroni"):
		w.recreated = append(w.recreated, h.name)
		h.patroniUp = true
		h.etcdHosts = envLine(h.files[root+"/infra/patroni.env"], "ETCD3_HOSTS")
		// The member key keeps what the old process wrote until it expires,
		// so `patronictl list` goes on saying streaming.
		h.starting = 1
		return "", nil
	case strings.Contains(command, "exec -T patroni python3 -c") && strings.Contains(command, "/patroni"):
		if !h.patroniUp {
			return "service \"patroni\" is not running", errors.New("exit status 1")
		}
		if !strings.Contains(command, "http://"+w.cfg.Sites[h.name].Address+":8008/patroni") {
			return "urllib.error.URLError: Connection refused", errors.New("exit status 1")
		}
		if h.name == w.neverStreams || h.starting > 0 {
			h.starting--
			return "starting \n", nil
		}
		return "running streaming\n", nil
	case strings.HasPrefix(command, "ids=$(docker ps -q --filter") && strings.Contains(command, "service=etcd"):
		h.etcdContainer, h.etcdUp = false, false
		return "", nil
	case strings.HasPrefix(command, "docker ps -q --filter") && strings.Contains(command, "service=etcd"):
		if h.etcdContainer {
			return "0123456789ab\n", nil
		}
		return "", nil
	case strings.Contains(command, "/healthz"):
		if w.activePocket == h.name {
			return "active\n", nil
		}
		return "standby\n", nil

	// etcd
	case strings.Contains(command, "--quiet etcd"):
		if !h.etcdUp {
			return "__PAISANS_NO_ETCD__\n", nil
		}
		return w.memberList(h), nil
	case strings.Contains(command, "endpoint health --cluster"):
		var out []map[string]any
		failed := false
		for _, m := range w.etcd {
			site := apply.EtcdMemberSite(w.cfg, m)
			ok := !w.etcdDown[site] && w.hosts[site] != nil && w.hosts[site].etcdUp
			failed = failed || !ok
			out = append(out, map[string]any{"endpoint": m.ClientURLs[0], "health": ok})
		}
		if w.healthOut != "" {
			return w.healthOut, errors.New("exit status 1")
		}
		data, _ := json.Marshal(out)
		if failed {
			// etcdctl prints every endpoint, then exits 1 with its error on
			// stderr, which the transport's combined output carries too.
			return "Error: unhealthy cluster\n" + string(data) + "\n", errors.New("exit status 1")
		}
		return string(data), nil
	case strings.Contains(command, "member list -w json"):
		return w.memberList(h), nil
	case memberRemove.MatchString(command):
		hex := memberRemove.FindStringSubmatch(command)[1]
		var kept []apply.EtcdMember
		for _, m := range w.etcd {
			if m.HexID() == hex {
				if other := w.hosts[apply.EtcdMemberSite(w.cfg, m)]; other != nil {
					other.etcdUp = false
				}
				continue
			}
			kept = append(kept, m)
		}
		w.etcd = kept
		return "Member removed\n", nil
	case memberDel.MatchString(command):
		name := memberDel.FindStringSubmatch(command)[1]
		var kept []map[string]any
		for _, m := range w.members {
			if m["Member"] != name {
				kept = append(kept, m)
			}
		}
		w.members = kept
		return "1\n", nil

	// Patroni
	case strings.Contains(command, "--quiet patroni"):
		if !h.patroniUp {
			return "__PAISANS_NO_PATRONI__\n", nil
		}
		w.failover()
		return w.patroniList(), nil
	case strings.Contains(command, "patronictl") && strings.Contains(command, "list -f json"):
		w.failover()
		return w.patroniList(), nil
	case strings.Contains(command, "show-config"):
		return w.patronictlOut(fmt.Sprintf("loop_wait: 10\nsynchronous_mode: %v\n", w.syncMode)), nil
	case strings.Contains(command, "edit-config"):
		w.syncMode = false
		for _, m := range w.members {
			if m["Role"] == "Sync Standby" {
				m["Role"] = "Replica"
			}
		}
		return "", nil
	case switchover.MatchString(command):
		sw := switchover.FindStringSubmatch(command)
		for _, m := range w.members {
			switch m["Member"] {
			case sw[1]:
				m["Role"], m["State"] = "Sync Standby", "streaming"
			case sw[2]:
				m["Role"], m["State"] = "Leader", "running"
			default:
				if m["Role"] == "Sync Standby" {
					m["Role"] = "Replica"
				}
			}
		}
		return "Successfully switched over\n", nil
	case strings.HasSuffix(command, "stop etcd; fi"):
		h.etcdUp = false
		return "", nil
	case strings.HasSuffix(command, "stop patroni; fi"):
		h.patroniUp = false
		wasSync := false
		for _, m := range w.members {
			if m["Member"] == h.name {
				wasSync = m["Role"] == "Sync Standby"
				m["Role"], m["State"] = "Replica", "stopped"
			}
		}
		if wasSync && w.syncMode {
			for _, m := range w.members {
				if m["Role"] == "Replica" && m["State"] == "streaming" {
					m["Role"] = "Sync Standby"
					break
				}
			}
		}
		return "", nil

	// Garage
	case strings.Contains(command, "exec -T garage /garage "):
		return w.garage(h, command[strings.Index(command, "/garage ")+len("/garage "):])

	// the mesh
	case strings.Contains(command, "ip -br addr show dev psns-f2a9"):
		if !h.wgUp {
			return "", nil
		}
		address := ""
		for _, line := range strings.Split(h.files["/srv/paisans/f2a9/psns-f2a9.conf"], "\n") {
			if v, ok := strings.CutPrefix(line, "Address = "); ok {
				address = v
			}
		}
		return "psns-f2a9        UNKNOWN        " + address + "\n", nil
	case command == wgHelper+"sync", command == wgHelper+"up", command == wgHelper+"restart":
		running := h.files[wgInstalled]
		h.wgRun = &running
		if command != wgHelper+"sync" {
			h.wgUp = true
		}
		return "", nil
	case strings.Contains(command, "getent ahosts"):
		// Every peer's endpoint name resolves.
		return "", nil
	case command == wgHelper+"show public-key":
		m := regexp.MustCompile(`(?m)^PrivateKey = (\S+)`).FindStringSubmatch(h.runningWG())
		if m == nil {
			return "", fmt.Errorf("%s: unable to access interface: No such device", h.name)
		}
		key, err := render.PublicKey(m[1])
		if err != nil {
			return "", err
		}
		return key + "\n", nil
	case command == wgHelper+"show peers":
		var b strings.Builder
		for _, m := range peerRe.FindAllStringSubmatch(h.runningWG(), -1) {
			b.WriteString(m[1] + "\n")
		}
		return b.String(), nil
	case command == wgHelper+"show latest-handshakes":
		var b strings.Builder
		for _, m := range peerRe.FindAllStringSubmatch(h.runningWG(), -1) {
			b.WriteString(m[1] + "\t1760000000\n")
		}
		return b.String(), nil

	// HAProxy and the apps around its restart
	case strings.Contains(command, "/stats;csv"):
		return w.stats(h.name)
	case strings.HasSuffix(command, "restart haproxy"):
		w.served[h.name] = h.files[root+"/infra/haproxy/haproxy.cfg"]
		return "", nil
	case strings.Contains(command, " logs --no-color --tail 30 "):
		return unhealthyLogs, nil
	case w.unhealthy != "" && strings.Contains(command, "/"+w.unhealthy+"/compose.yaml ps --all --format json"):
		return `{"Service":"app","Name":"x","State":"exited","ExitCode":1}` + "\n", nil
	case strings.Contains(command, " ps --all --format json"):
		return `{"Service":"x","Name":"x","State":"running","Health":""}` + "\n", nil
	case command == apply.ContainersRead:
		return applytest.Running(h.files), nil

	// a gateway's Caddy kept for the host owner's sites: validated and
	// reloaded inside its container, from a path the container mounts
	case strings.HasPrefix(command, "docker exec ") && strings.Contains(command, " caddy validate --config "):
		if _, err := h.caddyFile(command); err != nil {
			return err.Error(), errors.New("exit status 1")
		}
		if w.caddyBroken {
			return "Error: adapting config using caddyfile: unknown directive", errors.New("exit status 1")
		}
		return "Valid configuration\n", nil
	case strings.HasPrefix(command, "docker exec ") && strings.Contains(command, " caddy reload --config "):
		content, err := h.caddyFile(command)
		if err != nil {
			return err.Error(), errors.New("exit status 1")
		}
		h.caddyLoads = append(h.caddyLoads, content)
		return "", nil
	case strings.Contains(command, "-prune -o") && strings.Contains(command, "-printf"):
		return h.keptProbe(command), nil
	case strings.Contains(command, `case "${f##*/}" in`):
		h.deleteBesideKept(command)
		return "", nil
	case strings.Contains(command, "-type d -empty !"):
		return "", nil
	case strings.Contains(command, "caddy validate"), strings.Contains(command, "caddy reload"):
		return "", nil

	// the host stage
	case strings.Contains(command, `getent passwd "$u"`):
		if h.loginRoot {
			return "root:x:0:0:root:/root:/bin/bash\n", nil
		}
		if h.loginName != "" {
			return h.loginName + ":x:1001:1001::/home/" + h.loginName + ":/bin/bash\n", nil
		}
		return "ubuntu:x:1000:1000::/home/ubuntu:/bin/bash\n", nil
	case strings.Contains(command, `for f in `+userUnits+`/paisans-f2a9-*; do [ -e "$f" ] && echo`):
		var b strings.Builder
		for _, p := range h.sortedFiles() {
			if strings.HasPrefix(p, userUnits+"/paisans-f2a9-") {
				b.WriteString("unit " + p + "\n")
			}
		}
		return b.String(), nil
	case strings.Contains(command, `for f in `+userUnits+`/paisans-f2a9-*; do [ -e "$f" ] || continue`):
		for p := range h.files {
			if strings.HasPrefix(p, userUnits+"/paisans-f2a9-") {
				delete(h.files, p)
			}
		}
		return "", nil
	case strings.HasPrefix(command, "if [ -d '") && strings.Contains(command, "rootlesskit find '"):
		// Files hold the directories, so the empty-directories form has
		// nothing to delete.
		if !strings.Contains(command, "-type d -empty") {
			dir := quoted(command)[0]
			for p := range h.files {
				if strings.HasPrefix(p, dir+"/") {
					delete(h.files, p)
				}
			}
		}
		return "", nil
	case strings.HasPrefix(command, "d='") && strings.Contains(command, "du -sb"):
		dir := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(command, ";", 2)[0], "d='"), "'")
		return h.dirAnswer(dir), nil
	case strings.HasPrefix(command, "d='") && strings.Contains(command, "-empty -delete"):
		return "", nil
	case strings.HasPrefix(command, "rm -rf -- '"):
		dir := strings.TrimSuffix(strings.TrimPrefix(command, "rm -rf -- '"), "'")
		for p := range h.files {
			if strings.HasPrefix(p, dir+"/") {
				delete(h.files, p)
			}
		}
		return "", nil
	case strings.Contains(command, "docker ps -aq --no-trunc --filter 'label=community.paisans.deployment="+ourID+"'"):
		if strings.Contains(command, "ls -d "+sitesDir+"/*.caddy") && len(h.hostSites()) > 0 {
			for _, c := range h.containers {
				if c.Deployment == ourID && c.Service == "caddy" {
					return "a site of the host owner's is in " + sitesDir + " now", errors.New("exit status 3")
				}
			}
		}
		keep := ""
		if m := regexp.MustCompile(`grep -vxF '([^']+)'`).FindStringSubmatch(command); m != nil {
			keep = m[1]
		}
		var kept []hostcheck.Container
		for _, c := range h.containers {
			if c.Deployment != ourID || (keep != "" && c.ID == keep) {
				kept = append(kept, c)
			}
		}
		h.containers = kept
		var nets []hostcheck.Network
		for _, n := range h.networks {
			if n.Deployment != ourID {
				nets = append(nets, n)
			}
		}
		h.networks = nets
		return "", nil
	case strings.Contains(command, "docker volume ls -q --filter 'label=community.paisans.deployment="+ourID+"'"):
		var kept []hostcheck.Volume
		for _, v := range h.volumes {
			if v.Deployment != ourID {
				kept = append(kept, v)
			}
		}
		h.volumes = kept
		return "", nil
	case strings.Contains(command, "/.deployed."):
		if w.recordRace {
			return deployrecord.ChangedMarker + "\n", errors.New("exit status 1")
		}
		path := regexp.MustCompile(`f='([^']+)'`).FindStringSubmatch(command)[1]
		data, _ := base64.StdEncoding.DecodeString(regexp.MustCompile(`printf %s '([^']*)' \| base64 -d`).FindStringSubmatch(command)[1])
		h.files[path] = string(data)
		return "", nil
	case strings.HasPrefix(command, "rm -f -- '/srv/paisans/") && strings.HasSuffix(command, "/.paisans/deployed.json'"):
		delete(h.files, strings.TrimSuffix(strings.TrimPrefix(command, "rm -f -- '"), "'"))
		return "", nil
	case strings.HasPrefix(command, "for i in ") && strings.Contains(command, "docker image rm"):
		list, _, _ := strings.Cut(strings.TrimPrefix(command, "for i in "), "; do")
		var b strings.Builder
		for _, q := range strings.Fields(list) {
			id := strings.Trim(q, "'")
			used := false
			for _, c := range h.containers {
				used = used || c.Image == id
			}
			switch {
			case used:
				fmt.Fprintf(&b, "kept %s Error response from daemon: conflict: unable to delete (image is being used by running container)\n", id)
			default:
				delete(h.images, id)
				if h.gone == nil {
					h.gone = map[string]bool{}
				}
				h.gone[id] = true
				fmt.Fprintf(&b, "removed %s\n", id)
			}
		}
		return b.String(), nil
	case strings.HasPrefix(command, "f='"):
		var out strings.Builder
		for _, line := range strings.Split(strings.TrimSpace(command), "\n") {
			m := fileLine.FindStringSubmatch(line)
			if m == nil {
				return "", fmt.Errorf("unreadable files line %q", line)
			}
			content, ok := h.files[m[1]]
			switch {
			case !ok:
				fmt.Fprintf(&out, "gone %s\n", m[1])
			case sum(content) == m[2]:
				delete(h.files, m[1])
				fmt.Fprintf(&out, "removed %s\n", m[1])
			default:
				fmt.Fprintf(&out, "kept %s\n", m[1])
			}
		}
		return out.String(), nil
	case strings.HasPrefix(command, "rm -f -- '"):
		delete(h.files, strings.TrimSuffix(strings.TrimPrefix(command, "rm -f -- '"), "'"))
		return "", nil
	case strings.HasSuffix(command, " stop"), strings.HasSuffix(command, " up -d"):
		return "", nil
	}
	return "", fmt.Errorf("%s: unexpected command %q", h.name, command)
}

// runningWG is the mesh file the running interface holds.
func (h *host) runningWG() string {
	if h.wgRun != nil {
		return *h.wgRun
	}
	return h.files[wgConf]
}

// failover promotes a remaining member once the leader is cut off:
// the Sync Standby, or else the first streaming replica.
func (w *world) failover() {
	if w.cutoff == "" {
		return
	}
	defer w.keepSync()
	if w.member(w.cutoff) == nil {
		return
	}
	for _, private := range []string{w.secrets.Sites[w.cutoff].WireGuardPrivateKey, w.secrets.Sites[w.cutoff].WireGuardNextPrivateKey} {
		if private == "" {
			continue
		}
		key, err := render.PublicKey(private)
		if err != nil {
			w.t.Fatal(err)
		}
		for name, h := range w.hosts {
			if name != w.cutoff && strings.Contains(h.runningWG(), key) {
				return
			}
		}
	}
	if w.leader() != w.cutoff {
		// A cut Sync Standby stops streaming, and the leader names another.
		gone := w.member(w.cutoff)
		if gone["State"] == "stopped" {
			return
		}
		wasSync := gone["Role"] == "Sync Standby"
		gone["Role"], gone["State"] = "Replica", "stopped"
		for _, m := range w.members {
			if wasSync && w.syncMode && m["Role"] == "Replica" && m["State"] == "streaming" {
				m["Role"] = "Sync Standby"
				break
			}
		}
		return
	}
	var next map[string]any
	for _, m := range w.members {
		if m["Role"] == "Sync Standby" && m["State"] == "streaming" {
			next = m
		}
	}
	for _, m := range w.members {
		if next == nil && m["Member"] != w.cutoff && m["State"] == "streaming" {
			next = m
		}
	}
	// Replicas a hostile leader stopped streaming still run Postgres, and
	// Patroni promotes one when it is within maximum_lag_on_failover.
	for _, m := range w.members {
		if next == nil && m["Member"] != w.cutoff && m["State"] == "running" {
			next = m
		}
	}
	if next == nil {
		return
	}
	for _, m := range w.members {
		switch {
		case m["Member"] == w.cutoff:
			m["Role"], m["State"] = "Replica", "stopped"
		case m["State"] == "running":
			// It follows the new leader.
			m["State"] = "streaming"
		}
	}
	next["Role"], next["State"] = "Leader", "running"
}

// keepSync names a streaming replica the Sync Standby when synchronous mode
// is on and none is, as the leader does once its standby is gone.
func (w *world) keepSync() {
	if !w.syncMode {
		return
	}
	for _, m := range w.members {
		if m["Role"] == "Sync Standby" {
			return
		}
	}
	for _, m := range w.members {
		if m["Role"] == "Replica" && m["State"] == "streaming" {
			m["Role"] = "Sync Standby"
			return
		}
	}
}

func envLine(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// addToManifest writes a file on the host and lists it, with its checksum,
// in the deployment's manifest there, as apply would have. path is relative
// to the host's /, as manifest entries are.
func (h *host) addToManifest(t *testing.T, path, content string) {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
		t.Fatal(err)
	}
	h.files["/"+path] = content
	m.Files = append(m.Files, render.ManifestFile{Path: path, SHA256: hexSum(content), Mode: "0644"})
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	h.files[dep.Manifest()] = string(data) + "\n"
}

// setToolkitVersion records v in the deployment's manifest on the host as
// the release of the paisans that wrote it, empty for a manifest from before
// manifests recorded one.
func (h *host) setToolkitVersion(t *testing.T, v string) {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files[dep.Manifest()]), &m); err != nil {
		t.Fatal(err)
	}
	m.ToolkitVersion = v
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	h.files[dep.Manifest()] = string(data) + "\n"
}

func (h *host) sortedFiles() []string {
	var out []string
	for p := range h.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (h *host) under(dir string) bool {
	for p := range h.files {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func (h *host) dirAnswer(dir string) string {
	var bytes, n int
	top := map[string]bool{}
	for p, c := range h.files {
		if rest, ok := strings.CutPrefix(p, dir+"/"); ok {
			bytes += len(c)
			n++
			top[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	if n == 0 {
		return ""
	}
	entries := make([]string, 0, len(top))
	for e := range top {
		entries = append(entries, e)
	}
	sort.Strings(entries)
	return fmt.Sprintf("present\n%d\n%d\n%s\n", bytes, n, strings.Join(entries, "\n"))
}

// etcdView is one etcd cluster as a member of it reports it.
type etcdView struct {
	cluster uint64
	members []apply.EtcdMember
}

// memberList is `etcdctl member list -w json` on h: the world's cluster, or
// the one h's etcd founded on its own.
func (w *world) memberList(h *host) string {
	view := etcdView{w.clusterID, w.etcd}
	if h.ownCluster != nil {
		view = *h.ownCluster
	}
	header := map[string]any{}
	if view.cluster != 0 {
		header["cluster_id"] = view.cluster
	}
	data, _ := json.Marshal(map[string]any{"header": header, "members": view.members})
	return string(data)
}

// splitEtcd makes site's etcd one that founded a cluster of its own, as an
// etcd restarted on a wiped data directory with --initial-cluster-state new
// does: it lists only itself, under another cluster ID.
func (w *world) splitEtcd(site string) {
	addr := w.cfg.Sites[site].Address
	w.hosts[site].ownCluster = &etcdView{cluster: 0x5eed, members: []apply.EtcdMember{{ID: 0x9999, Name: site, PeerURLs: []string{"http://" + addr + ":2380"}, ClientURLs: []string{"http://" + addr + ":2379"}}}}
}

func (w *world) patroniList() string {
	data, _ := json.Marshal(w.members)
	return w.patronictlOut(string(data))
}

func (w *world) patronictlOut(out string) string {
	if w.patroniLog {
		return patronitest.Wrap(out)
	}
	return out
}

func (w *world) leader() string {
	for _, m := range w.members {
		if m["Role"] == "Leader" {
			return m["Member"].(string)
		}
	}
	return ""
}

func (w *world) member(name string) map[string]any {
	for _, m := range w.members {
		if m["Member"] == name {
			return m
		}
	}
	return nil
}

func (w *world) stats(site string) (string, error) {
	cfg := w.served[site]
	if !strings.Contains(cfg, "listen stats") {
		return "curl: (7) Failed to connect", errors.New("exit status 7")
	}
	var b strings.Builder
	b.WriteString("# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,\n")
	b.WriteString("postgres,FRONTEND,,,0,0,500,0,0,0,0,0,0,,,,,OPEN,\n")
	for _, sv := range listenServers(cfg) {
		proxy, name := sv[0], sv[1]
		status := "DOWN"
		if proxy != "postgres" || name == w.leader() {
			status = "UP"
		}
		fmt.Fprintf(&b, "%s,%s,0,0,0,0,100,0,0,0,,0,,0,0,0,0,%s,\n", proxy, name, status)
	}
	b.WriteString("postgres,BACKEND,0,0,0,0,50,0,0,0,0,0,,0,0,0,0,UP,\n")
	return b.String(), nil
}

// stagedChanges is what `garage layout show` prints after the current
// layout when changes are staged, as dxflrs/garage:v1.0.1 prints it
// (src/garage/cli/layout.rs, cmd_show_layout and
// print_staging_role_changes): nothing at all when none are.
func (w *world) stagedChanges() string {
	if len(w.staged) == 0 && len(w.assigned) == 0 && w.redundancy == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n==== STAGED ROLE CHANGES ====\n")
	if len(w.staged) > 0 || len(w.assigned) > 0 {
		b.WriteString("ID                Tags  Zone    Capacity\n")
		var ids []string
		for id := range w.staged {
			ids = append(ids, id)
		}
		for id := range w.assigned {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if w.staged[id] {
				fmt.Fprintf(&b, "%s  REMOVED\n", id)
			} else {
				fmt.Fprintf(&b, "%s        %s  100.0 GB\n", id, w.assigned[id])
			}
		}
		b.WriteString("\n")
	}
	if w.redundancy != "" {
		fmt.Fprintf(&b, "Zone redundancy: %s\n", w.redundancy)
	}
	b.WriteString("\n==== NEW CLUSTER LAYOUT AFTER APPLYING CHANGES ====\nID                Tags  Zone    Capacity  Usable capacity\n")
	for id, zone := range w.layout {
		if !w.staged[id] {
			fmt.Fprintf(&b, "%s  []  %s  100.0 GB  100.0 GB (100.0%%)\n", id, zone)
		}
	}
	fmt.Fprintf(&b, "\nZone redundancy: maximum\n\nTo enact the staged role changes, type:\n\n    garage layout apply --version %d\n\nYou can also revert all proposed changes with: garage layout revert\n", w.version+1)
	return b.String()
}

func (w *world) garage(h *host, cmd string) (string, error) {
	switch {
	case cmd == "layout show":
		var b strings.Builder
		b.WriteString("==== CURRENT CLUSTER LAYOUT ====\nID                Tags  Zone   Capacity  Usable capacity\n")
		var ids []string
		for id := range w.layout {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, "%s  []  %s  100.0 GB  100.0 GB (100.0%%)\n", id, w.layout[id])
		}
		fmt.Fprintf(&b, "\nZone redundancy: maximum\n\nCurrent cluster layout version: %d\n", w.version)
		b.WriteString(w.stagedChanges())
		return b.String(), nil
	case cmd == "status":
		var b strings.Builder
		b.WriteString("==== HEALTHY NODES ====\nID                Hostname  Address  Tags  Zone  Capacity  DataAvail\n")
		for id, zone := range w.layout {
			if !w.garageDown[zone] {
				fmt.Fprintf(&b, "%s  %s  10.44.0.9:3901  []  %s  100.0 GB  1 TB\n", id, zone, zone)
			}
		}
		return b.String(), nil
	case layoutRemove.MatchString(cmd):
		w.staged[layoutRemove.FindStringSubmatch(cmd)[1]] = true
		return "Role removal is staged but not yet committed.\n", nil
	case layoutApply.MatchString(cmd):
		v, _ := strconv.Atoi(layoutApply.FindStringSubmatch(cmd)[1])
		if v != w.version+1 {
			return fmt.Sprintf("Error: Invalid new layout version: expected %d", w.version+1), errors.New("exit status 1")
		}
		for id := range w.staged {
			delete(w.layout, id)
		}
		for id, zone := range w.assigned {
			w.layout[id] = zone
		}
		w.staged, w.assigned, w.redundancy = map[string]bool{}, map[string]string{}, ""
		w.version = v
		w.moving, w.resyncing = 2, 2
		return "New cluster layout with updated role assignment has been applied in cluster.\n", nil
	case strings.HasPrefix(cmd, "layout skip-dead-nodes "):
		w.skips = append(w.skips, strings.TrimPrefix(cmd, "layout skip-dead-nodes "))
		if w.moving == 0 {
			return "Error: This command cannot be called when there is only one live cluster layout version", errors.New("exit status 1")
		}
		w.moving = 0
		return "Increased the ACK tracker for node\nSuccess.\n", nil
	case cmd == "layout history":
		if w.moving > 0 {
			w.moving--
			return "==== LAYOUT HISTORY ====\nSeveral layout versions are currently live in the cluster, and data is being migrated.\n", nil
		}
		return "==== LAYOUT HISTORY ====\nYour cluster is currently in a stable state with a single live layout version.\n", nil
	case cmd == "stats":
		queue := 0
		if w.resyncing > 0 {
			w.resyncing--
			queue = 42
		}
		return fmt.Sprintf("Block manager stats:\n  resync queue length: %d\n  blocks with resync errors: 0\n", queue), nil
	}
	return "", fmt.Errorf("%s: unexpected garage command %q", h.name, cmd)
}

// inspect stands in for hostcheck.Inspect, from the fake host's maps.
func (w *world) inspect(t apply.Transport, cfg *config.Config) (*hostcheck.Inventory, error) {
	h := t.(*host)
	if w.unreachable[h.name] {
		return nil, fmt.Errorf("%s: %w", h.name, apply.ErrUnreachable)
	}
	inv := &hostcheck.Inventory{Host: h.name, Docker: hostcheck.Docker{Present: true}, ManifestPath: dep.Manifest()}
	inv.Containers = append(inv.Containers, h.containers...)
	inv.Networks = append(inv.Networks, h.networks...)
	inv.Volumes = append(inv.Volumes, h.volumes...)
	if content, ok := h.files[dep.Manifest()]; ok {
		inv.Manifest = true
		var m render.Manifest
		if err := json.Unmarshal([]byte(content), &m); err == nil {
			inv.ManifestFiles = m.Files
			inv.ManifestWebServer = m.WebServer
			inv.ManifestToolkitVersion = m.ToolkitVersion
			if inv.ManifestFiles == nil {
				inv.ManifestFiles = []render.ManifestFile{}
			}
			for _, f := range m.Files {
				if f.Path == dep.WireGuardConf() {
					inv.ManifestWireGuard = true
				}
			}
		}
	}
	if h.wgUp {
		inv.Links = []string{dep.Interface()}
	}
	for _, p := range h.sortedFiles() {
		if strings.HasPrefix(p, sitesDir+"/") && strings.HasSuffix(p, ".caddy") {
			inv.HostSites = append(inv.HostSites, p)
		}
	}
	return inv, nil
}

func (w *world) transports() map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for name, h := range w.hosts {
		out[name] = h
	}
	return out
}

func (w *world) options() siteremove.Options {
	return siteremove.Options{ConfigPath: w.configPath}
}

func (w *world) build(site string, o siteremove.Options) (*siteremove.Plan, error) {
	o.ConfigPath = w.configPath
	return siteremove.Build(w.cfg, w.secrets, site, w.transports(), o)
}

func (w *world) mustBuild(site string, o siteremove.Options) *siteremove.Plan {
	w.t.Helper()
	p, err := w.build(site, o)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func sum(s string) string { return hexSum(s) }

func hexSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func mustTime() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }

// otherKey is a second key on the login user, which no deployment's record
// lists: what an operator added by hand.
func otherKey(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " bob@example.org"
}

func fingerprint(t *testing.T, line string) string {
	k, _, err := config.ParseKeyLine(line)
	if err != nil {
		t.Fatal(err)
	}
	return k.Fingerprint
}

// worldConfig is the fixture grown to the shape a removal needs: three data
// sites, so one can go and leave a cluster of two with three etcd voters; a
// third Garage node, so replication 2 survives losing one; and the apps
// pinned to home-b moved to home-a.
func worldConfig(t *testing.T, edits ...func(string) string) (*config.Config, *config.Secrets, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	text = strings.Replace(text, "  vm:\n    roles: [gateway, witness]", `  # The third data site.
  home-c:
    roles: [data]
    address: 10.44.0.5
    public_address: 203.0.113.50
    endpoint: 203.0.113.50:51820
    ssh:
      host: home-c.local
      user: ubuntu
      keys: [alice, bob]
  vm:
    roles: [gateway, witness]`, 1)
	text = strings.Replace(text, "    address: 10.44.0.2\n", "    address: 10.44.0.2\n    public_address: 203.0.113.20\n", 1)
	text = strings.Replace(text, "cluster:\n  sites: [home-a, home-b]", "cluster:\n  sites: [home-a, home-b, home-c]", 1)
	text = strings.Replace(text, "  members: [home-a, home-b, vm]", "  members: [home-a, home-b, home-c, vm]", 1)
	text = strings.Replace(text, "    sites: [home-a, home-b]\n    replication: 2", "    sites: [home-a, home-b, home-c]\n    replication: 2", 1)
	text = strings.Replace(text, "placement: { pinned: home-b }", "placement: { pinned: home-a }", -1)
	for _, edit := range edits {
		text = edit(text)
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets.Sites["home-c"] = config.SiteSecrets{WireGuardPrivateKey: "REREREREREREREREREREREREREREREREREREREREREQ=", HeartbeatToken: "6666666666666666666666666666ffff"}
	// box is a site only some worlds declare.
	secrets.Sites["box"] = config.SiteSecrets{WireGuardPrivateKey: "RUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUU=", HeartbeatToken: "7777777777777777777777777777aaaa"}
	secrets.Sites["home-d"] = config.SiteSecrets{WireGuardPrivateKey: "RkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkY=", HeartbeatToken: "8888888888888888888888888888bbbb"}
	return cfg, secrets, path
}

// newWorld is the deployment before the removal: every site applied from
// the configuration as it stands, home-a leading with home-b its Sync
// Standby, three Garage nodes, and on home-b a site prepare's units, rules
// and key record beside things that are not this deployment's.
func newWorld(t *testing.T, edits ...func(string) string) *world {
	t.Helper()
	cfg, secrets, path := worldConfig(t, edits...)
	w := &world{t: t, cfg: cfg, secrets: secrets, configPath: path, hosts: map[string]*host{},
		etcdDown: map[string]bool{}, clusterID: 0xc1a5, syncMode: true, staged: map[string]bool{}, assigned: map[string]string{}, version: 3,
		garageDown: map[string]bool{}, served: map[string]string{}, unreachable: map[string]bool{}}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.SiteNames() {
		h := &host{w: w, name: name, files: map[string]string{}, wgUp: true, images: map[string]bool{"sha256:etcd": true}}
		w.hosts[name] = h
		var entries []render.ManifestFile
		for _, f := range rendered.Files {
			rel, ok := strings.CutPrefix(f.Path, name+"/")
			if !ok || rel == render.ManifestName {
				continue
			}
			content := applytest.Pinned(f.Content)
			h.files["/"+rel] = content
			entries = append(entries, render.ManifestFile{Path: rel, SHA256: hexSum(content), Mode: fmt.Sprintf("%04o", f.Mode)})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		if running, ok := h.files[wgConf]; ok {
			h.wgRun = &running
			h.files[wgInstalled] = running
		}
		manifest, _ := json.MarshalIndent(render.Manifest{Version: 1, Files: entries, ToolkitVersion: version.Version}, "", "  ")
		h.files[dep.Manifest()] = string(manifest) + "\n"
		if contains(cfg.Etcd.Members, name) {
			h.etcdUp, h.etcdContainer = true, true
			w.nextID++
			addr := cfg.Sites[name].Address
			w.etcd = append(w.etcd, apply.EtcdMember{ID: w.nextID * 0x1111, Name: name, PeerURLs: []string{"http://" + addr + ":2380"}, ClientURLs: []string{"http://" + addr + ":2379"}})
		}
		if contains(cfg.Cluster.Sites, name) {
			h.patroniUp = true
			h.etcdHosts = envLine(h.files[root+"/infra/patroni.env"], "ETCD3_HOSTS")
		}
		if proxy, ok := h.files[root+"/infra/haproxy/haproxy.cfg"]; ok {
			w.served[name] = proxy
		}
		reg := registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{}}
		id, e := claimOf(cfg, name, mustTime())
		reg.Deployments[id] = e
		h.files[registry.Path] = encode(t, reg)
		path, record := admitted(t, cfg, name)
		h.files[path] = record
		h.containers = append(h.containers, hostcheck.Container{Name: "paisans-f2a9-infra-etcd-1", Project: "paisans-f2a9-infra", Deployment: ourID, Image: "sha256:etcd", PID: 10})
		h.networks = append(h.networks, hostcheck.Network{Name: "bridge"})
	}
	for i, name := range cfg.Cluster.Sites {
		role, state := "Replica", "streaming"
		switch i {
		case 0:
			role, state = "Leader", "running"
		case 1:
			role = "Sync Standby"
		}
		w.members = append(w.members, map[string]any{"Member": name, "Role": role, "State": state, "Replay Lag": float64(0)})
	}
	w.layout = map[string]string{}
	for i, name := range cfg.Storage.Garage.Sites {
		w.layout[strings.Repeat(string("abcdef"[i]), 16)] = name
	}

	b := w.hosts["home-b"]
	if b == nil {
		return w
	}
	b.containers = append(b.containers,
		hostcheck.Container{Name: "paisans-f2a9-talk-app-1", Project: "paisans-f2a9-talk", Deployment: ourID, Image: "sha256:talk", PID: 11},
		hostcheck.Container{Name: "someone-elses-db", Project: "theirs", Image: "sha256:theirs"},
		hostcheck.Container{Name: "paisans-0c1d-infra-etcd-1", Project: "paisans-0c1d-infra", Deployment: otherID},
	)
	b.networks = append(b.networks,
		hostcheck.Network{Name: "paisans-f2a9-talk_default", Project: "paisans-f2a9-talk", Deployment: ourID},
		hostcheck.Network{Name: "theirs_default", Project: "theirs"},
	)
	b.images["sha256:talk"], b.images["sha256:theirs"] = true, true
	b.volumes = append(b.volumes, hostcheck.Volume{Name: "paisans-f2a9-talk_media", Project: "paisans-f2a9-talk", Deployment: ourID})
	b.files[root+"/infra/postgres/PG_VERSION"] = "18\n"
	b.files[userUnits+"/paisans-f2a9-config-daemon.service"] = "[Unit]\n"
	b.files[userUnits+"/paisans-0c1d-config-daemon.service"] = "[Unit]\n"
	b.files["/etc/systemd/system/paisans-f2a9-egress.service"] = "[Unit]\n"
	b.rules = []string{
		"allow 22/tcp comment 'paisans-f2a9: ssh, the bootstrap route'",
		"allow in on psns-f2a9 comment 'paisans-f2a9: the mesh'",
		"allow 51821/udp comment 'paisans-0c1d: wireguard'",
		"allow 8080/tcp",
	}
	bob := otherKey(t)
	b.files[keysAt] = "# managed by hand\n" + alice + "\n" + bob + "\n"
	b.files[record] = "# Keys paisans site prepare added.\n" + fingerprint(t, alice) + " alice@example.org\n"
	reg := registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{}}
	id, e := claimOf(cfg, "home-b", mustTime())
	reg.Deployments[id] = e
	reg.Deployments[otherID] = registry.Entry{Token: "0c1d", Root: "/srv/paisans/0c1d", Domain: "example.net", Site: "x", Roles: "apps", ClaimedAt: "2026-10-01T00:00:00Z", Interface: "psns-0c1d", Subnet: "10.45.0.0/24", Address: "10.45.0.2"}
	b.files[registry.Path] = encode(t, reg)
	return w
}

func encode(t *testing.T, r registry.Registry) string {
	data, err := registry.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// unhealthyLogs is the tail of the log of the exited app in an unhealthy
// stack: the line that says why, and one around it that does not.
const unhealthyLogs = `app-1  | {"level":"info","msg":"starting up"}
app-1  | {"level":"fatal","msg":"exiting","error":"database unreachable","stacktrace":"main.main\n\t/src/main.go:12"}
`

// shortAndVerbose checks how err prints: by default with each of want and
// no raw log line, and with -v with the log lines.
func shortAndVerbose(t *testing.T, err error, want ...string) {
	t.Helper()
	var plain, verbose bytes.Buffer
	ui.PrintError(&plain, err, false)
	ui.PrintError(&verbose, err, true)
	// Output is wrapped to 80 columns, so a long phrase may span lines.
	flat := strings.Join(strings.Fields(plain.String()), " ")
	for _, w := range append(want, "app: database unreachable") {
		if !strings.Contains(flat, w) {
			t.Errorf("default output lacks %q:\n%s", w, plain.String())
		}
	}
	for _, raw := range []string{"starting up", "stacktrace", "last 30 log lines"} {
		if strings.Contains(plain.String(), raw) {
			t.Errorf("default output shows the raw log (%q):\n%s", raw, plain.String())
		}
		if !strings.Contains(verbose.String(), raw) {
			t.Errorf("verbose output lacks the raw log (%q):\n%s", raw, verbose.String())
		}
	}
}

// listenServers is every `server` line in a rendered haproxy.cfg with the
// `listen` section it is in, so the fake statistics name each server under
// its own proxy, as HAProxy's do: the postgres servers and the pocket-id
// ones are separate rows.
func listenServers(cfg string) [][2]string {
	var out [][2]string
	proxy := ""
	for _, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(line, "listen ") {
			proxy = strings.TrimSpace(strings.TrimPrefix(line, "listen "))
			continue
		}
		if m := serverRe.FindStringSubmatch(line); m != nil {
			out = append(out, [2]string{proxy, m[1]})
		}
	}
	return out
}

// admitted is the path and content of the admission record site admit
// writes for site.
func admitted(t *testing.T, cfg *config.Config, site string) (string, string) {
	t.Helper()
	req, err := admission.Build(cfg, site)
	if err != nil {
		t.Fatal(err)
	}
	b, err := admission.EncodeRecord(admission.NewRecord(req, req.Grants, "", mustTime()))
	if err != nil {
		t.Fatal(err)
	}
	path, err := admission.Path(cfg.Deployment().Token())
	if err != nil {
		t.Fatal(err)
	}
	return path, string(b)
}

// claimOf is the registry entry the fixture's deployment holds a site's host
// with, read from the configuration.
func claimOf(cfg *config.Config, site string, now time.Time) (string, registry.Entry) {
	d := cfg.Deployment()
	declared := cfg.Sites[site]
	return d.ID, registry.Entry{
		Token:      d.Token(),
		Root:       d.Root(),
		Domain:     cfg.Community.Domain,
		Site:       site,
		ClaimedAt:  now.UTC().Format(time.RFC3339),
		Interface:  d.Interface(),
		ListenPort: declared.ListenPort(),
		Subnet:     cfg.Mesh.Subnet,
		Address:    declared.Address,
		Roles:      registry.JoinRoles(declared.Roles),
	}
}

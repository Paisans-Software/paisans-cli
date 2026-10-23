package siteadd_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/apply/applytest"
	"github.com/paisans-software/paisans-cli/internal/apply/etcdwaltest"
	"github.com/paisans-software/paisans-cli/internal/apply/meshfake"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/patronitest"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// world is a small deployment in maps: three hosts, an etcd membership, a
// Patroni cluster and what each HAProxy serves. Every gate in site add is
// about what a command on some host answers, so a fake that answers from
// shared state is the same test as three machines with the failures made
// reachable.
type world struct {
	t        *testing.T
	cfg      *config.Config
	secrets  *config.Secrets
	hosts    map[string]*host
	etcd     []apply.EtcdMember
	nextID   uint64
	running  map[string]bool // etcd running, by site
	patroni  map[string]bool // Patroni running, by site
	members  []map[string]any
	syncMode bool
	// served is the haproxy.cfg each site's running HAProxy read at its
	// last start.
	served map[string]string
	// etcdHosts is ETCD3_HOSTS as each site's running Patroni has it.
	etcdHosts map[string]string
	// recreated is every site whose Patroni was recreated, in order.
	recreated []string
	// starting is, by site, how many more times a recreated Patroni's REST
	// API answers that Postgres is starting.
	starting map[string]int

	// Knobs that break one thing.
	noPing bool
	// pingOut is what a failed ping prints, when noPing is set.
	pingOut string
	// clock is what date +%s prints on every host, when set.
	clock *string
	// handshakesFail is what the helper's show latest-handshakes prints,
	// failing, on every host and every time, when set.
	handshakesFail  *string
	promoteRefusals int
	// addRefusals is how many learner adds etcd refuses as an unhealthy
	// cluster, as it does until every voter has been connected five seconds.
	addRefusals int
	// etcdSick is each site whose etcd endpoint health reports unhealthy
	// though it runs.
	etcdSick map[string]bool
	// healthDrop is each site `endpoint health` leaves out, as an etcdctl
	// that answers for fewer endpoints would.
	healthDrop map[string]bool
	// patroniLog wraps every patronictl answer in the log lines patronictl
	// writes when it cannot reach one of etcd's endpoints.
	patroniLog    bool
	lags          []float64
	noSyncStandby bool
	replicaUp     bool
	failOnce      string
	// unreachable makes every command containing it fail as ssh does when
	// it cannot connect, after its retries.
	unreachable string
	// unhealthy names an app stack whose container has exited, once it has
	// been started.
	unhealthy string
	// clusterID is the cluster ID every etcd's member list reports in its
	// header; every world sets one, as etcd always does, and a test clears
	// it to model an answer without one.
	clusterID uint64
	// otherCluster is, by site, the ID of another cluster whose etcd runs on
	// that host and answers for it.
	otherCluster map[string]uint64
	// probeOut is, by site, what asking that host's etcd for its member
	// list prints, and the error it exits with when the second is set: Eg a
	// warning line before the answer, or a timeout from an etcd without a
	// quorum.
	probeOut map[string][2]string
	// restarting is each site whose etcd container restarts in a loop: it
	// is never seen running, and removing it ends the loop.
	restarting map[string]bool
	// dataUnreadable is each site whose etcd data directory cannot be
	// listed.
	dataUnreadable map[string]bool
	// dataLink is each site whose etcd data path is a symbolic link.
	dataLink map[string]bool
	// unreadable is, by site, a file reading it fails on.
	unreadable map[string]string
}

type host struct {
	w        *world
	name     string
	files    map[string]string
	commands []string
	writes   int
	wgUp     bool
	// wgRun is the mesh file the running interface was last handed, by the
	// helper's up, restart or sync, which is what its show answers from.
	wgRun string
	// dataDir is whether the host has an etcd data directory when no file
	// is in it: an empty one rather than none.
	dataDir bool
}

func (h *host) Describe() string { return h.name }

func (h *host) ReadFile(path string) (string, bool, error) {
	if h.w.unreadable[h.name] == path {
		return "", false, fmt.Errorf("%s: cat: %s: Permission denied", h.name, path)
	}
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *host) WriteFile(path, content string, mode uint32) error {
	h.files[path] = content
	h.writes++
	return nil
}

func (h *host) RunInput(command, stdin string) (string, error) {
	out, err := h.Run(command)
	if err == nil && command == wgHelper+"put" {
		h.files[wgInstalled] = stdin
	}
	return out, err
}

// etcdData is a host's etcd data directory, and etcdDB a file etcd keeps
// in it once its member has started.
const (
	etcdData = "/srv/paisans/f2a9/infra/etcd"
	etcdDB   = etcdData + "/member/snap/db"
	etcdWAL  = etcdData + "/member/wal/0000000000000000-0000000000000000.wal"
)

// wgHelper is the start of every command sent to the WireGuard helper for
// the fixture's interface, and wgInstalled is where its put installs the
// mesh file on a real host.
const (
	wgHelper    = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	wgInstalled = "/etc/wireguard/psns-f2a9.conf"
)

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
	memberAdd       = regexp.MustCompile(`member add (\S+) --peer-urls=(\S+) --learner`)
	memberRemove    = regexp.MustCompile(`member remove ([0-9a-f]+)`)
	etcdRemovedLine = regexp.MustCompile(`grep -qxF '([^']+)'`)
	promote         = regexp.MustCompile(`member promote ([0-9a-f]+)`)
	emptyLoop       = regexp.MustCompile(`for f in '([^']+)'/\*`)
	initialRe       = regexp.MustCompile(`--initial-cluster=(\S+)`)
	serverRe        = regexp.MustCompile(`(?m)^\s+server (\S+) `)
)

func (h *host) Run(command string) (string, error) {
	w := h.w
	h.commands = append(h.commands, command)
	if w.failOnce != "" && strings.Contains(command, w.failOnce) {
		w.failOnce = ""
		return "failed by the test", fmt.Errorf("exit status 1")
	}
	if out, ok, err := meshfake.ChainedRead(command, h.Run); ok {
		return out, err
	}
	if field, ok := strings.CutPrefix(command, wgHelper+"show "); ok && field != "latest-handshakes" {
		if out, ok := meshfake.ShowRunning(field, h.wgRun); ok {
			return out, nil
		}
	}
	if w.unreachable != "" && strings.Contains(command, w.unreachable) {
		return "ssh: connect to host 203.0.113.10 port 22: Operation timed out\n",
			fmt.Errorf("%s: %w after 3 attempts: exit status 255", h.name, apply.ErrUnreachable)
	}
	switch {
	case strings.HasPrefix(command, "rm -f '"):
		delete(h.files, strings.Trim(strings.TrimPrefix(command, "rm -f "), "'"))
		return "", nil
	case strings.HasPrefix(command, "for n in "):
		// How each stack's compose network is addressed: no stack has one
		// yet, in this world.
		list, _, _ := strings.Cut(strings.TrimPrefix(command, "for n in "), "; do")
		var b strings.Builder
		for _, q := range strings.Fields(list) {
			fmt.Fprintf(&b, "absent %s\n", strings.Trim(q, "'"))
		}
		return b.String(), nil
	case strings.Contains(command, "echo paisans-sweep-end"):
		// The sweep's read: no image beyond the ones rendered, each present,
		// and no container.
		_, loop, _ := strings.Cut(command, "for r in ")
		list, _, _ := strings.Cut(loop, "; do")
		var b strings.Builder
		for _, q := range strings.Fields(list) {
			ref := strings.Trim(q, "'")
			d := sha256.Sum256([]byte(ref))
			fmt.Fprintf(&b, "present sha256:%s %s\n", hex.EncodeToString(d[:]), ref)
		}
		return b.String() + "paisans-sweep-end\n", nil
	case strings.HasPrefix(command, "for r in ") && strings.Contains(command, ".Config.Volumes"):
		// What each image declares: nothing, for every image in this world.
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
			d := sha256.Sum256([]byte(ref))
			fmt.Fprintf(&b, "present sha256:%s %s\n", hex.EncodeToString(d[:]), ref)
		}
		return b.String(), nil
	case strings.Contains(command, "docker info --format"):
		return "/var/lib/docker\n", nil
	case strings.HasPrefix(command, "df -B1"):
		return "Avail\n999999999999\n", nil
	case strings.Contains(command, " logs --no-color --tail 30 "):
		return unhealthyLogs, nil
	case w.unhealthy != "" && strings.Contains(command, "/srv/paisans/f2a9/"+w.unhealthy+"/compose.yaml ps --all --format json"):
		return `{"Service":"app","Name":"x","State":"exited","ExitCode":1}` + "\n", nil
	case strings.Contains(command, " ps --all --format json"):
		return `{"Service":"x","Name":"x","State":"running","Health":""}` + "\n", nil
	case command == apply.ContainersRead:
		return applytest.Running(h.files), nil
	case strings.Contains(command, "ip -br addr show dev psns-f2a9"):
		if !h.wgUp {
			return "", nil
		}
		address := ""
		for _, line := range strings.Split(h.wgRun, "\n") {
			if v, ok := strings.CutPrefix(line, "Address = "); ok {
				address = v
			}
		}
		return "psns-f2a9        UNKNOWN        " + address + "\n", nil
	case command == wgHelper+"up", command == wgHelper+"restart":
		h.wgUp, h.wgRun = true, h.files[wgInstalled]
		return "", nil
	case command == wgHelper+"sync":
		h.wgRun = h.files[wgInstalled]
		return "", nil
	case command == wgHelper+"put":
		return "", nil
	case command == "date +%s":
		if w.clock != nil {
			return *w.clock, nil
		}
		return "1000\n", nil
	case strings.HasPrefix(command, "wg "), strings.Contains(command, "wg-quick"):
		return "", fmt.Errorf("%s: %q reached the mesh without the WireGuard helper", h.name, command)
	case strings.HasPrefix(command, "ping "):
		if w.noPing {
			if w.pingOut != "" {
				return w.pingOut, fmt.Errorf("exit status 1")
			}
			return "100% packet loss", fmt.Errorf("exit status 1")
		}
		return "3 received", nil
	case command == wgHelper+"show latest-handshakes":
		if w.handshakesFail != nil {
			return *w.handshakesFail, fmt.Errorf("exit status 1")
		}
		return w.handshakes(h), nil
	case strings.Contains(command, "/.paisans/etcd-removed'; touch"):
		line := etcdRemovedLine.FindStringSubmatch(command)[1]
		f := "/srv/paisans/f2a9/.paisans/etcd-removed"
		if !strings.Contains("\n"+h.files[f], "\n"+line+"\n") {
			h.files[f] += line + "\n"
		}
		return "", nil
	case strings.HasPrefix(command, "ids=$(docker ps -aq") && !strings.Contains(command, "rootlesskit"):
		// The etcd container stopped and removed, alone.
		w.running[h.name] = false
		delete(w.restarting, h.name)
		return "", nil
	case strings.HasPrefix(command, "rootlesskit sh -c ") && strings.Contains(command, "paisans-etcd-data"):
		return h.etcdDataAnswer(), nil
	case strings.Contains(command, "--quiet etcd"):
		if r, ok := w.probeOut[h.name]; ok {
			if r[1] != "" {
				return r[0], fmt.Errorf("%s", r[1])
			}
			return r[0], nil
		}
		if id, ok := w.otherCluster[h.name]; ok {
			data, _ := json.Marshal(map[string]any{"header": map[string]any{"cluster_id": id}, "members": []apply.EtcdMember{{ID: 0xabc, Name: h.name, PeerURLs: []string{render.EtcdPeerURL(w.cfg.Sites[h.name].Address)}}}})
			return string(data), nil
		}
		if !w.running[h.name] {
			return "__PAISANS_NO_ETCD__\n", nil
		}
		return w.memberList(), nil
	case strings.Contains(command, "member list -w json"):
		return w.memberList(), nil
	case memberAdd.MatchString(command) && w.addRefusals > 0:
		w.addRefusals--
		return "Error: etcdserver: unhealthy cluster", fmt.Errorf("exit status 1")
	case memberRemove.MatchString(command):
		id := memberRemove.FindStringSubmatch(command)[1]
		kept := w.etcd[:0]
		for _, m := range w.etcd {
			if m.HexID() != id {
				kept = append(kept, m)
			}
		}
		w.etcd = kept
		return "Member removed\n", nil
	case memberAdd.MatchString(command):
		m := memberAdd.FindStringSubmatch(command)
		w.nextID++
		w.etcd = append(w.etcd, apply.EtcdMember{ID: w.nextID, PeerURLs: []string{m[2]}, IsLearner: true})
		return "Member added\n", nil
	case strings.Contains(command, `rootlesskit rm -rf -- "$f"`):
		// The etcd container stopped and removed, when the command says
		// so, and every file under each directory a loop empties deleted.
		if strings.Contains(command, "docker stop $ids") {
			w.running[h.name] = false
			delete(w.restarting, h.name)
		}
		for _, m := range emptyLoop.FindAllStringSubmatch(command, -1) {
			for p := range h.files {
				if strings.HasPrefix(p, m[1]+"/") {
					delete(h.files, p)
				}
			}
		}
		return "", nil
	case strings.HasSuffix(command, "up -d etcd"):
		w.running[h.name] = true
		// etcd starts as the member its data records, and the cluster
		// refuses one it removed: it exits and restarts in a loop, and the
		// learner never starts. With no data it joins only when
		// --initial-cluster is the membership as it stands.
		if db, ok := h.files[etcdDB]; ok && db != w.memberRecord(h.name) {
			return "", nil
		}
		if _, ok := h.files[etcdDB]; !ok {
			m := initialRe.FindStringSubmatch(h.files["/srv/paisans/f2a9/infra/compose.yaml"])
			if m == nil || m[1] != w.membership() {
				return "", nil
			}
		}
		for i := range w.etcd {
			if w.etcd[i].PeerURLs[0] == render.EtcdPeerURL(w.cfg.Sites[h.name].Address) {
				w.etcd[i].Name = h.name
				w.etcd[i].ClientURLs = []string{"http://" + w.cfg.Sites[h.name].Address + ":2379"}
				h.files[etcdDB] = w.memberRecord(h.name)
				h.files[etcdWAL] = string(etcdwaltest.Head(w.etcd[i].ID, w.clusterID))
			}
		}
		return "", nil
	case promote.MatchString(command):
		if w.promoteRefusals > 0 {
			w.promoteRefusals--
			return "etcdserver: can only promote a learner member which is in sync with leader", fmt.Errorf("exit status 1")
		}
		id := promote.FindStringSubmatch(command)[1]
		for i := range w.etcd {
			if w.etcd[i].HexID() == id {
				w.etcd[i].IsLearner = false
			}
		}
		return "promoted\n", nil
	case strings.Contains(command, "endpoint health --cluster"):
		out := []map[string]any{}
		failed := false
		for _, m := range w.etcd {
			site := apply.EtcdMemberSite(w.cfg, m)
			if w.healthDrop[site] {
				continue
			}
			ok := w.running[site] && !w.etcdSick[site]
			failed = failed || !ok
			out = append(out, map[string]any{"endpoint": "http://" + w.cfg.Sites[site].Address + ":2379", "health": ok})
		}
		data, _ := json.Marshal(out)
		if failed {
			// etcdctl prints every endpoint, then exits 1 with its error on
			// stderr, which the transport's combined output carries too.
			return "Error: unhealthy cluster\n" + string(data) + "\n", fmt.Errorf("exit status 1")
		}
		return string(data), nil
	case strings.Contains(command, "exec -T patroni printenv ETCD3_HOSTS"):
		if !w.patroni[h.name] {
			return "service \"patroni\" is not running", fmt.Errorf("exit status 1")
		}
		return w.etcdHosts[h.name] + "\n", nil
	case strings.HasSuffix(command, "up -d --no-deps --force-recreate patroni"):
		w.recreated = append(w.recreated, h.name)
		w.etcdHosts[h.name] = envLine(h.files["/srv/paisans/f2a9/infra/patroni.env"], "ETCD3_HOSTS")
		// The member key keeps what the old process wrote until it expires,
		// so `patronictl list` goes on saying streaming.
		w.starting[h.name] = 1
		return "", nil
	case strings.Contains(command, "exec -T patroni python3 -c") && strings.Contains(command, "/patroni"):
		if !w.patroni[h.name] || !strings.Contains(command, "http://"+w.cfg.Sites[h.name].Address+":8008/patroni") {
			return "urllib.error.URLError: Connection refused", fmt.Errorf("exit status 1")
		}
		if w.starting[h.name] > 0 {
			w.starting[h.name]--
			return "starting \n", nil
		}
		return "running streaming\n", nil
	case strings.Contains(command, "--quiet patroni"):
		if !w.patroni[h.name] {
			return "__PAISANS_NO_PATRONI__\n", nil
		}
		return w.patroniList(), nil
	case strings.Contains(command, "patronictl") && strings.Contains(command, "list -f json"):
		return w.patroniList(), nil
	case strings.Contains(command, "show-config"):
		return w.patronictlOut(fmt.Sprintf("loop_wait: 10\nsynchronous_mode: %v\n", w.syncMode)), nil
	case strings.Contains(command, "edit-config"):
		w.syncMode = true
		if !w.noSyncStandby {
			for _, m := range w.members {
				if m["Role"] == "Replica" {
					m["Role"] = "Sync Standby"
				}
			}
		}
		return "", nil
	case strings.HasSuffix(command, "compose.yaml up -d"):
		if h.name == "home-b" && !w.patroni["home-b"] {
			w.patroni["home-b"] = true
			w.members = append(w.members, map[string]any{"Member": "home-b", "Role": "Replica", "State": "streaming", "Replay Lag": float64(0)})
		}
		return "", nil
	case strings.HasSuffix(command, "restart haproxy"):
		w.served[h.name] = h.files["/srv/paisans/f2a9/infra/haproxy/haproxy.cfg"]
		return "", nil
	case strings.Contains(command, "/stats;csv"):
		return w.stats(h.name)
	}
	return "", nil
}

func envLine(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

// etcdDataAnswer is what apply.EtcdDataProbe prints on this host, read from
// its files: what its etcd data path is, every path under it with its type
// and size, the directories those paths imply among them, and the head of
// every write-ahead log as od prints it.
func (h *host) etcdDataAnswer() string {
	const mark = "paisans-etcd-data: "
	w := h.w
	switch {
	case w.dataLink[h.name]:
		return mark + "link\n"
	case w.dataUnreadable[h.name]:
		return mark + "unreadable\n"
	}
	if _, ok := h.files[etcdData]; ok {
		return mark + "notdir\n"
	}
	entries := map[string]string{}
	for p, content := range h.files {
		rest, ok := strings.CutPrefix(p, etcdData+"/")
		if !ok {
			continue
		}
		if link, ok := strings.CutPrefix(content, "symlink:"); ok && link != "" {
			entries[rest] = "l 0 " + rest
		} else {
			entries[rest] = fmt.Sprintf("f %d %s", len(content), rest)
		}
		for d := path.Dir(rest); d != "."; d = path.Dir(d) {
			entries[d] = "d 0 " + d
		}
	}
	if len(entries) == 0 && !h.dataDir {
		return mark + "absent\n"
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("[rootlesskit:parent] a line the probe ignores\n" + mark + "listing\npaisans-list-begin\n")
	for _, n := range names {
		b.WriteString(entries[n] + "\n")
	}
	b.WriteString("paisans-list-end\n")
	for _, n := range names {
		name, ok := strings.CutPrefix(n, "member/wal/")
		if !ok || !strings.HasSuffix(name, ".wal") || strings.HasPrefix(entries[n], "l ") {
			continue
		}
		head := []byte(h.files[etcdData+"/"+n])
		if len(head) > 4096 {
			head = head[:4096]
		}
		fmt.Fprintf(&b, "paisans-wal-begin %s\n", name)
		for i := 0; i < len(head); i += 16 {
			for _, c := range head[i:min(i+16, len(head))] {
				fmt.Fprintf(&b, " %02x", c)
			}
			b.WriteString("\n")
		}
		b.WriteString("paisans-wal-end\n")
	}
	if e, ok := entries["member/snap/db"]; ok && strings.HasPrefix(e, "f ") && !strings.HasPrefix(entries["member"], "l ") && !strings.HasPrefix(entries["member/snap"], "l ") {
		head := []byte(h.files[etcdDB])
		if len(head) > 8192 {
			head = head[:8192]
		}
		b.WriteString("paisans-db-begin\n")
		for i := 0; i < len(head); i += 16 {
			for _, c := range head[i:min(i+16, len(head))] {
				fmt.Fprintf(&b, " %02x", c)
			}
			b.WriteString("\n")
		}
		b.WriteString("paisans-db-end\n")
	}
	return b.String()
}

// memberRecord is what a site's etcd data holds once its member has
// started: the ID etcd knows it by.
func (w *world) memberRecord(site string) string {
	for _, m := range w.etcd {
		if apply.EtcdMemberSite(w.cfg, m) == site {
			return "member " + m.HexID()
		}
	}
	return ""
}

// membership is etcd's members as an --initial-cluster value.
func (w *world) membership() string {
	var parts []string
	for _, m := range w.etcd {
		parts = append(parts, apply.EtcdMemberSite(w.cfg, m)+"="+m.PeerURLs[0])
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (w *world) memberList() string {
	header := map[string]any{}
	if w.clusterID != 0 {
		header["cluster_id"] = w.clusterID
	}
	data, _ := json.Marshal(map[string]any{"header": header, "members": w.etcd})
	return string(data)
}

func (w *world) patroniList() string {
	for _, m := range w.members {
		if m["Member"] == "home-b" && len(w.lags) > 0 {
			m["Replay Lag"] = w.lags[0]
			if len(w.lags) > 1 {
				w.lags = w.lags[1:]
			}
		}
	}
	data, _ := json.Marshal(w.members)
	return w.patronictlOut(string(data))
}

func (w *world) patronictlOut(out string) string {
	if w.patroniLog {
		return patronitest.Wrap(out)
	}
	return out
}

func (w *world) stats(site string) (string, error) {
	cfg := w.served[site]
	if !strings.Contains(cfg, "listen stats") {
		return "curl: (7) Failed to connect", fmt.Errorf("exit status 7")
	}
	var b strings.Builder
	b.WriteString("# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,\n")
	b.WriteString("postgres,FRONTEND,,,0,0,500,0,0,0,0,0,0,,,,,OPEN,\n")
	for _, sv := range listenServers(cfg) {
		proxy, name := sv[0], sv[1]
		status := "DOWN"
		if proxy != "postgres" || name == "home-a" || w.replicaUp {
			status = "UP"
		}
		fmt.Fprintf(&b, "%s,%s,0,0,0,0,100,0,0,0,,0,,0,0,0,0,%s,\n", proxy, name, status)
	}
	b.WriteString("postgres,BACKEND,0,0,0,0,50,0,0,0,0,0,,0,0,0,0,UP,\n")
	return b.String(), nil
}

// handshakes answers the helper's show latest-handshakes from what each
// interface runs: two sites have a fresh handshake when each runs the
// other's key as a peer and both interfaces are up.
func (w *world) handshakes(h *host) string {
	var b strings.Builder
	mine := h.wgRun
	for _, name := range w.cfg.SiteNames() {
		if name == h.name {
			continue
		}
		other := w.hosts[name]
		if other == nil {
			continue
		}
		key := w.key(name)
		if !strings.Contains(mine, key) {
			continue
		}
		stamp := 0
		if h.wgUp && other.wgUp && strings.Contains(other.wgRun, w.key(h.name)) {
			stamp = 990
		}
		fmt.Fprintf(&b, "%s\t%d\n", key, stamp)
	}
	return b.String()
}

func (w *world) key(site string) string {
	k, err := render.PublicKey(w.secrets.Sites[site].WireGuardPrivateKey)
	if err != nil {
		w.t.Fatal(err)
	}
	return k
}

func (w *world) transports() map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for name, h := range w.hosts {
		out[name] = h
	}
	return out
}

func loadFixture(t *testing.T) (*config.Config, *config.Secrets) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, secrets
}

// endState is the staging shaped join: vm is the gateway and gains the
// witness, home-a is data and apps, home-b is new and data only. Every site
// has an endpoint.
func endState(t *testing.T) (*config.Config, *config.Secrets) {
	cfg, secrets := loadFixture(t)
	for name, endpoint := range map[string]string{"home-a": "198.51.100.10:51820", "home-b": "198.51.100.20:51820"} {
		s := cfg.Sites[name]
		s.Endpoint = endpoint
		cfg.Sites[name] = s
	}
	b := cfg.Sites["home-b"]
	b.Roles = []config.Role{config.RoleData}
	cfg.Sites["home-b"] = b
	for name, app := range cfg.Apps {
		if app.Placement.Site == "home-b" {
			app.Placement.Site = "home-a"
			cfg.Apps[name] = app
		}
	}
	cfg.Storage.Garage.Sites = []string{"home-a"}
	cfg.Storage.Garage.Replication = 1
	return cfg, secrets
}

// newWorld is the deployment before the join: home-a alone in the cluster
// and in etcd, vm a gateway only, watch the monitor, home-b prepared and
// empty. home-a, vm and watch were applied by a whole apply of the
// configuration as it was then.
func newWorld(t *testing.T) *world {
	t.Helper()
	cfg, secrets := endState(t)
	w := &world{t: t, cfg: cfg, secrets: secrets, hosts: map[string]*host{}, clusterID: 0xc1a5, nextID: 1,
		running: map[string]bool{"home-a": true}, patroni: map[string]bool{"home-a": true},
		served: map[string]string{}, syncMode: false, etcdHosts: map[string]string{}, starting: map[string]int{}}
	for _, name := range cfg.SiteNames() {
		w.hosts[name] = &host{w: w, name: name, files: map[string]string{}}
	}

	before, _ := endState(t)
	delete(before.Sites, "home-b")
	before.Cluster.Sites = []string{"home-a"}
	before.Etcd.Members = []string{"home-a"}
	vm := before.Sites["vm"]
	vm.Roles = []config.Role{config.RoleGateway}
	before.Sites["vm"] = vm
	rendered, err := render.Build(before, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home-a", "vm", "watch"} {
		p, err := apply.Build(name, rendered, "", w.hosts[name])
		if err != nil {
			t.Fatal(err)
		}
		if err := apply.Execute(p, w.hosts[name]); err != nil {
			t.Fatal(err)
		}
		w.hosts[name].commands = nil
		w.hosts[name].writes = 0
	}
	w.served["home-a"] = w.hosts["home-a"].files["/srv/paisans/f2a9/infra/haproxy/haproxy.cfg"]
	w.etcd = []apply.EtcdMember{{ID: 1, Name: "home-a", PeerURLs: []string{"http://10.44.0.1:2380"}, ClientURLs: []string{"http://10.44.0.1:2379"}}}
	w.members = []map[string]any{{"Member": "home-a", "Role": "Leader", "State": "running", "Replay Lag": ""}}
	return w
}

func (w *world) voters() []string {
	var out []string
	for _, m := range w.etcd {
		if !m.IsLearner {
			out = append(out, apply.EtcdMemberSite(w.cfg, m))
		}
	}
	sort.Strings(out)
	return out
}

// mustRender renders the end state as the live hosts record it.
func mustRender(t *testing.T, w *world) *render.Plan {
	t.Helper()
	var opts []render.Option
	for name, h := range w.hosts {
		if in, ok := render.ParseEtcdInitial(h.files["/"+"srv/paisans/f2a9/infra/etcd-initial"]); ok {
			opts = append(opts, render.WithEtcdInitial(name, in))
		}
	}
	rendered, err := render.Build(w.cfg, w.secrets, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return rendered
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

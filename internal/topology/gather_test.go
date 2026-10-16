package topology_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/topology"
)

// fakeHost answers each command by a substring only it contains, fails any
// it has no answer for, and records everything it was asked. A host with
// login set refuses every command with it, as ssh does.
type fakeHost struct {
	login   error
	answers map[string]string
	fail    map[string]error
	files   map[string]string
	readErr map[string]error
	ran     []string
	read    []string
}

func (h *fakeHost) Describe() string { return "community@host.example.org" }

func (h *fakeHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	if h.login != nil {
		return "", h.login
	}
	if command == "true" {
		return "", nil
	}
	for key, err := range h.fail {
		if strings.Contains(command, key) {
			return "", err
		}
	}
	for key, out := range h.answers {
		if strings.Contains(command, key) {
			return out, nil
		}
	}
	return "fake: no answer", fmt.Errorf("fake: no answer for %q", command)
}

func (h *fakeHost) RunInput(command, _ string) (string, error) { return h.Run(command) }

func (h *fakeHost) ReadFile(path string) (string, bool, error) {
	h.read = append(h.read, path)
	if h.login != nil {
		return "", false, h.login
	}
	if err := h.readErr[path]; err != nil {
		return "", false, err
	}
	content, ok := h.files[path]
	return content, ok, nil
}

func (h *fakeHost) WriteFile(string, string, uint32) error {
	return errors.New("fake: gathering writes nothing")
}

// memberList is `etcdctl member list -w json` for the fixture's three etcd
// members, home-a, home-b and home-c, by their peer URLs.
const memberList = `{"header":{"cluster_id":1,"member_id":2,"raft_term":3},"members":[` +
	`{"ID":1,"name":"home-a","peerURLs":["http://10.44.0.1:2380"],"clientURLs":["http://10.44.0.1:2379"]},` +
	`{"ID":2,"name":"home-b","peerURLs":["http://10.44.0.2:2380"],"clientURLs":["http://10.44.0.2:2379"]},` +
	`{"ID":3,"name":"home-c","peerURLs":["http://10.44.0.8:2380"],"clientURLs":["http://10.44.0.8:2379"]}]}`

// Each source is read from the first site holding it that answers; a source
// none answered for is unread; a site that did not answer is not asked for
// its record.
func TestGatherReadsEachSourceFromTheFirstThatAnswers(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	hosts := func() map[string]*fakeHost {
		out := map[string]*fakeHost{}
		for _, name := range cfg.SiteNames() {
			out[name] = &fakeHost{answers: map[string]string{"member list -w json": memberList}, files: map[string]string{}}
		}
		return out
	}
	transports := func(hs map[string]*fakeHost) map[string]apply.Transport {
		out := map[string]apply.Transport{}
		for name, h := range hs {
			out[name] = h
		}
		return out
	}
	first := cfg.Etcd.Members[0]
	if !slices.Contains(cfg.GatewaySites(), first) || len(cfg.GatewaySites()) != 1 {
		t.Fatalf("the fixture's first etcd member %s is not its one gateway %v", first, cfg.GatewaySites())
	}
	second := cfg.Etcd.Members[1]

	// The first etcd member, which is the gateway, refuses the login.
	hs := hosts()
	hs[first].login = refused
	ev := topology.Gather(cfg, transports(hs))
	if !errors.Is(ev.Login[first], apply.ErrLoginRefused) {
		t.Errorf("login %s: %v", first, ev.Login[first])
	}
	if _, ok := ev.Records[first]; ok || len(hs[first].read) > 0 {
		t.Errorf("%s did not answer and was asked for its record: %v", first, hs[first].read)
	}
	if !errors.Is(ev.Records[second].Err, admission.ErrNotAdmitted) {
		t.Errorf("%s's record: %+v", second, ev.Records[second])
	}
	if !slices.ContainsFunc(hs[second].ran, func(c string) bool { return strings.Contains(c, "member list -w json") }) {
		t.Errorf("%s was not asked for etcd's members: %v", second, hs[second].ran)
	}
	if got := ev.Joined["etcd's membership"]; !slices.Equal(got, []string{"home-a", "home-b", "home-c"}) {
		t.Errorf("etcd's membership %v", got)
	}
	for _, name := range cfg.Etcd.Members[2:] {
		if slices.ContainsFunc(hs[name].ran, func(c string) bool { return strings.Contains(c, "member list") }) {
			t.Errorf("%s was asked for etcd's members after %s answered", name, second)
		}
	}
	if len(ev.Unread) != 1 || !strings.Contains(ev.Unread[0], "the deployment record") {
		t.Errorf("with every gateway refusing, unread %v", ev.Unread)
	}
	if _, ok := ev.Joined["the deployment record"]; ok {
		t.Errorf("joined %v", ev.Joined)
	}

	// A gateway that answers with no record is a deployment never applied:
	// it lists nothing, and nothing is unread.
	hs = hosts()
	ev = topology.Gather(cfg, transports(hs))
	if len(ev.Unread) != 0 {
		t.Errorf("a gateway with no record: unread %v", ev.Unread)
	}
	if _, ok := ev.Joined["the deployment record"]; ok {
		t.Errorf("a gateway with no record: joined %v", ev.Joined)
	}

	// A gateway with a record lists its sites and apps.
	hs = hosts()
	hs[first].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: []string{"home-a", "home-b"}, Apps: []string{"talk"}})
	ev = topology.Gather(cfg, transports(hs))
	if got := ev.Joined["the deployment record"]; !slices.Equal(got, []string{"home-a", "home-b"}) || !slices.Equal(ev.DeployedApps, []string{"talk"}) {
		t.Errorf("a gateway with a record: joined %v, apps %v, unread %v", ev.Joined, ev.DeployedApps, ev.Unread)
	}
}

// hostsFor is a fake host per site of cfg, each answering etcd's member
// list, and transports is the same hosts as apply.Transport.
func hostsFor(cfg *config.Config) map[string]*fakeHost {
	out := map[string]*fakeHost{}
	for _, name := range cfg.SiteNames() {
		out[name] = &fakeHost{answers: map[string]string{"member list -w json": memberList}, fail: map[string]error{}, files: map[string]string{}, readErr: map[string]error{}}
	}
	return out
}

func transportsOf(hs map[string]*fakeHost) map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for name, h := range hs {
		out[name] = h
	}
	return out
}

// A gateway record that cannot be read leaves the record unread, even when
// another gateway's was found: the unreadable one may hold the only add of
// a site.
func TestGatherAnUnreadableRecordIsUnread(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	gw := cfg.GatewaySites()[0]
	hs := hostsFor(cfg)
	hs[gw].readErr[deployrecord.Path(d)] = errors.New("permission denied")
	ev := topology.Gather(cfg, transportsOf(hs))
	if len(ev.Unread) == 0 || !strings.Contains(ev.Unread[0], "the deployment record") || !strings.Contains(ev.Unread[0], "permission denied") {
		t.Errorf("one gateway, its record unreadable: unread %v", ev.Unread)
	}

	second := "home-b"
	site := cfg.Sites[second]
	site.Roles = append(slices.Clone(site.Roles), config.RoleGateway)
	cfg.Sites[second] = site
	if !slices.Equal(cfg.GatewaySites(), []string{gw, second}) {
		t.Fatalf("gateways %v", cfg.GatewaySites())
	}
	hs = hostsFor(cfg)
	hs[gw].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: []string{"home-a"}})
	hs[second].readErr[deployrecord.Path(d)] = errors.New("permission denied")
	ev = topology.Gather(cfg, transportsOf(hs))
	if len(ev.Unread) == 0 || !strings.Contains(strings.Join(ev.Unread, " "), second) {
		t.Errorf("one record found, one unreadable: unread %v, joined %v", ev.Unread, ev.Joined)
	}
}

// With no deployment record anywhere, etcd is the evidence of a site a
// first apply joined before the gateway recorded it: etcd answering with an
// error on every member is unread. With a record found, the record decides.
func TestGatherAFailedEtcdReadIsUnreadWithoutARecord(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	hs := hostsFor(cfg)
	for _, h := range hs {
		h.fail["member list -w json"] = errors.New("context deadline exceeded")
	}
	ev := topology.Gather(cfg, transportsOf(hs))
	if !slices.ContainsFunc(ev.Unread, func(u string) bool { return strings.Contains(u, "etcd's membership") }) {
		t.Errorf("no record, etcd failing: unread %v", ev.Unread)
	}

	hs[cfg.GatewaySites()[0]].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: []string{"home-a"}})
	ev = topology.Gather(cfg, transportsOf(hs))
	if len(ev.Unread) != 0 {
		t.Errorf("a record found, etcd failing: unread %v", ev.Unread)
	}
}

// Patroni's members are read from the first cluster site that answers.
func TestGatherReadsPatronisMembers(t *testing.T) {
	cfg := fixture(t)
	hs := hostsFor(cfg)
	first := cfg.Cluster.Sites[0]
	hs[first].login = refused
	second := cfg.Cluster.Sites[1]
	hs[second].answers["/cluster"] = `{"members":[{"name":"home-b","role":"leader","state":"running"},{"name":"home-a","role":"replica","state":"streaming"}]}`
	ev := topology.Gather(cfg, transportsOf(hs))
	if got := ev.Joined["Patroni's members"]; !slices.Equal(got, []string{"home-a", "home-b"}) {
		t.Errorf("Patroni's members %v", got)
	}
}

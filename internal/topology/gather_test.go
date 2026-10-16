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
	"github.com/paisans-software/paisans-cli/internal/render"
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
	if len(ev.Unread) != 1 || !strings.Contains(ev.Unread[0].Name, "the deployment record") || ev.Unread[0].Sites != nil {
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

// hostsFor is a fake host per site of cfg, each admitting the deployment
// with a record that covers it and answering etcd's member list, and
// transports is the same hosts as apply.Transport.
func hostsFor(t *testing.T, cfg *config.Config) map[string]*fakeHost {
	t.Helper()
	path, err := admission.Path(cfg.Deployment().Token())
	if err != nil {
		t.Fatal(err)
	}
	ev := everyoneAnswers(t, cfg)
	out := map[string]*fakeHost{}
	for _, name := range cfg.SiteNames() {
		data, err := admission.EncodeRecord(ev.Records[name].Record)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fakeHost{answers: map[string]string{"member list -w json": memberList}, fail: map[string]error{}, files: map[string]string{path: string(data)}, readErr: map[string]error{}}
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
	hs := hostsFor(t, cfg)
	hs[gw].readErr[deployrecord.Path(d)] = errors.New("permission denied")
	ev := topology.Gather(cfg, transportsOf(hs))
	if len(ev.Unread) == 0 || !strings.Contains(ev.Unread[0].Name, "the deployment record") || !strings.Contains(ev.Unread[0].Name, "permission denied") {
		t.Errorf("one gateway, its record unreadable: unread %v", ev.Unread)
	}

	second := "home-b"
	site := cfg.Sites[second]
	site.Roles = append(slices.Clone(site.Roles), config.RoleGateway)
	cfg.Sites[second] = site
	if !slices.Equal(cfg.GatewaySites(), []string{gw, second}) {
		t.Fatalf("gateways %v", cfg.GatewaySites())
	}
	hs = hostsFor(t, cfg)
	hs[gw].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: []string{"home-a"}})
	hs[second].readErr[deployrecord.Path(d)] = errors.New("permission denied")
	ev = topology.Gather(cfg, transportsOf(hs))
	if len(ev.Unread) == 0 || !strings.Contains(ev.Unread[0].Name, second) {
		t.Errorf("one record found, one unreadable: unread %v, joined %v", ev.Unread, ev.Joined)
	}
}

// etcd answering with an error on every member is unread, for the sites
// it could list, whether or not a deployment record was found: the record
// is written when a gateway is applied, after the data sites, so it lags
// what etcd lists.
func TestGatherAFailedEtcdReadIsUnread(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	hs := hostsFor(t, cfg)
	for _, h := range hs {
		h.fail["member list -w json"] = errors.New("context deadline exceeded")
	}
	etcdUnread := func(ev topology.Evidence) bool {
		return slices.ContainsFunc(ev.Unread, func(u topology.Source) bool {
			return strings.Contains(u.Name, "etcd's membership") && slices.Equal(u.Sites, cfg.Etcd.Members)
		})
	}
	ev := topology.Gather(cfg, transportsOf(hs))
	if !etcdUnread(ev) {
		t.Errorf("no record, etcd failing: unread %+v", ev.Unread)
	}
	hs[cfg.GatewaySites()[0]].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: []string{"home-a"}})
	ev = topology.Gather(cfg, transportsOf(hs))
	if !etcdUnread(ev) {
		t.Errorf("a record found, etcd failing: unread %+v", ev.Unread)
	}
}

// Review Focus, with a record that lags etcd: the record lists every site
// but home-c, which etcd would list, etcd errors on every member, and home-c
// does not answer. The run refuses rather than take home-c for pending.
func TestALaggingRecordWithEtcdUnreadRefuses(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	hs := hostsFor(t, cfg)
	for _, h := range hs {
		h.fail["member list -w json"] = errors.New("context deadline exceeded")
	}
	var sites []string
	for _, name := range cfg.SiteNames() {
		if name != "home-c" {
			sites = append(sites, name)
		}
	}
	hs[cfg.GatewaySites()[0]].files[deployrecord.Path(d)] = deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: sites})
	hs["home-c"].login = refused
	_, err := topology.Classify(cfg, topology.Gather(cfg, transportsOf(hs)))
	if err == nil || !strings.Contains(err.Error(), "home-c") || !strings.Contains(err.Error(), "etcd's membership") {
		t.Errorf("got %v", err)
	}
}

// A first apply that founded one etcd member and stopped: that member's
// `member list` hangs until a second joins, and the others run no etcd. Its
// founding record lists it as joined; sites etcd could never list are still
// pending, and a voter nothing lists refuses.
func TestGatherAFoundingMemberAlone(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	hs := hostsFor(t, cfg)
	for name, h := range hs {
		h.answers["member list -w json"] = "__PAISANS_NO_ETCD__"
		if name == "home-b" {
			h.fail["member list -w json"] = errors.New("context deadline exceeded")
			h.files["/"+render.EtcdInitialPath(d)] = render.FormatEtcdInitial(render.EtcdInitial{State: render.EtcdStateNew, Cluster: "home-b=http://10.44.0.2:2380"})
		}
	}
	hs["status"].login = refused
	ev := topology.Gather(cfg, transportsOf(hs))
	listed := false
	for _, sites := range ev.Joined {
		listed = listed || slices.Contains(sites, "home-b")
	}
	if !listed {
		t.Errorf("the founding member is not listed: joined %v", ev.Joined)
	}
	c, err := topology.Classify(cfg, ev)
	if err != nil || c.Sites["status"].Class != topology.Pending {
		t.Errorf("a site etcd could never list: %+v %v", c, err)
	}
	hs["home-c"].login = refused
	_, err = topology.Classify(cfg, topology.Gather(cfg, transportsOf(hs)))
	if err == nil || !strings.Contains(err.Error(), "home-c") {
		t.Errorf("a voter nothing lists: %v", err)
	}
}

// Patroni's members are read from the first cluster site that answers.
func TestGatherReadsPatronisMembers(t *testing.T) {
	cfg := fixture(t)
	hs := hostsFor(t, cfg)
	first := cfg.Cluster.Sites[0]
	hs[first].login = refused
	second := cfg.Cluster.Sites[1]
	hs[second].answers["/cluster"] = `{"members":[{"name":"home-b","role":"leader","state":"running"},{"name":"home-a","role":"replica","state":"streaming"}]}`
	ev := topology.Gather(cfg, transportsOf(hs))
	if got := ev.Joined["Patroni's members"]; !slices.Equal(got, []string{"home-a", "home-b"}) {
		t.Errorf("Patroni's members %v", got)
	}
}

// recordLacking is a deployment record listing every site of cfg but one.
func recordLacking(cfg *config.Config, site string) string {
	var sites []string
	for _, name := range cfg.SiteNames() {
		if name != site {
			sites = append(sites, name)
		}
	}
	return deployrecord.Encode(deployrecord.Record{Version: deployrecord.Version, Sites: sites})
}

// An etcd that does not run on a host that has run one, every site
// rebooted with the user's Docker not yet up, is no answer: with no voter's
// etcd running and a record that lags, a joined voter that does not answer
// or is not admitted refuses the run. A host that never ran etcd is an
// answer, so a fresh deployment still goes on.
func TestGatherAStoppedEtcdThatHasRunIsUnread(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	initial := render.FormatEtcdInitial(render.EtcdInitial{State: render.EtcdStateExisting, Cluster: "home-a=http://10.44.0.1:2380,home-b=http://10.44.0.2:2380,home-c=http://10.44.0.8:2380"})
	stopped := func(withRecords bool) map[string]*fakeHost {
		hs := hostsFor(t, cfg)
		for _, name := range cfg.Etcd.Members {
			hs[name].answers["member list -w json"] = "__PAISANS_NO_ETCD__"
			if withRecords {
				hs[name].files["/"+render.EtcdInitialPath(d)] = initial
			}
		}
		return hs
	}
	for _, how := range []string{"refused", "not admitted"} {
		hs := stopped(true)
		hs[cfg.GatewaySites()[0]].files[deployrecord.Path(d)] = recordLacking(cfg, "home-c")
		if how == "refused" {
			hs["home-c"].login = refused
		} else {
			hs["home-c"].files = map[string]string{}
		}
		_, err := topology.Classify(cfg, topology.Gather(cfg, transportsOf(hs)))
		if err == nil || !strings.Contains(err.Error(), "home-c") || !strings.Contains(err.Error(), "etcd") {
			t.Errorf("home-c %s, every etcd stopped: %v", how, err)
		}
	}
	hs := stopped(false)
	hs["home-c"].login = refused
	c, err := topology.Classify(cfg, topology.Gather(cfg, transportsOf(hs)))
	if err != nil || c.Sites["home-c"].Class != topology.Pending {
		t.Errorf("a fresh deployment: %+v %v", c, err)
	}
}

// A cluster site an explicit etcd.members leaves out is listed only by
// Patroni: a Patroni that cannot be read on a host that has run it is
// unread for the cluster's sites, so such a site that does not answer
// refuses the run. A host never given Patroni is an answer.
func TestGatherAnUnreadablePatroniIsUnread(t *testing.T) {
	cfg := fixture(t)
	d := cfg.Deployment()
	cfg.Etcd.Members = []string{"home-a", "home-b"}
	if !slices.Contains(cfg.Cluster.Sites, "home-c") {
		t.Fatalf("cluster sites %v", cfg.Cluster.Sites)
	}
	twoMembers := `{"header":{"cluster_id":1,"member_id":2,"raft_term":3},"members":[` +
		`{"ID":1,"name":"home-a","peerURLs":["http://10.44.0.1:2380"],"clientURLs":["http://10.44.0.1:2379"]},` +
		`{"ID":2,"name":"home-b","peerURLs":["http://10.44.0.2:2380"],"clientURLs":["http://10.44.0.2:2379"]}]}`
	setup := func(deployed bool) map[string]*fakeHost {
		hs := hostsFor(t, cfg)
		for _, name := range cfg.Cluster.Sites {
			hs[name].answers["member list -w json"] = twoMembers
			hs[name].fail["/cluster"] = errors.New("service \"patroni\" is not running")
			if deployed {
				hs[name].files[d.Path("infra", "patroni.env")] = "SCOPE=x\n"
			}
		}
		hs["home-c"].login = refused
		return hs
	}
	hs := setup(true)
	hs[cfg.GatewaySites()[0]].files[deployrecord.Path(d)] = recordLacking(cfg, "home-c")
	ev := topology.Gather(cfg, transportsOf(hs))
	if !slices.ContainsFunc(ev.Unread, func(u topology.Source) bool {
		return strings.Contains(u.Name, "Patroni") && slices.Equal(u.Sites, cfg.Cluster.Sites)
	}) {
		t.Errorf("unread %+v", ev.Unread)
	}
	_, err := topology.Classify(cfg, ev)
	if err == nil || !strings.Contains(err.Error(), "home-c") || !strings.Contains(err.Error(), "Patroni") {
		t.Errorf("a cluster site only Patroni lists, Patroni unread: %v", err)
	}
	c, err := topology.Classify(cfg, topology.Gather(cfg, transportsOf(setup(false))))
	if err != nil || c.Sites["home-c"].Class != topology.Pending {
		t.Errorf("Patroni never deployed: %+v %v", c, err)
	}
}

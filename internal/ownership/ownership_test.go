package ownership_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const (
	ours   = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	theirs = "0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"
)

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func container(name, id, project, service string, pid int, networks ...string) hostcheck.Container {
	return hostcheck.Container{Name: name, Deployment: id, Project: project, Service: service, PID: pid, Networks: networks}
}

// On an apps site: a stack the configuration no longer places there is left
// over, running or stopped; one it still renders, another deployment's, an
// unlabelled one and one whose project is not paisans-<token>-... are not.
// Only manifest entries marked left over are listed.
func TestLeftoversAreOnlyWhatIsProvablyOurs(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Apps, "docs")
	inv := &hostcheck.Inventory{Containers: []hostcheck.Container{
		container("paisans-f2a9-talk-app-1", ours, "paisans-f2a9-talk", "app", 10),
		container("paisans-f2a9-docs-app-1", ours, "paisans-f2a9-docs", "app", 11),
		container("paisans-f2a9-docs-redis-1", ours, "paisans-f2a9-docs", "redis", 0),
		container("paisans-f2a9-old-app-1", ours, "paisans-f2a9-old", "app", 0),
		container("paisans-0c1d-docs-app-1", theirs, "paisans-0c1d-docs", "app", 12),
		container("docs-app-1", "", "paisans-f2a9-docs", "app", 13),
		container("hand-made", ours, "shop", "app", 14),
	}}
	manifest := []render.ManifestFile{
		{Path: "srv/paisans/f2a9/talk/.env"},
		{Path: "srv/paisans/f2a9/docs/.env", Leftover: true, LeftoverSince: "2026-10-01T00:00:00Z"},
	}
	r, err := ownership.Classify(cfg, "home-a", inv, manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := []ownership.Stack{
		{Name: "docs", Project: "paisans-f2a9-docs", Containers: []string{"paisans-f2a9-docs-app-1", "paisans-f2a9-docs-redis-1"}, Running: 1},
		{Name: "old", Project: "paisans-f2a9-old", Containers: []string{"paisans-f2a9-old-app-1"}},
	}
	if !reflect.DeepEqual(r.Stacks, want) {
		t.Errorf("stacks = %+v, want %+v", r.Stacks, want)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "srv/paisans/f2a9/docs/.env" {
		t.Errorf("files = %+v", r.Files)
	}
	if !r.Leftover() || r.Foreign() {
		t.Errorf("Leftover %v, Foreign %v", r.Leftover(), r.Foreign())
	}
	lines := strings.Join(r.LeftoverLines(), "\n")
	for _, want := range []string{
		"stack docs (compose project paisans-f2a9-docs, 1 of 2 running): paisans-f2a9-docs-app-1, paisans-f2a9-docs-redis-1",
		"stack old (compose project paisans-f2a9-old, stopped): paisans-f2a9-old-app-1",
		"file /srv/paisans/f2a9/docs/.env, left over since 2026-10-01T00:00:00Z",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("lines lack %q:\n%s", want, lines)
		}
	}
}

// On the gateway, every site block in the host's own directory is foreign
// and relies on its Caddy. A foreign container is not, whatever networks it
// shares: Caddy serves only what its Caddyfile and those blocks name.
func TestForeignUsersOfTheGatewaysCaddy(t *testing.T) {
	cfg := fixture(t)
	inv := &hostcheck.Inventory{
		HostSites: []string{"/srv/paisans/f2a9/caddy.d/blog.caddy"},
		Containers: []hostcheck.Container{
			container("paisans-f2a9-infra-caddy-1", ours, "paisans-f2a9-infra", "caddy", 10, "host", "web"),
			container("paisans-f2a9-chat-app-1", ours, "paisans-f2a9-chat", "app", 11, "web"),
			container("blog-ghost-1", "", "blog", "ghost", 12, "web", "blog_default"),
			container("paisans-0c1d-talk-app-1", theirs, "paisans-0c1d-talk", "app", 13, "web"),
			container("node-exporter", "", "", "", 14, "host"),
			container("db", "", "blog", "db", 15, "blog_default"),
		},
	}
	r, err := ownership.Classify(cfg, "vm", inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Leftover() {
		t.Errorf("the gateway's own stacks read as left over: %+v", r.Stacks)
	}
	if !reflect.DeepEqual(r.HostSites, []string{"/srv/paisans/f2a9/caddy.d/blog.caddy"}) {
		t.Errorf("host sites = %v", r.HostSites)
	}
	lines := r.ForeignLines()
	if want := []string{"site block /srv/paisans/f2a9/caddy.d/blog.caddy, served by this deployment's Caddy"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("foreign lines = %q, want %q", lines, want)
	}

	// The same directory on a site whose Caddy is not the gateway's is not
	// served by this deployment.
	r, err = ownership.Classify(cfg, "home-a", inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.HostSites) != 0 {
		t.Errorf("a site without the gateway role lists %v", r.HostSites)
	}
}

func TestAnUndeclaredSiteIsAnError(t *testing.T) {
	if _, err := ownership.Classify(fixture(t), "nowhere", &hostcheck.Inventory{}, nil); err == nil {
		t.Error("Classify answered for an undeclared site")
	}
}

// The apps leftovers belong to, for `paisans app remove`: a stack's name, a
// file's stack directory, and the app a route is named for. The
// infrastructure stack is never one.
func TestLeftoversNameTheirApps(t *testing.T) {
	r := ownership.Report{
		Stacks: []ownership.Stack{{Name: "old"}, {Name: "infra"}},
		Files: []render.ManifestFile{
			{Path: "srv/paisans/f2a9/docs/.env"},
			{Path: "srv/paisans/f2a9/infra/caddy/etc/snippets/wiki-media.caddy"},
			{Path: "srv/paisans/f2a9/infra/caddy/etc/snippets/gate-gates.caddy"},
			{Path: "srv/paisans/f2a9/infra/haproxy/haproxy.cfg"},
		},
	}
	if got, want := r.Apps(), []string{"docs", "gate", "old", "wiki"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Apps = %v, want %v", got, want)
	}
}

// A container of a stack still rendered whose service its compose file no
// longer declares is left over: a removed broker beside the app that stayed.
// A declared service, another deployment's, an unlabelled container and a
// stack the render does not judge are not.
func TestUndeclaredServicesAreLeftOver(t *testing.T) {
	cfg := fixture(t)
	inv := &hostcheck.Inventory{Containers: []hostcheck.Container{
		container("paisans-f2a9-talk-app-1", ours, "paisans-f2a9-talk", "app", 10),
		container("paisans-f2a9-talk-rabbitmq-1", ours, "paisans-f2a9-talk", "rabbitmq", 11),
		container("paisans-f2a9-talk-amqproxy-1", ours, "paisans-f2a9-talk", "amqproxy", 0),
		container("paisans-f2a9-infra-patroni-1", ours, "paisans-f2a9-infra", "patroni", 12),
		container("paisans-f2a9-infra-caddy-1", ours, "paisans-f2a9-infra", "caddy", 13),
		container("paisans-0c1d-talk-rabbitmq-1", theirs, "paisans-0c1d-talk", "rabbitmq", 14),
		container("paisans-f2a9-talk-hand-1", ours, "paisans-f2a9-talk", "", 15),
		container("paisans-f2a9-docs-redis-1", ours, "paisans-f2a9-docs", "redis", 16),
	}}
	declared := map[string][]string{"talk": {"app"}, "infra": {"caddy"}}
	got := ownership.UndeclaredServices(cfg.Deployment(), inv, declared)
	want := []ownership.Service{
		{Stack: "infra", Project: "paisans-f2a9-infra", Name: "patroni", Containers: []string{"paisans-f2a9-infra-patroni-1"}, Running: 1, Kept: "a member of the Patroni cluster, which the rest of the deployment may still count on; `paisans site remove` takes a site out of it"},
		{Stack: "talk", Project: "paisans-f2a9-talk", Name: "amqproxy", Containers: []string{"paisans-f2a9-talk-amqproxy-1"}},
		{Stack: "talk", Project: "paisans-f2a9-talk", Name: "rabbitmq", Containers: []string{"paisans-f2a9-talk-rabbitmq-1"}, Running: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("services = %+v, want %+v", got, want)
	}

	r := ownership.Report{Services: got}
	if !r.Leftover() {
		t.Error("a report with only left over services is not left over")
	}
	if len(r.Apps()) != 0 {
		t.Errorf("left over services name apps to remove: %v", r.Apps())
	}
	lines := strings.Join(r.LeftoverLines(), "\n")
	for _, want := range []string{
		"service patroni of stack infra (compose project paisans-f2a9-infra, 1 of 1 running): paisans-f2a9-infra-patroni-1",
		"service amqproxy of stack talk (compose project paisans-f2a9-talk, stopped): paisans-f2a9-talk-amqproxy-1",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("lines lack %q:\n%s", want, lines)
		}
	}
}

// site prune judges against the compose files on the host. A service the
// host's file no longer declares is left over; a stack without a file is not
// judged; an infrastructure stack the manifest marks left over declares
// nothing, while an app's stack so marked is app remove's. etcd, Patroni and
// Garage are kept, with why.
func TestPruneServicesJudgesTheHostsComposeFiles(t *testing.T) {
	d := fixture(t).Deployment()
	inv := &hostcheck.Inventory{
		Containers: []hostcheck.Container{
			container("paisans-f2a9-talk-app-1", ours, "paisans-f2a9-talk", "app", 10),
			container("paisans-f2a9-talk-rabbitmq-1", ours, "paisans-f2a9-talk", "rabbitmq", 11),
			container("paisans-f2a9-infra-patroni-1", ours, "paisans-f2a9-infra", "patroni", 12),
			container("paisans-f2a9-infra-haproxy-1", ours, "paisans-f2a9-infra", "haproxy", 13),
			container("paisans-f2a9-docs-app-1", ours, "paisans-f2a9-docs", "app", 14),
			container("paisans-f2a9-wiki-app-1", ours, "paisans-f2a9-wiki", "app", 15),
			container("paisans-0c1d-infra-caddy-1", theirs, "paisans-0c1d-infra", "caddy", 16),
		},
		ManifestFiles: []render.ManifestFile{
			{Path: "srv/paisans/f2a9/infra/compose.yaml", Leftover: true},
			{Path: "srv/paisans/f2a9/docs/compose.yaml", Leftover: true},
		},
	}
	if got, want := ownership.Stacks(d, inv), []string{"docs", "infra", "talk", "wiki"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks = %v, want %v", got, want)
	}
	composes := map[string]string{
		"talk":  "services:\n  app:\n    image: x\n",
		"infra": "services:\n  patroni:\n    image: x\n",
		"docs":  "services:\n  web:\n    image: x\n",
	}
	got, err := ownership.PruneServices(d, inv, composes)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Stack+"/"+s.Name)
	}
	if want := []string{"infra/haproxy", "infra/patroni", "talk/rabbitmq"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("services = %v, want %v", names, want)
	}
	if got[0].Kept != "" || got[2].Kept != "" {
		t.Errorf("haproxy or rabbitmq kept: %q, %q", got[0].Kept, got[2].Kept)
	}
	if !strings.Contains(got[1].Kept, "a member of the Patroni cluster") || !strings.Contains(got[1].Kept, "site remove") {
		t.Errorf("patroni kept = %q", got[1].Kept)
	}

	if _, err := ownership.PruneServices(d, inv, map[string]string{"talk": "services: ["}); err == nil {
		t.Error("an unreadable compose file was judged")
	}
}

package appremove

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const (
	agentEnv         = "/srv/paisans/f2a9/infra/beszel-agent/beszel-agent.env"
	agentFingerprint = "/srv/paisans/f2a9/infra/beszel-agent/data/fingerprint"
	statsConfig      = "/srv/paisans/f2a9/stats/data/config.yml"
	infraCompose     = "/srv/paisans/f2a9/infra/compose.yaml"
)

// hubless is the beszel hub, stats, taken out of the configuration. watch,
// its site, and home-a have been applied since, so their agents' files are
// marked left over, while the agents, which no compose file names any more,
// still run: `up -d` leaves a service the file dropped running. home-b has
// not been applied, so its infra stack still renders and runs its agent.
func hubless(t *testing.T) (map[string]*host, Input, *Plan) {
	t.Helper()
	cfg, hosts := world(t)
	delete(cfg.Apps, "stats")
	delete(cfg.Apps, "notify") // it serves the hub
	agent := func(h *host, leftover bool) {
		h.files[agentEnv] = "TOKEN=x\n"
		h.files[agentFingerprint] = "f00d"
		h.files[infraCompose] = "services: {}\n"
		h.containers = append(h.containers,
			hostcheck.Container{Name: "paisans-f2a9-infra-beszel-agent-1", Deployment: ours, Project: "paisans-f2a9-infra", Service: "beszel-agent", PID: 20},
			hostcheck.Container{Name: "paisans-f2a9-infra-caddy-1", Deployment: ours, Project: "paisans-f2a9-infra", Service: "caddy", PID: 21},
		)
		entries := append(h.manifestFiles(t), h.entry(agentEnv, leftover), h.entry(agentFingerprint, leftover), h.entry(infraCompose, false))
		if h.name == "watch" {
			h.files[statsConfig] = "systems: []\n"
			entries = append(entries, h.entry(statsConfig, true))
		}
		h.manifest(t, entries...)
	}
	hosts["watch"].manifest(t)
	hosts["home-b"].manifest(t)
	agent(hosts["watch"], true)
	agent(hosts["home-a"], true)
	agent(hosts["home-b"], false)
	in := probe(t, cfg, "stats", hosts, false)
	p, err := Build(cfg, "stats", in)
	if err != nil {
		t.Fatal(err)
	}
	return hosts, in, p
}

// Removing the hub stops and removes every agent a whole apply has already
// taken out of its site's infra stack, with the agent's left over files and
// their entries, and leaves the rest of the infra stack alone. A site not
// applied since the hub left is reported owed and not touched.
func TestRemovingTheHubStopsEveryAgent(t *testing.T) {
	hosts, _, p := hubless(t)
	rec := &ui.Recorder{Verbose_: true}
	p.Show(rec)
	if !rec.Has("detail", "stop paisans-f2a9-infra-beszel-agent-1") {
		t.Errorf("the plan does not show the agent stopped:\n%s", rec.Lines())
	}
	remains := strings.Join(Remains(p, nil, nil), "\n")
	if !strings.Contains(remains, "home-b") || !strings.Contains(remains, "deployment apply --site home-b --execute") {
		t.Errorf("home-b's agent is not reported owed:\n%s", remains)
	}
	if err := executor(hosts).Execute(p); err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"watch", "home-a"} {
		h := hosts[site]
		for _, c := range h.containers {
			if c.Service == "beszel-agent" {
				t.Errorf("%s: the agent still runs", site)
			}
		}
		if !slices.ContainsFunc(h.containers, func(c hostcheck.Container) bool { return c.Service == "caddy" }) {
			t.Errorf("%s: the rest of the infra stack was removed with the agent", site)
		}
		for _, f := range []string{agentEnv, agentFingerprint} {
			if _, ok := h.files[f]; ok {
				t.Errorf("%s: %s is still there", site, f)
			}
			for _, e := range h.manifestFiles(t) {
				if "/"+e.Path == f {
					t.Errorf("%s: the manifest still records %s", site, f)
				}
			}
		}
		if _, ok := h.files[infraCompose]; !ok {
			t.Errorf("%s: the infra stack's compose file was removed", site)
		}
	}
	b := hosts["home-b"]
	if _, ok := b.files[agentEnv]; !ok || !slices.ContainsFunc(b.containers, func(c hostcheck.Container) bool { return c.Service == "beszel-agent" }) {
		t.Error("home-b, not applied since the hub left, was touched")
	}
}

// A run after the agents are gone plans nothing of them: the step is a
// no-op once done, so a stopped run resumes.
func TestRemovingTheHubAgainFindsNoAgent(t *testing.T) {
	hosts, _, p := hubless(t)
	if err := executor(hosts).Execute(p); err != nil {
		t.Fatal(err)
	}
	cfg := fixture(t)
	delete(cfg.Apps, "blog")
	delete(cfg.Apps, "stats")
	delete(cfg.Apps, "notify") // it serves the hub
	again, err := Build(cfg, "stats", probe(t, cfg, "stats", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range again.Sites {
		if s.Site != "home-b" && (len(s.Agents) > 0 || len(s.AgentFiles) > 0) {
			t.Errorf("%s still plans the agent: %+v", s.Site, s)
		}
	}
}

// While a hub is declared an agent is its, and removing another app plans
// nothing of it.
func TestAnAgentWithAHubIsLeftAlone(t *testing.T) {
	cfg, hosts := world(t)
	a := hosts["home-a"]
	a.files[agentEnv] = "TOKEN=x\n"
	a.containers = append(a.containers, hostcheck.Container{Name: "paisans-f2a9-infra-beszel-agent-1", Deployment: ours, Project: "paisans-f2a9-infra", Service: "beszel-agent", PID: 20})
	a.manifest(t, append(a.manifestFiles(t), a.entry(agentEnv, true))...)
	p, err := Build(cfg, "blog", probe(t, cfg, "blog", hosts, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Sites {
		if len(s.Agents) > 0 || len(s.AgentFiles) > 0 || s.AgentOwed {
			t.Errorf("%s plans the agent of a declared hub: %+v", s.Site, s)
		}
	}
}

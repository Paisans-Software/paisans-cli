package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// initFake is one site as init sees it: its registry, its networks, or no
// answer at all.
type initFake struct {
	name     string
	registry string
	routes   string
	down     bool
	// files are answered to ReadFile by path.
	files map[string]string
}

func (f *initFake) Run(command string) (string, error) {
	if f.down {
		return "ssh: connect to host " + f.name + " port 22: Operation timed out", errors.New("exit status 255")
	}
	switch command {
	case mesh.RouteCommand:
		if f.routes != "" {
			return f.routes, nil
		}
		return `[{"dst":"default","dev":"eth0"}]`, nil
	case mesh.AddrCommand, mesh.NetworksCommand:
		return "[]", nil
	}
	return "", errors.New("unexpected command " + command)
}

func (f *initFake) ReadFile(path string) (string, bool, error) {
	if f.down {
		return "", false, f.unreachable()
	}
	if c, ok := f.files[path]; ok && c != "" {
		return c, true, nil
	}
	if path == registry.Path && f.registry != "" {
		return f.registry, true, nil
	}
	return "", false, nil
}

func (f *initFake) Describe() string { return "ubuntu@" + f.name }

// unreachable is the error a real transport gives for a host ssh cannot
// connect to, by way of the sudo probe that runs first.
func (f *initFake) unreachable() error {
	return fmt.Errorf("asking whether sudo needs a password: %w", &ui.Problem{
		Hint:    f.Describe() + " cannot be reached over ssh",
		Explain: "ssh could not connect in 3 attempts: ssh: connect to host " + f.name + " port 22: Operation timed out. Check that the host is up, and that ssh " + f.Describe() + " from this machine reaches it.",
		Cause:   fmt.Errorf("%w after 3 attempts: exit status 255", apply.ErrUnreachable),
	})
}

const (
	exampleID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	otherID   = "0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"
)

// registryHolding is a registry with one deployment on it.
func registryHolding(t *testing.T, id, token, subnet string) string {
	t.Helper()
	data, err := registry.Encode(registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{
		id: {Token: token, Root: "/srv/paisans/" + token, Domain: "example.net", Site: "home-a", Roles: "apps", ClaimedAt: "2026-10-01T00:00:00Z", Interface: "psns-" + token, Subnet: subnet},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// initWorld copies the example declaration into a directory of its own and
// points init at fakes for its sites and at random.
func initWorld(t *testing.T, edit func(string) string, sites map[string]*initFake, random []byte) (string, *ui.Recorder) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "paisans-stack.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if edit != nil {
		body = edit(body)
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	savedHost, savedRandom := initHost, meshRandom
	initHost = func(_ string, site config.Site) registry.Runner {
		for _, f := range sites {
			if f.name == site.SSH.Host {
				return f
			}
		}
		t.Fatalf("init reached an unexpected host %q", site.SSH.Host)
		return nil
	}
	meshRandom = bytes.NewReader(random)
	t.Cleanup(func() { initHost, meshRandom = savedHost, savedRandom })
	// Every init makes an age key under the example id, and the package's
	// keystore outlives the test: put its items back as they were.
	if m, ok := config.AgeKeystore.(*keystore.Memory); ok {
		saved := map[string]keystore.MemoryItem{}
		for k, v := range m.Items {
			saved[k] = v
		}
		t.Cleanup(func() { m.Items = saved })
	}
	return path, withRecorder(t, true)
}

func fakeSites() map[string]*initFake {
	return map[string]*initFake{
		"home-a": {name: "home-a.local"},
		"home-b": {name: "home-b.local"},
		"vm":     {name: "vm.example.org"},
		"health": {name: "health.example.org"},
	}
}

func runInitQuietly(t *testing.T, path string) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = runInit([]string{"--config", path}) })
	return err
}

// The declared subnet overlaps another deployment's mesh on home-a, so init
// rolls; the first roll lands inside a route on vm and the second is clear.
// mesh.subnet and every address move, host numbers and every comment stay,
// and the output says why.
func TestInitRollsASubnetClearOfEveryHost(t *testing.T) {
	sites := fakeSites()
	sites["home-a"].registry = registryHolding(t, otherID, "0c1d", "10.44.0.0/24")
	sites["vm"].routes = `[{"dst":"10.1.0.0/16","dev":"tun0"}]`
	path, out := initWorld(t, nil, sites, []byte{1, 2, 212, 37})
	before, _ := os.ReadFile(path)

	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	want := strings.NewReplacer(
		"subnet: 10.44.0.0/24", "subnet: 10.212.37.0/24",
		"address: 10.44.0.1 ", "address: 10.212.37.1 ",
		"address: 10.44.0.2\n", "address: 10.212.37.2\n",
		"address: 10.44.0.3\n", "address: 10.212.37.3\n",
		"address: 10.44.0.4\n", "address: 10.212.37.4\n",
	).Replace(string(before))
	if string(after) != want {
		t.Errorf("init wrote:\n%s", after)
	}
	for _, line := range []string{
		"rolled mesh.subnet 10.212.37.0/24 (10.44.0.0/24 overlapped deployment " + otherID + " (example.net)'s mesh 10.44.0.0/24 on home-a; 10.1.2.0/24 overlapped route 10.1.0.0/16 dev tun0 on vm)",
		"sites.home-a.address 10.44.0.1 -> 10.212.37.1",
	} {
		if !strings.Contains(out.Lines(), line) {
			t.Errorf("init did not say %q:\n%s", line, out.Lines())
		}
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Mesh.Subnet != "10.212.37.0/24" {
		t.Fatalf("the rewritten file loads as %v, %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err != nil {
		t.Errorf("secrets were not generated after the roll: %v", err)
	}
}

// With no subnet declared, init rolls one and adds it.
func TestInitAddsAMissingSubnet(t *testing.T) {
	path, out := initWorld(t, func(s string) string {
		i := strings.Index(s, "mesh:\n")
		j := strings.Index(s, "  subnet: 10.44.0.0/24\n")
		return s[:i] + s[j+len("  subnet: 10.44.0.0/24\n"):]
	}, fakeSites(), []byte{212, 37})
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mesh.Subnet != "10.212.37.0/24" || cfg.Sites["vm"].Address != "10.212.37.3" {
		t.Errorf("subnet %s, vm %s", cfg.Mesh.Subnet, cfg.Sites["vm"].Address)
	}
	if !strings.Contains(out.Lines(), "(no mesh.subnet was declared)") {
		t.Errorf("init did not say why it rolled:\n%s", out.Lines())
	}
}

// A declared subnet that overlaps nothing is kept, and the file is not
// touched.
func TestInitKeepsAClearSubnet(t *testing.T) {
	path, out := initWorld(t, nil, fakeSites(), nil)
	before, _ := os.ReadFile(path)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || !strings.Contains(out.Lines(), "overlaps nothing on health, home-a, home-b, vm; kept") {
		t.Errorf("a clear subnet was not kept as it was:\n%s", out.Lines())
	}
}

// A site that does not answer makes init refuse with nothing written: no
// subnet, and no secrets either.
func TestInitRefusesWhenASiteIsUnreachable(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].down = true
	path, _ := initWorld(t, nil, sites, []byte{212, 37})
	before, _ := os.ReadFile(path)
	err := runInitQuietly(t, path)
	if err == nil {
		t.Fatal("init did not refuse although home-b is unreachable")
	}
	hint, explain := ui.Describe(err)
	shown := hint + "\n" + explain
	if !strings.Contains(hint, "every site") || !strings.Contains(explain, "- home-b: ubuntu@home-b.local cannot be reached over ssh") || !strings.Contains(explain, "Nothing was written") {
		t.Fatalf("init refused with:\n%s\nwant a refusal naming home-b", shown)
	}
	for _, once := range []string{"Operation timed out", "ubuntu@home-b.local cannot", "Check that the host is up"} {
		if n := strings.Count(shown, once); n != 1 {
			t.Errorf("%q appears %d times, want once:\n%s", once, n, shown)
		}
	}
	for _, noise := range []string{"sudo", "registry.json", "exit status 255"} {
		if strings.Contains(shown, noise) {
			t.Errorf("the refusal shows %q, which only --verbose should:\n%s", noise, shown)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("init changed paisans-stack.yaml although a site was unreachable")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err == nil {
		t.Error("init generated secrets although it refused")
	}
}

// Each site's ssh connection can take as long as its connect timeout, and
// longer when it is retried, so while init settles the subnet its step says
// which site it is reading: a site that does not answer is named while it is
// waited on, not only in the refusal after.
func TestInitSaysWhichSiteItIsReading(t *testing.T) {
	path, rec := initWorld(t, nil, fakeSites(), nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"health", "home-a", "home-b", "vm"} {
		if !rec.Has("progress", "read "+site) || !rec.Has("progress", "read the networks on "+site) {
			t.Errorf("settle mesh subnet did not say it was reading %s:\n%s", site, rec.Lines())
		}
	}
}

// Once any site's registry records this deployment, its subnet is deployed:
// init leaves it alone even where it now overlaps something, and an
// unreachable site no longer matters to it.
func TestInitLeavesADeployedSubnetAlone(t *testing.T) {
	sites := fakeSites()
	sites["vm"].registry = registryHolding(t, exampleID, "f2a9", "10.44.0.0/24")
	sites["home-a"].routes = `[{"dst":"10.44.0.0/16","dev":"eth1"}]`
	sites["home-b"].down = true
	path, out := initWorld(t, nil, sites, []byte{212, 37})
	before, _ := os.ReadFile(path)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Errorf("init changed a deployed subnet:\n%s", after)
	}
	if !strings.Contains(out.Lines(), "is deployed (this deployment is on vm)") {
		t.Errorf("init did not say the subnet is deployed:\n%s", out.Lines())
	}
}

// Every roll overlapping something is a refusal naming what blocked it, and
// writes nothing.
func TestInitGivesUpAfterMaxRolls(t *testing.T) {
	sites := fakeSites()
	sites["home-a"].routes = `[{"dst":"10.0.0.0/8","dev":"tun0"}]`
	path, _ := initWorld(t, nil, sites, bytes.Repeat([]byte{7, 7}, mesh.MaxRolls))
	before, _ := os.ReadFile(path)
	err := runInitQuietly(t, path)
	if err == nil || !strings.Contains(err.Error(), "route 10.0.0.0/8 dev tun0 on home-a") {
		t.Fatalf("init = %v, want a refusal naming the route", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("init wrote paisans-stack.yaml although it found no subnet")
	}
}

// apply and host prepare refuse a host where something outside this
// deployment's own interface overlaps the mesh, naming it, and pass one whose
// only mesh route is their own.
func TestCheckMeshLive(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	own := &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/24","dev":"psns-f2a9"}]`}
	if err := checkMeshLive(cfg, "home-a", own); err != nil {
		t.Errorf("the deployment's own route was refused: %v", err)
	}
	clash := &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/24","dev":"psns-f2a9"},{"dst":"10.44.0.0/23","dev":"psns-0c1d"}]`}
	err = checkMeshLive(cfg, "home-a", clash)
	if err == nil || !strings.Contains(err.Error(), "route 10.44.0.0/23 dev psns-0c1d on home-a") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("checkMeshLive = %v, want a refusal naming the other route", err)
	}
	// Tailscale keeps its routes in table 52, which the probe reads too.
	tailscale := &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/16","dev":"tailscale0","table":"52"}]`}
	err = checkMeshLive(cfg, "home-a", tailscale)
	if err == nil || !strings.Contains(err.Error(), "route 10.44.0.0/16 dev tailscale0 table 52 on home-a") {
		t.Errorf("checkMeshLive = %v, want a refusal naming Tailscale's route", err)
	}
}

// init reports the subnet decision as one step with its result, each secret
// it generates as a step named for it and never its value, and a fresh
// deployment's age key as a step of its own.
func TestInitReportsStepsAndWhatIsOwed(t *testing.T) {
	path, rec := initWorld(t, nil, fakeSites(), nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("done", "settle mesh subnet") {
		t.Errorf("no settled subnet step:\n%s", rec.Lines())
	}
	if !rec.Has("done", "write secrets") || !rec.Has("detail", "+ ") || !rec.Has("result", "Next: run `paisans secrets set`.") {
		t.Errorf("no created secrets:\n%s", rec.Lines())
	}
	if !rec.Has("done", "make age key") || rec.Has("warn", "was written in plaintext") {
		t.Errorf("a fresh init did not make a key and encrypt:\n%s", rec.Lines())
	}
	again := withRecorder(t, true)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if again.Has("done", "write secrets") || !again.Has("result", "Next: run `paisans secrets set`.") {
		t.Errorf("a second run wrote secrets, or does not name what is next:\n%s", again.Lines())
	}
}

// A plaintext secrets file that already exists stays plaintext, and says so.
func TestInitWarnsAboutAPlaintextFile(t *testing.T) {
	path, rec := initWorld(t, nil, fakeSites(), nil)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "secrets.enc.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("warn", "was written in plaintext") || rec.Has("done", "make age key") {
		t.Errorf("unexpected report:\n%s", rec.Lines())
	}
}

// A secret naming a site paisans-stack.yaml no longer declares is warned about by
// key, and init leaves it for `secrets prune`.
func TestInitWarnsAboutOrphanedSecrets(t *testing.T) {
	sites := fakeSites()
	path, out := initWorld(t, nil, sites, nil)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	sites["vm"].files = map[string]string{deployrecord.Path(cfg.Deployment()): fixtureRecord()}
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	secrets.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "not-a-real-key-0003"}
	secretsPath := filepath.Join(filepath.Dir(path), "secrets.enc.yaml")
	if err := config.WriteSecrets(secretsPath, secrets, nil); err != nil {
		t.Fatal(err)
	}
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Lines(), "secrets: sites.monitor-a names a site paisans-stack.yaml does not declare") {
		t.Errorf("no warning:\n%s", out.Lines())
	}
	if strings.Contains(out.Lines(), "not-a-real-key-0003") {
		t.Error("a secret value was printed")
	}
	if s, _ := config.LoadSecrets(secretsPath); s.Sites["monitor-a"].WireGuardPrivateKey == "" {
		t.Error("init removed the orphan")
	}
}

// A site the record lists that paisans-stack.yaml no longer declares is warned
// about, whether or not it has secrets.
func TestInitWarnsAboutADroppedSite(t *testing.T) {
	sites := fakeSites()
	path, out := initWorld(t, nil, sites, nil)
	cfg, _ := config.Load(path)
	sites["vm"].files = map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"health", "home-a", "home-b", "vm", "monitor-a"}})}
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Lines(), "sites.monitor-a is deployed but paisans-stack.yaml no longer declares it") {
		t.Errorf("no warning:\n%s", out.Lines())
	}
	secrets, err := config.LoadDeploymentSecrets(filepath.Join(filepath.Dir(path), "secrets.enc.yaml"), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range secrets.Sites {
		for _, value := range []string{s.WireGuardPrivateKey, s.HeartbeatToken} {
			if value != "" && strings.Contains(out.Lines(), value) {
				t.Errorf("a secret of sites.%s was printed", name)
			}
		}
	}
}

// A secrets file init cannot write fails its write step, naming what it
// generated, and init returns the write's own error. The file is as it was.
func TestInitFailsItsWriteStep(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read only file")
	}
	path, rec := initWorld(t, nil, fakeSites(), nil)
	secretsPath := filepath.Join(filepath.Dir(path), "secrets.enc.yaml")
	if err := os.WriteFile(secretsPath, []byte("version: 1\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	err := runInitQuietly(t, path)
	var failed *secretsWriteError
	if err == nil || errors.As(err, &failed) || !strings.Contains(err.Error(), "writing "+secretsPath) {
		t.Fatalf("init = %v, want the write's own error", err)
	}
	if !rec.Has("fail", "write secrets") || !rec.Has("detail", "+ cluster.") {
		t.Errorf("the write step did not fail naming what it generated:\n%s", rec.Lines())
	}
	if data, _ := os.ReadFile(secretsPath); string(data) != "version: 1\n" {
		t.Error("the secrets file changed")
	}
}

// A clash with another paisans deployment's interface names that
// deployment, from the host's registry when it lists it; a clash with
// anything else names what holds the subnet. Either way the advice is one
// line.
func TestCheckMeshLiveSaysWhoHoldsTheSubnet(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	advice := "Remove it if not in use or run paisans init to pick a free subnet."
	// The other deployment's site name is shown only where it differs from
	// the site being checked, which the line already names.
	other := &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/23","dev":"psns-0c1d"}]`, registry: registryHolding(t, otherID, "0c1d", "10.44.0.0/23")}
	var p *ui.Problem
	if err := checkMeshLive(cfg, "home-b", other); !errors.As(err, &p) || p.Hint != "mesh subnet 10.44.0.0/24 is in use by paisans deployment 0c1d (example.net, as site home-a)" {
		t.Errorf("a different site name: %#v", err)
	}
	for _, c := range []struct {
		name string
		host *initFake
		hint string
	}{
		{"paisans, registered", &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/23","dev":"psns-0c1d"}]`, registry: registryHolding(t, otherID, "0c1d", "10.44.0.0/23")},
			"mesh subnet 10.44.0.0/24 is in use by paisans deployment 0c1d (example.net)"},
		{"paisans, unregistered", &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/23","dev":"psns-0c1d"}]`},
			"mesh subnet 10.44.0.0/24 is in use by paisans deployment 0c1d"},
		{"something else", &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/23","dev":"tun0"}]`},
			"mesh subnet 10.44.0.0/24 is in use (route 10.44.0.0/23 dev tun0 on home-a)"},
	} {
		err := checkMeshLive(cfg, "home-a", c.host)
		var p *ui.Problem
		if !errors.As(err, &p) || p.Hint != c.hint || p.Explain != advice {
			t.Errorf("%s: %#v", c.name, err)
		}
	}
}

// A sign in client is not the operator's to provide: apply creates it at
// Pocket ID and records it. init names the clients apply will create on one
// line, never as a warning, and warns only for what the operator owes.
func TestInitNamesPendingClientsWithoutWarning(t *testing.T) {
	path, rec := initWorld(t, nil, fakeSites(), nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if rec.Has("warn", "oidc_clients.") {
		t.Errorf("a sign in client was warned about as a decision:\n%s", rec.Lines())
	}
	if !rec.Has("warn", "Secret for external.acme_dns_token needs to be set") {
		t.Errorf("an owed secret is no longer warned about:\n%s", rec.Lines())
	}
	if !rec.Has("result", "Next: run `paisans secrets set`.") {
		t.Errorf("init does not say how to set what is owed:\n%s", rec.Lines())
	}
	want := "7 sign in clients (account, blog, chat, gate, health, talk, wiki) are created by apply"
	if !rec.Has("pending", want) {
		t.Errorf("no pending line %q:\n%s", want, rec.Lines())
	}
	if !rec.Has("detail", "https://talk.example.org/") {
		t.Errorf("--verbose does not say what apply creates for each client:\n%s", rec.Lines())
	}
}

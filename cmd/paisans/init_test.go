package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// initFake is one site as init sees it: its files, its networks, or no
// answer at all.
type initFake struct {
	name   string
	routes string
	down   bool
	// broken is what ReadFile fails with on a host that answered.
	broken error
	// files are answered to ReadFile by path.
	files map[string]string
	// unadmitted is a host whose owner has not admitted the deployment: it
	// refuses the deployment's login.
	unadmitted bool
	// admission is what its admission check fails with on a host that
	// answers, Eg: a record that does not match.
	admission error
}

func (f *initFake) Run(command string) (string, error) {
	if f.down {
		return "ssh: connect to host " + f.name + " port 22: Operation timed out", errors.New("exit status 255")
	}
	if f.unadmitted {
		return "", fmt.Errorf("ssh: %w", apply.ErrLoginNotAdmitted)
	}
	switch command {
	case mesh.RouteCommand:
		if f.routes != "" {
			return f.routes, nil
		}
		return `[{"dst":"default","dev":"eth0"}]`, nil
	case mesh.AddrCommand, mesh.NetworksCommand:
		return "[]", nil
	case mesh.UserDaemonCommand:
		return "absent\n", nil
	}
	return "", errors.New("unexpected command " + command)
}

func (f *initFake) ReadFile(path string) (string, bool, error) {
	if f.down {
		return "", false, f.unreachable()
	}
	if f.unadmitted {
		return "", false, fmt.Errorf("ssh: %w", apply.ErrLoginNotAdmitted)
	}
	if f.broken != nil {
		return "", false, f.broken
	}
	if c, ok := f.files[path]; ok && c != "" {
		return c, true, nil
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
	savedHost, savedRandom, savedAdmission := initHost, meshRandom, admissionForSite
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
	// A fake answers its admission as it answers everything else: a record
	// for an admitted host, none for one that is not, and the fake's own
	// failure for one that does not answer.
	admissionForSite = func(h admission.Reader, _ *config.Config, _ string) (admission.Record, error) {
		f := h.(*initFake)
		switch {
		case f.down, f.unadmitted:
			_, _, err := f.ReadFile("/etc/paisans/admitted.json")
			return admission.Record{}, err
		case f.admission != nil:
			return admission.Record{}, f.admission
		}
		return admission.Record{}, nil
	}
	t.Cleanup(func() { initHost, meshRandom, admissionForSite = savedHost, savedRandom, savedAdmission })
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
	captureStdout(t, func() { err = runInit([]string{"--config", path}, strings.NewReader(""), io.Discard) })
	return err
}

// An admitted host's subnet is the one site admit built its firewall and
// Docker address pools around, so a subnet that overlaps something there is
// not rolled: init refuses, naming what it overlaps, with nothing written.
func TestInitRefusesToRollAnAdmittedSubnet(t *testing.T) {
	sites := fakeSites()
	sites["home-a"].routes = `[{"dst":"10.44.0.0/24","dev":"psns-0c1d"}]`
	path, _ := initWorld(t, nil, sites, []byte{212, 37})
	before, _ := os.ReadFile(path)
	err := runInitQuietly(t, path)
	hint, explain := ui.Describe(err)
	if err == nil || !strings.Contains(hint, "is not rolled") || !strings.Contains(explain, "evict") {
		t.Fatalf("init = %v, want a refusal to roll an admitted subnet", err)
	}
	if !strings.Contains(fmt.Sprintf("%+v", errors.Unwrap(err)), "route 10.44.0.0/24 dev psns-0c1d on home-a") {
		t.Errorf("the refusal does not name what the subnet overlaps: %v", errors.Unwrap(err))
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("init changed paisans-stack.yaml although it refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err == nil {
		t.Error("init generated secrets although it refused")
	}
}

// With no subnet declared, and no site admitted yet to have one, init rolls
// one and adds it, moving each address into it.
func TestInitAddsAMissingSubnet(t *testing.T) {
	sites := fakeSites()
	for _, f := range sites {
		f.unadmitted = true
	}
	path, out := initWorld(t, func(s string) string {
		i := strings.Index(s, "mesh:\n")
		j := strings.Index(s, "  subnet: 10.44.0.0/24\n")
		return s[:i] + s[j+len("  subnet: 10.44.0.0/24\n"):]
	}, sites, []byte{212, 37})
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

// Once any site holds this deployment's manifest, its subnet is deployed:
// init leaves it alone even where it now overlaps something, and an
// unreachable site no longer matters to it.
func TestInitLeavesADeployedSubnetAlone(t *testing.T) {
	sites := fakeSites()
	sites["home-a"].routes = `[{"dst":"10.44.0.0/16","dev":"eth1"}]`
	sites["home-b"].down = true
	path, out := initWorld(t, nil, sites, []byte{212, 37})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	sites["vm"].files = map[string]string{cfg.Deployment().Manifest(): `{"version":1,"files":[]}`}
	before, _ := os.ReadFile(path)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Errorf("init changed a deployed subnet:\n%s", after)
	}
	if !strings.Contains(out.Lines(), "is deployed (vm holds this deployment's manifest)") {
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

// apply and site prepare refuse a host where something outside this
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

// In the explicit test mode a plaintext secrets file that already exists
// stays plaintext, and says so.
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
// deployment by its token; a clash with anything else names what holds the
// subnet. Either way the advice is one line.
func TestCheckMeshLiveSaysWhoHoldsTheSubnet(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	advice := "Remove it if not in use or run paisans init to pick a free subnet."
	for _, c := range []struct {
		name string
		host *initFake
		hint string
	}{
		{"paisans", &initFake{name: "home-a.local", routes: `[{"dst":"10.44.0.0/23","dev":"psns-0c1d"}]`},
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

// Each site's admission is its own step: ✓ for an admitted host, ○ for one
// that has not admitted the deployment yet. init skips that one, with a
// warning saying how to have it admitted, and settles the subnet and writes
// the secrets from the others; site admit checks the subnet against it as
// root.
func TestInitSkipsAHostNotYetAdmitted(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	path, rec := initWorld(t, nil, sites, nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"health", "home-a", "vm"} {
		if !rec.Has("done", "check "+site+"'s admission") {
			t.Errorf("%s is not marked admitted:\n%s", site, rec.Lines())
		}
	}
	if !rec.Has("pending", "check home-b's admission") {
		t.Errorf("home-b is not marked as skipped:\n%s", rec.Lines())
	}
	if !rec.Has("note", "home-b has not admitted this deployment yet, so init skipped it") {
		t.Errorf("no warning for home-b:\n%s", rec.Lines())
	}
	if rec.Has("progress", "read home-b") || rec.Has("progress", "read the networks on home-b") {
		t.Errorf("init read home-b although it is not admitted:\n%s", rec.Lines())
	}
	if !rec.Has("detail", "overlaps nothing on health, home-a, vm; kept") {
		t.Errorf("the subnet was not checked against the admitted sites alone:\n%s", rec.Lines())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err != nil {
		t.Errorf("init wrote no secrets although only home-b is unadmitted: %v", err)
	}
}

// With no site admitted yet, as after paisans create, init still writes the
// secrets: it keeps the declared subnet for site admit to check on each.
func TestInitBeforeAnyAdmission(t *testing.T) {
	sites := fakeSites()
	for _, f := range sites {
		f.unadmitted = true
	}
	path, rec := initWorld(t, nil, sites, nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if rec.Has("done", "'s admission") {
		t.Errorf("a site is marked admitted:\n%s", rec.Lines())
	}
	if !rec.Has("detail", "no site has admitted this deployment yet") {
		t.Errorf("init did not say the subnet is left to site admit:\n%s", rec.Lines())
	}
	// A gateway not admitted holds no deployment record, and is not asked.
	if rec.Has("warn", "could not read the deployment record") {
		t.Errorf("init asked an unadmitted gateway for its record:\n%s", rec.Lines())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err != nil {
		t.Errorf("init wrote no secrets: %v", err)
	}
}

// A site whose admission does not cover it is still read, since the
// deployment's user logs in there, and warned about, since apply refuses it
// until its owner admits it again; an unreadable record says so.
func TestInitWarnsAboutAnAdmissionThatDoesNotMatch(t *testing.T) {
	sites := fakeSites()
	sites["vm"].admission = &admission.SiteError{Site: "vm", Problems: []string{"the host opened ssh port 22, and the site's is 2222"}}
	sites["health"].admission = &admission.Error{Problems: []string{"not JSON"}}
	path, rec := initWorld(t, nil, sites, nil)
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("warned", "check vm's admission") || !rec.Has("warn", "vm's admission does not match") {
		t.Errorf("vm's mismatch is not marked and warned:\n%s", rec.Lines())
	}
	if !rec.Has("warned", "check health's admission") || !rec.Has("warn", "health's admission record cannot be read") {
		t.Errorf("health's unreadable record is not marked and warned:\n%s", rec.Lines())
	}
	if !rec.Has("progress", "read the networks on vm") || !rec.Has("progress", "read the networks on health") {
		t.Errorf("a site admitted with problems was not read:\n%s", rec.Lines())
	}
}

// A login refused as a user the site names is the operator's key problem,
// not a host that has not admitted the deployment: init cannot ask it, and
// refuses with nothing written.
func TestInitRefusesWhenASiteRefusesTheOperatorsKey(t *testing.T) {
	sites := fakeSites()
	sites["vm"].admission = fmt.Errorf("ssh: %w", apply.ErrLoginRefused)
	path, rec := initWorld(t, nil, sites, nil)
	err := runInitQuietly(t, path)
	if hint, _ := ui.Describe(err); !strings.Contains(hint, "could not read every site") {
		t.Fatalf("init = %v, want a refusal naming vm", err)
	}
	if !rec.Has("fail", "check vm's admission") || rec.Has("pending", "check vm's admission") {
		t.Errorf("vm is not marked as failed:\n%s", rec.Lines())
	}
}

// One admitted site whose manifest cannot be read does not refuse init when
// another holds the manifest: the subnet is deployed, and is kept.
func TestInitKeepsADeployedSubnetPastAFailedRead(t *testing.T) {
	sites := fakeSites()
	sites["home-a"].broken = errors.New("permission denied")
	path, out := initWorld(t, nil, sites, nil)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	sites["vm"].files = map[string]string{cfg.Deployment().Manifest(): `{"version":1,"files":[]}`}
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Lines(), "is deployed (vm holds this deployment's manifest)") {
		t.Errorf("init did not keep the deployed subnet:\n%s", out.Lines())
	}
	sites["vm"].files = nil
	if err := runInitQuietly(t, path); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("with no manifest anywhere, init = %v, want the failed read", err)
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

// runInitAt runs init with stdin typed, at a terminal or not, and returns
// what it wrote to stdout, where its questions go.
func runInitAt(t *testing.T, path string, terminal bool, stdin string, extra ...string) (string, error) {
	t.Helper()
	withTerminal(t, terminal)
	out := new(strings.Builder)
	var err error
	captureStdout(t, func() { err = runInit(append([]string{"--config", path}, extra...), typed(t, stdin), out) })
	return out.String(), err
}

// The commands that admit a skipped site, from this machine or by request.
func hasAdmitCommands(rec *ui.Recorder, site string) bool {
	i := rec.Index("note", site+" has not admitted this deployment yet, so init skipped it")
	if i < 0 {
		return false
	}
	explain := rec.Events[i].Extra
	for _, want := range []string{
		"paisans site admit --site " + site + " --config ",
		"--ssh <your login> --execute",
		"paisans site request --site " + site + " --config ",
		"sudo paisans site admit --request " + site + ".admit --execute",
	} {
		if !strings.Contains(explain, want) {
			return false
		}
	}
	return true
}

// At a terminal init lists every site not admitted, with why, before it
// asks anything, then asks whether to admit each. A no prints the commands
// that admit it and skips it, as before any admission; nothing reaches the
// host as its owner.
func TestInitAnsweredNoListsTheCommandsAndSkips(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	sites["vm"].unadmitted = true
	path, rec := initWorld(t, nil, sites, nil)
	noAdmitFromInit(t)
	out, err := runInitAt(t, path, true, "n\nn\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"home-b", "vm"} {
		if !strings.Contains(out, "Admit "+site+" now from this machine? [y/N]") {
			t.Errorf("init did not ask about %s:\n%s", site, out)
		}
		if !hasAdmitCommands(rec, site) {
			t.Errorf("no commands for %s:\n%s", site, rec.Lines())
		}
	}
	list := rec.Index("note", "home-b and vm have not admitted this deployment yet")
	if list < 0 || !strings.Contains(rec.Events[list].Extra, "- home-b: ") || !strings.Contains(rec.Events[list].Extra, "- vm: ") {
		t.Fatalf("the sites not admitted are not listed with why:\n%s", rec.Lines())
	}
	if settle := rec.Index("step", "settle mesh subnet"); settle < list {
		t.Errorf("the list does not come before the subnet is settled:\n%s", rec.Lines())
	}
	if !rec.Has("detail", "overlaps nothing on health, home-a; kept") {
		t.Errorf("the subnet was not checked against the admitted sites alone:\n%s", rec.Lines())
	}
}

// A yes runs site admit --site's own flow: it asks for the grant the site's
// roles need and for the owner's login, shows the plan and asks before it
// runs it. init then reads the site's admission again and goes on with it
// admitted.
func TestInitAnsweredYesAdmitsTheSiteAndGoesOn(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	path, rec := initWorld(t, nil, sites, nil)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reached := fakeAdmitFromInit(t, cfg.Sites["home-b"].SSHUser(), func() { sites["home-b"].unadmitted = false })
	out, err := runInitAt(t, path, true, "y\ny\nowner\ny\n")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, rec.Lines())
	}
	for _, want := range []string{"Admit home-b now from this machine? [y/N]", "Grant --watchdog?", "Existing username on home-b.local (site home-b)", "Make these changes on"} {
		if !strings.Contains(out, want) {
			t.Errorf("init did not ask %q:\n%s", want, out)
		}
	}
	if reached.User != "owner" || reached.Host != "home-b.local" {
		t.Errorf("admit logged in as %+v", *reached)
	}
	if !rec.Has("done", "check home-b's admission") {
		t.Errorf("home-b is not marked admitted after admit:\n%s", rec.Lines())
	}
	if rec.Has("note", "home-b has not admitted this deployment yet, so init skipped it") {
		t.Errorf("init skipped home-b although it admitted it:\n%s", rec.Lines())
	}
	if !rec.Has("detail", "overlaps nothing on health, home-a, home-b, vm; kept") {
		t.Errorf("the subnet was not checked against home-b:\n%s", rec.Lines())
	}
}

// A plan the operator declines changes nothing, and is a no: init prints
// the commands and skips the site.
func TestInitDecliningAdmitsPlanSkipsTheSite(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	path, rec := initWorld(t, nil, sites, nil)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fakeAdmitFromInit(t, cfg.Sites["home-b"].SSHUser(), func() { t.Error("a declined plan ran") })
	if _, err := runInitAt(t, path, true, "y\ny\nowner\nn\n"); err != nil {
		t.Fatal(err)
	}
	if !hasAdmitCommands(rec, "home-b") {
		t.Errorf("no commands for home-b:\n%s", rec.Lines())
	}
}

// With --yes, or with no terminal, init asks nothing and never admits a
// host: it prints the commands and skips the site.
func TestInitWithYesOrNoTerminalNeverAsks(t *testing.T) {
	for _, c := range []struct {
		name     string
		terminal bool
		args     []string
	}{
		{"no terminal", false, nil},
		{"--yes", true, []string{"--yes"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(allowYesVariable, "1")
			sites := fakeSites()
			sites["home-b"].unadmitted = true
			path, rec := initWorld(t, nil, sites, nil)
			noAdmitFromInit(t)
			out, err := runInitAt(t, path, c.terminal, "y\ny\nowner\ny\n", c.args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "[y/N]") {
				t.Errorf("init asked:\n%s", out)
			}
			if !hasAdmitCommands(rec, "home-b") {
				t.Errorf("no commands for home-b:\n%s", rec.Lines())
			}
		})
	}
}

// plainInit makes init report as a plain run without --verbose does, into
// the returned buffer, which is what an operator reads.
func plainInit(t *testing.T) *strings.Builder {
	t.Helper()
	b := new(strings.Builder)
	reporterOverride = ui.NewPlain(b, false)
	return b
}

// Without --verbose, init still prints why each site is not admitted, and
// the commands that admit each site it skips, each with --execute, since
// without it site admit only shows its plan.
func TestInitPrintsWhyAndTheCommandsWithoutVerbose(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	sites["vm"].unadmitted = true
	path, _ := initWorld(t, nil, sites, nil)
	plain := plainInit(t)
	if _, err := runInitAt(t, path, false, ""); err != nil {
		t.Fatal(err)
	}
	out := plain.String()
	for _, want := range []string{
		"- home-b: ", "- vm: ",
		"paisans site admit --site home-b --config ",
		"--ssh <your login> --execute",
		"paisans site request --site home-b --config ",
		"sudo paisans site admit --request home-b.admit --execute",
		"paisans site admit --site vm --config ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a plain run does not print %q:\n%s", want, out)
		}
	}
}

// A site's admission step says it is not admitted, not that it was
// skipped: init has yet to offer to admit it.
func TestInitDoesNotCallASiteSkippedBeforeItAsks(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	path, rec := initWorld(t, nil, sites, nil)
	noAdmitFromInit(t)
	if _, err := runInitAt(t, path, true, "n\n"); err != nil {
		t.Fatal(err)
	}
	i := rec.Index("pending", "check home-b's admission")
	if i < 0 || strings.Contains(rec.Events[i].Extra, "skipped") {
		t.Errorf("home-b's admission step:\n%s", rec.Lines())
	}
}

// A grant the operator declines at admit's question changes nothing, as a
// declined plan does, so it is a no too: init prints the commands, naming
// site admit --site for the flags init cannot pass, and goes on.
func TestInitDecliningAGrantSkipsTheSite(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	path, rec := initWorld(t, nil, sites, nil)
	noAdmitFromInit(t)
	out, err := runInitAt(t, path, true, "y\nn\n")
	if err != nil {
		t.Fatalf("init stopped: %v", err)
	}
	if !strings.Contains(out, "Grant --watchdog?") {
		t.Errorf("admit did not ask for the grant:\n%s", out)
	}
	if !hasAdmitCommands(rec, "home-b") {
		t.Errorf("no commands for home-b:\n%s", rec.Lines())
	}
	i := rec.Index("note", "home-b has not admitted this deployment yet, so init skipped it")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "--watchdog") {
		t.Errorf("the skip does not say why admit stopped:\n%s", rec.Lines())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err != nil {
		t.Errorf("init did not go on to the secrets: %v", err)
	}
}

// Before mesh.subnet is set, init offers no admission at a terminal, and
// says why without --verbose.
func TestInitSaysWhyItOffersNoAdmissionWithoutASubnet(t *testing.T) {
	sites := fakeSites()
	for _, f := range sites {
		f.unadmitted = true
	}
	path, _ := initWorld(t, func(s string) string {
		i := strings.Index(s, "mesh:\n")
		j := strings.Index(s, "  subnet: 10.44.0.0/24\n")
		return s[:i] + s[j+len("  subnet: 10.44.0.0/24\n"):]
	}, sites, []byte{212, 37})
	noAdmitFromInit(t)
	plain := plainInit(t)
	out, err := runInitAt(t, path, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("init asked without a subnet:\n%s", out)
	}
	if !strings.Contains(plain.String(), "mesh.subnet is not set yet") {
		t.Errorf("a plain run does not say why nothing was offered:\n%s", plain.String())
	}
}

// Why admit stopped keeps the explanation's lines: a host no profile
// matches lists every supported release on a line of its own, none
// joined to the sentence before it.
func TestAdmitStopReasonKeepsTheExplanationsLines(t *testing.T) {
	err := &admitStoppedError{errors.New(hostprep.UnsupportedMessage(hostprep.OSRelease{ID: "ubuntu", VersionID: "20.04"}))}
	want := "site admit stopped with nothing changed: ubuntu 20.04 is not supported. Use one of the following:\n" + strings.Join(hostprep.Supported(), "\n")
	if got := admitStopReason(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// stopAdmitFromInit makes site admit, run from init, stop at site with
// err and nothing changed, as admit does when the host refuses the claim.
func stopAdmitFromInit(t *testing.T, site string, err error) {
	t.Helper()
	saved := admitFromInit
	t.Cleanup(func() { admitFromInit = saved })
	admitFromInit = func(_ ui.Reporter, _, s string, _ io.Reader, _ io.Writer) error {
		if s != site {
			t.Fatalf("site admit ran for %s", s)
		}
		return &admitStoppedError{err}
	}
}

// claimRefused is the refusal a host gives a claim that clashes with
// another deployment there on what.
func claimRefused(roles string, what ...string) error {
	theirs := registry.Entry{Token: "0c1d", Interface: "psns-0c1d", Subnet: "10.44.0.0/24", Roles: roles}
	ours := registry.Entry{Token: "f2a9", Interface: "psns-f2a9", Subnet: "10.44.0.0/24", Roles: "apps,data"}
	return registry.Refused(&initFake{name: "home-b.local"}, ours, registry.Conflict{ID: otherID, Entry: theirs, Clashes: what})
}

// A host that refuses admit because another deployment there holds an
// overlapping mesh subnet is a network init could not read, since the
// deployment's user cannot log in there: with no site admitted yet, init
// rolls a subnet clear of it, so admitting the site again succeeds.
func TestInitRollsClearOfASubnetAdmitRefused(t *testing.T) {
	sites := fakeSites()
	for _, f := range sites {
		f.unadmitted = true
	}
	path, rec := initWorld(t, nil, sites, []byte{212, 37})
	stopAdmitFromInit(t, "home-b", claimRefused("apps", "mesh subnet 10.44.0.0/24"))
	if _, err := runInitAt(t, path, true, "n\nn\ny\nn\n"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mesh.Subnet != "10.212.37.0/24" {
		t.Errorf("subnet %s, want it rolled clear of home-b:\n%s", cfg.Mesh.Subnet, rec.Lines())
	}
	if !strings.Contains(rec.Lines(), "10.44.0.0/24 overlapped deployment "+otherID+"'s mesh subnet 10.44.0.0/24 on home-b") {
		t.Errorf("init did not say why it rolled:\n%s", rec.Lines())
	}
}

// Once a site has admitted the deployment, init does not roll clear of a
// subnet admit refused elsewhere: it refuses, naming the host that holds
// it, and writes nothing.
func TestInitRefusesToRollPastAnAdmittedSiteForAnotherHost(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].unadmitted = true
	path, rec := initWorld(t, nil, sites, []byte{212, 37})
	before, _ := os.ReadFile(path)
	stopAdmitFromInit(t, "home-b", claimRefused("apps", "mesh subnet 10.44.0.0/24"))
	_, err := runInitAt(t, path, true, "y\n")
	hint, explain := ui.Describe(err)
	if err == nil || !strings.Contains(hint, "overlaps deployment "+otherID+"'s mesh subnet 10.44.0.0/24 on home-b, and is not rolled") || !strings.Contains(explain, "evict") {
		t.Fatalf("init = %v, want a refusal to roll naming home-b\n%s", err, rec.Lines())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("init changed paisans-stack.yaml although it refused")
	}
}

// A host whose other deployment also holds a role this site needs cannot
// take the site on any subnet, so init keeps the subnet and skips the site
// with admit's advice to use another host.
func TestInitKeepsTheSubnetWhenAdmitRefusedARole(t *testing.T) {
	sites := fakeSites()
	for _, f := range sites {
		f.unadmitted = true
	}
	path, rec := initWorld(t, nil, sites, []byte{212, 37})
	before, _ := os.ReadFile(path)
	stopAdmitFromInit(t, "home-b", claimRefused("apps,data", "mesh subnet 10.44.0.0/24", "the data role"))
	if _, err := runInitAt(t, path, true, "n\nn\ny\nn\n"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("init rolled the subnet for a host it cannot use:\n%s", rec.Lines())
	}
	i := rec.Index("note", "home-b has not admitted this deployment yet, so init skipped it")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "Use another host for one of the data sites") {
		t.Errorf("the skip does not give admit's advice:\n%s", rec.Lines())
	}
}

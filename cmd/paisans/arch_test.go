package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/hostarch"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

type archHost string

func (a archHost) Run(command string) (string, error) {
	if command != hostarch.Command {
		return "", errors.New("unexpected " + command)
	}
	return string(a) + "\n", nil
}

const twoDataSites = `version: 1
id: f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01
community:
  name: "Fixture"
  domain: example.org
mesh:
  subnet: 10.44.0.0/24
operators:
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org
sites:
  home-a:
    roles: [data, apps]
    address: 10.44.0.1
    ssh: {host: home-a.local, user: ubuntu, keys: [alice]}
  home-b:
    roles: [data, apps]
    address: 10.44.0.2
    ssh: {host: home-b.local, user: ubuntu, keys: [alice]}
etcd:
  members: [home-a, home-b]
`

func archFixture(t *testing.T, body string) (string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func hostsAt(archs map[string]string) func(string) hostarch.Runner {
	return func(name string) hostarch.Runner { return archHost(archs[name]) }
}

func TestAnExecutedRunRecordsEveryMissingArchAndRereads(t *testing.T) {
	path, cfg := archFixture(t, twoDataSites)
	r := &ui.Recorder{}
	read, err := readArchitectures(r, cfg, cfg.SiteNames(), hostsAt(map[string]string{"home-a": "arm64", "home-b": "arm64"}))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = recordArchitectures(r, path, cfg, read, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["home-a"].Arch != "arm64" || cfg.Sites["home-b"].Arch != "arm64" {
		t.Fatalf("not reread: %+v", cfg.Sites)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "arch: arm64") != 2 {
		t.Fatalf("file:\n%s", data)
	}
	if r.Index("note", "home-a: arch arm64 recorded in paisans-stack.yaml") < 0 {
		t.Errorf("no note: %+v", r.Events)
	}
}

func TestADryRunRecordsInMemoryAndWritesNothing(t *testing.T) {
	path, cfg := archFixture(t, twoDataSites)
	before, _ := os.ReadFile(path)
	original := cfg
	cfg, err := recordArchitectures(&ui.Recorder{}, path, cfg, map[string]string{"home-a": "amd64", "home-b": "amd64"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if original.Sites["home-a"].Arch != "" {
		t.Fatal("a dry run changed the configuration it was given")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a dry run wrote the file")
	}
	if cfg.Sites["home-a"].Arch != "amd64" {
		t.Fatal("the plan does not see the probed architecture")
	}
}

func TestARecordedArchTheHostContradictsIsRefused(t *testing.T) {
	_, cfg := archFixture(t, strings.Replace(twoDataSites, "    address: 10.44.0.2\n", "    address: 10.44.0.2\n    arch: amd64\n", 1))
	_, err := readArchitectures(&ui.Recorder{}, cfg, []string{"home-b"}, hostsAt(map[string]string{"home-b": "arm64"}))
	if err == nil || !strings.Contains(err.Error(), "delete the arch line from home-b") {
		t.Fatalf("got %v", err)
	}
}

func TestDataSitesFoundToDifferAreRefusedAfterRecording(t *testing.T) {
	path, cfg := archFixture(t, twoDataSites)
	_, err := recordArchitectures(&ui.Recorder{}, path, cfg, map[string]string{"home-a": "amd64", "home-b": "arm64"}, false)
	if err == nil || !strings.Contains(err.Error(), "home-a is amd64 and home-b is arm64") {
		t.Fatalf("got %v", err)
	}
}

func TestEveryKeyRecordedWritesNothing(t *testing.T) {
	body := strings.Replace(strings.Replace(twoDataSites, "    address: 10.44.0.1\n", "    address: 10.44.0.1\n    arch: amd64\n", 1), "    address: 10.44.0.2\n", "    address: 10.44.0.2\n    arch: amd64\n", 1)
	path, cfg := archFixture(t, body)
	info, _ := os.Stat(path)
	if _, err := recordArchitectures(&ui.Recorder{}, path, cfg, map[string]string{"home-a": "amd64", "home-b": "amd64"}, true); err != nil {
		t.Fatal(err)
	}
	again, _ := os.Stat(path)
	if !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("rewrote a file with nothing to add")
	}
}

// archTransport is a host that answers hostarch.Command with arch and hands
// every other command to Transport.
type archTransport struct {
	apply.Transport
	arch string
}

func (h archTransport) Run(command string) (string, error) {
	if command == hostarch.Command {
		return h.arch + "\n", nil
	}
	return h.Transport.Run(command)
}

// SettlesSudo and PrepareSudo are the wrapped host's, so a run settles sudo
// on it as it would unwrapped.
func (h archTransport) SettlesSudo() bool  { return apply.SettlesSudo(h.Transport) }
func (h archTransport) PrepareSudo() error { return apply.PrepareSudo(h.Transport) }

// A whole apply reads every site's architecture before it plans, and its dry
// run says what --execute would record without writing it.
func TestADryRunApplyReadsEverySitesArchitecture(t *testing.T) {
	// The setup of the runConverge dry runs in converge_test.go: every
	// step, etcd read and review faked, and convergeHost replaced by a
	// host answering hostarch.Command with "arm64\n". The configuration is
	// a copy, so a write would land where the test can see it.
	fakeConverge(t, nil)
	recordConverge(t)
	saved := convergeHost
	convergeHost = func(string, config.Site, bool) apply.Transport {
		return archTransport{runningHost{failingHost{match: "\x00"}}, "arm64"}
	}
	t.Cleanup(func() { convergeHost = saved })
	path := fixtureWithoutArch(t)
	secrets := fixtureSecretsPath()
	r := &ui.Recorder{}
	_ = runConverge(r, convergeOptions{Config: path, Secrets: secrets, Execute: false, Sudo: false})
	if r.Index("done", "read each site's architecture") < 0 {
		t.Fatalf("no architecture read: %+v", r.Events)
	}
	if a, e := r.Index("done", "read each site's architecture"), r.Index("section", "etcd members"); e >= 0 && e < a {
		t.Errorf("read after the etcd records:\n%s", r.Lines())
	}
	if r.Index("note", "home-a: arch arm64, recorded in paisans-stack.yaml by --execute") < 0 {
		t.Errorf("no note of what --execute records:\n%s", r.Lines())
	}
	before, _ := os.ReadFile(path)
	if strings.Contains(string(before), "arch:") {
		t.Fatal("a dry run wrote arch")
	}
}

// fixtureWithoutArch is a copy of the fixture configuration without its arch
// lines, as a file written before the key existed lacks them.
func fixtureWithoutArch(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), "    arch: amd64\n", ""))
	if strings.Contains(string(body), "arch:") {
		t.Fatal("the fixture's arch lines changed shape; the copy still records one")
	}
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// gatheredStore is applyStore's version as a test sees it: the paisans-stack.yaml
// bytes it was gathered from, and published as they were.
type gatheredStore struct {
	paisans, secrets []byte
}

func (s *gatheredStore) check(ui.Reporter, map[string]grant.Record, bool, io.Reader, io.Writer) error {
	return nil
}

func (s *gatheredStore) publish(_ ui.Reporter, execute bool) (*appliedVersion, error) {
	if !execute {
		return nil, nil
	}
	return &appliedVersion{sha: strings.Repeat("d", 40), revision: 1, files: snapshotted(configstore.Files{Paisans: s.paisans, Secrets: s.secrets})}, nil
}

func (s *gatheredStore) held() (string, int) { return "", 0 }

// gatherFiles replaces applyStore with one that takes the bytes the real one
// would: those the caller loaded, or, given none, the files on disk at the
// gather, as apply without --site gathers them.
func gatherFiles(t *testing.T) **gatheredStore {
	t.Helper()
	var got *gatheredStore
	saved := applyStore
	applyStore = func(_ ui.Reporter, configPath, secretsPath string, loaded *configstore.Files, _ *config.Config, _ bool, _ io.Reader, _ io.Writer) (storeVersion, error) {
		s := &gatheredStore{}
		if loaded != nil {
			s.paisans, s.secrets = loaded.Paisans, loaded.Secrets
		} else {
			var err error
			if s.paisans, err = os.ReadFile(configPath); err != nil {
				return nil, err
			}
			if s.secrets, err = os.ReadFile(secretsPath); err != nil {
				return nil, err
			}
		}
		got = s
		return s, nil
	}
	t.Cleanup(func() { applyStore = saved })
	return &got
}

// A whole apply records every site's architecture before it gathers the
// version it publishes, so the published bytes carry the keys and no
// apply step finds its file changed after the publish.
func TestAnExecutedApplyRecordsEveryArchBeforeItPublishes(t *testing.T) {
	path := fixtureWithoutArch(t)
	fakeConverge(t, nil)
	rec := recordConverge(t)
	gathered := gatherFiles(t)
	// Each apply step runs for real up to the host, which answers only
	// the architecture read, so the step's check of the published bytes
	// runs and the step then stops at the host.
	failingSSH(t)
	fake := convergeRun
	var stepErrs []error
	convergeRun = func(args []string) error {
		if args[0] == "apply" {
			if err := runStep(args); err != nil {
				stepErrs = append(stepErrs, err)
			}
			return nil
		}
		return fake(args)
	}
	var err error
	captureStdout(t, func() {
		err = runConverge(rec, convergeOptions{Config: path, Secrets: fixtureSecretsPath(), Execute: true, Sudo: false, stdin: noTerminalStdin(), stdout: io.Discard})
	})
	if err != nil {
		t.Fatalf("%v %s\n%s", err, problemText(err), rec.Lines())
	}
	body, _ := os.ReadFile(path)
	if n := strings.Count(string(body), "arch: amd64"); n != 4 {
		t.Fatalf("%d sites record arch:\n%s", n, body)
	}
	if *gathered == nil || strings.Count(string((*gathered).paisans), "arch: amd64") != 4 {
		t.Fatalf("the version published lacks the recorded keys")
	}
	if len(stepErrs) == 0 {
		t.Fatal("no apply step ran")
	}
	for _, e := range stepErrs {
		if strings.Contains(e.Error()+problemText(e), "changed after the run published it") {
			t.Errorf("a step found the file changed after the publish: %v", problemText(e))
		}
	}
}

// apply --site on its own records its site's architecture, and the version
// it publishes is the file as written, not as first loaded.
func TestAnExecutedApplySitePublishesTheRecordedArch(t *testing.T) {
	path := fixtureWithoutArch(t)
	gathered := gatherFiles(t)
	failingSSH(t)
	withRecorder(t, false)
	captureStdout(t, func() {
		_ = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--sudo=false", "--site", "vm", "--execute"}, noTerminalStdin(), io.Discard)
	})
	body, _ := os.ReadFile(path)
	if strings.Count(string(body), "arch: amd64") != 1 {
		t.Fatalf("vm's arch not recorded alone:\n%s", body)
	}
	if *gathered == nil || string((*gathered).paisans) != string(body) {
		t.Fatal("the version gathered is not the file as recorded")
	}
}

// platformsAt is a registry naming one build for every tag, published for
// the platforms given by image name (before @), and for amd64 and arm64
// otherwise.
type platformsAt map[string][]string

func (p platformsAt) Digest(string) (string, error) { return testCaddyDigest, nil }

func (p platformsAt) Platforms(ref string) ([]string, error) {
	name, _, _ := strings.Cut(ref, "@")
	if got, ok := p[name]; ok {
		return got, nil
	}
	return []string{"amd64", "arm64"}, nil
}

const amd64OnlyMbin = "ghcr.io/example-org/mbin:v1.10.1-fork"

// armWithAmd64OnlyMbin is the setup of the runConverge tests on every site
// being arm64, with home-a's render naming an Mbin published for amd64 alone.
func armWithAmd64OnlyMbin(t *testing.T) {
	t.Helper()
	saved, savedRender, savedDigests := convergeHost, convergeRender, imageDigests
	convergeHost = func(string, config.Site, bool) apply.Transport {
		return archTransport{runningHost{failingHost{match: "\x00"}}, "arm64"}
	}
	convergeRender = func(*config.Config, *config.Secrets) (*render.Plan, error) {
		return &render.Plan{Images: map[string][]render.ComposeImage{
			"home-a": {{Stack: "talk", App: "talk", Image: amd64OnlyMbin, Services: []string{"app"}}},
		}}, nil
	}
	imageDigests = platformsAt{amd64OnlyMbin: {"amd64"}}
	t.Cleanup(func() { convergeHost, convergeRender, imageDigests = saved, savedRender, savedDigests })
}

// A whole apply checks every site's images against its architecture from
// the render, before any host is prepared, so a dry run of a deployment no
// host of which is prepared yet still names an image a site cannot run.
func TestADryRunApplyRefusesAnImageWithoutTheSitesPlatform(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, map[string]any{
		"host prepare --site home-a": "install docker", "host prepare --site home-b": "install docker",
		"host prepare --site vm": "install docker", "host prepare --site watch": "install docker",
	})
	recordConverge(t)
	armWithAmd64OnlyMbin(t)
	r := &ui.Recorder{}
	err := runConverge(r, convergeOptions{Config: fixtureWithoutArch(t), Secrets: fixtureSecretsPath(), Execute: false, Sudo: false})
	if err == nil {
		t.Fatalf("not refused:\n%s", r.Lines())
	}
	if !r.Has("refuse", "") || r.Events[r.Index("refuse", "")].Extra != "home-a (arm64): `"+amd64OnlyMbin+"` is published for linux/amd64 only." {
		t.Fatalf("no refusal:\n%s", r.Lines())
	}
}

func TestAnExecutedApplyRefusesAnImageBeforeAnyHostPrepare(t *testing.T) {
	ran := fakeConverge(t, nil)
	rec := recordConverge(t)
	armWithAmd64OnlyMbin(t)
	var err error
	captureStdout(t, func() {
		err = runConverge(rec, convergeOptions{Config: fixtureWithoutArch(t), Secrets: fixtureSecretsPath(), Execute: true, Sudo: false, stdin: noTerminalStdin(), stdout: io.Discard})
	})
	if err == nil {
		t.Fatalf("not refused:\n%s", rec.Lines())
	}
	for _, step := range *ran {
		if strings.HasPrefix(step, "host prepare") {
			t.Fatalf("prepared a host before refusing: %v", *ran)
		}
	}
	if !rec.Has("refuse", "") {
		t.Fatalf("no refusal:\n%s", rec.Lines())
	}
}

// publishedFor is a registry naming one build for every tag, each published
// for one architecture alone.
type publishedFor string

func (p publishedFor) Digest(string) (string, error)      { return testCaddyDigest, nil }
func (p publishedFor) Platforms(string) ([]string, error) { return []string{string(p)}, nil }

// site add changes the mesh, HAProxy and etcd on every site before its
// stage 4 pulls the new site's images, so it checks them against the new
// site's architecture before it claims any host, its dry run included.
func TestSiteAddRefusesAnImageWithoutTheNewSitesPlatform(t *testing.T) {
	path := joinConfig(t)
	saved := imageDigests
	imageDigests = publishedFor("arm64")
	t.Cleanup(func() { imageDigests = saved })
	for _, execute := range []bool{false, true} {
		log := loggingSSH(t, "")
		fakeGrants(t, func(string) *grant.Record { return nil })
		rec := recordConverge(t)
		args := []string{"home-b", "--config", path, "--secrets", mustAbs(t, fixtureSecretsPath()), "--sudo=false"}
		if execute {
			args = append(args, "--execute")
		}
		err := runSiteAdd(args, noTerminalStdin(), io.Discard)
		if err == nil || !strings.Contains(problemText(err), "refusal") {
			t.Fatalf("execute %v: got %v\n%s", execute, err, rec.Lines())
		}
		if i := rec.Index("refuse", ""); i < 0 || !strings.HasPrefix(rec.Events[i].Extra, "home-b (amd64): ") || !strings.HasSuffix(rec.Events[i].Extra, "is published for linux/arm64 only.") {
			t.Fatalf("execute %v: no refusal:\n%s", execute, rec.Lines())
		}
		if rec.Has("refuse", "") && strings.Contains(rec.Lines(), "home-a (") {
			t.Errorf("checked a site other than the new one:\n%s", rec.Lines())
		}
		if claimed(logged(t, log)) {
			t.Errorf("execute %v: claimed a host before refusing", execute)
		}
	}
}

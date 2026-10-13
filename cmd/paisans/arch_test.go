package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostarch"
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
admins:
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
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	// The fixture records every site's arch, so this copy drops them, as
	// a file written before the key existed lacks them.
	body = []byte(strings.ReplaceAll(string(body), "    arch: amd64\n", ""))
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
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

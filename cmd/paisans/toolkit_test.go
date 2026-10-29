package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/appremove"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/dns/dnstest"
	"github.com/paisans-software/paisans-cli/internal/gatewaymove"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
	"github.com/paisans-software/pocketid-go"
)

// appliedByNewer makes each site in newer read as last applied by that
// release of paisans, and every other site as applied by this one, and
// records each site read.
func appliedByNewer(t *testing.T, newer map[string]string) *[]string {
	t.Helper()
	var read []string
	saved := toolkitCheck
	toolkitCheck = func(site string, _ apply.Transport, _ deployment.Deployment) error {
		read = append(read, site)
		if v, ok := newer[site]; ok {
			return &apply.NewerToolkitError{Site: site, Build: version.Build{Version: v, Commit: strings.Repeat("a", 40)}}
		}
		return nil
	}
	t.Cleanup(func() { toolkitCheck = saved })
	return &read
}

// An apply finding sites a newer paisans last applied is refused before its
// lease, its review or any step, naming every such site and the newest
// release, which is the one to upgrade to. A dry run is refused the same
// way, since its plan would be this release's reading of those hosts.
func TestApplyIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	cfg := fixtureCfg(t)
	sites := cfg.SiteNames()
	if len(sites) < 2 {
		t.Fatalf("the fixture declares %v, and the test needs two sites", sites)
	}
	for _, extra := range [][]string{nil, {"--execute"}} {
		ran := fakeConverge(t, nil)
		takes := heldBy(t, "bob@desk")
		read := appliedByNewer(t, map[string]string{sites[0]: "99.0.0", sites[1]: "99.1.0"})
		err := converge(t, extra...)
		text := problemText(err)
		if err == nil || !strings.Contains(text, "a newer paisans last applied this deployment") {
			t.Fatalf("%v: err = %v", extra, err)
		}
		for _, want := range []string{sites[0] + ": paisans 99.0.0", sites[1] + ": paisans 99.1.0", "Upgrade paisans to 99.1.0 or later", "Nothing was changed"} {
			if !strings.Contains(text, want) {
				t.Errorf("%v: the refusal does not say %q:\n%s", extra, want, text)
			}
		}
		if len(*read) < 2 {
			t.Errorf("%v: read only %v, so the refusal cannot name every site", extra, *read)
		}
		if len(*ran) != 0 {
			t.Errorf("%v: a refused apply ran %v", extra, *ran)
		}
		if len(*takes) != 0 {
			t.Errorf("%v: a refused apply took the lease", extra)
		}
	}
}

// Sites this release or an older one last applied pass, and the run goes on.
func TestApplyGoesOnWhenNoNewerPaisansAppliedASite(t *testing.T) {
	ran := fakeConverge(t, nil)
	read := appliedByNewer(t, nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	if len(*read) == 0 {
		t.Error("the run read no site's manifest")
	}
	if len(*ran) == 0 {
		t.Error("the run ran no step")
	}
}

// publish is refused as apply is, before its lease and its gather, and
// pushes nothing.
func TestPublishIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	f.editDraft(t, "# next\n")
	takes := heldBy(t, "bob@desk")
	appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	var out bytes.Buffer
	var err error
	captureStdout(t, func() {
		err = runDeployment([]string{"publish", "--config", f.cfgPath, "--execute"}, noTerminalStdin(), &out)
	})
	text := problemText(err)
	if err == nil || !strings.Contains(text, "home-a: paisans 99.0.0") || !strings.Contains(text, "run "+progname.Cmd()+" deployment publish again") {
		t.Fatalf("err = %v", text)
	}
	if len(*takes) != 0 {
		t.Error("a refused publish took the lease")
	}
	if h, _ := f.sys.Head(); h != f.shas[0] || f.pushed() {
		t.Fatalf("a refused publish moved the head to %s, pushed %v", h, f.pushed())
	}
}

// A site that does not answer is passed over with a warning, since publish
// pushes it nothing; one that answers is still checked.
func TestRefuseNewerToolkitPassesOverASiteThatDoesNotAnswer(t *testing.T) {
	cfg := fixtureCfg(t)
	saved := toolkitCheck
	toolkitCheck = func(site string, _ apply.Transport, _ deployment.Deployment) error {
		if site == "home-b" {
			return &apply.NewerToolkitError{Site: site, Build: version.Build{Version: "99.0.0", Commit: strings.Repeat("a", 40)}}
		}
		return apply.CheckToolkit(site, unansweredHost{}, cfg.Deployment())
	}
	t.Cleanup(func() { toolkitCheck = saved })
	host := func(string) apply.Transport { return nil }
	r := &ui.Recorder{}
	if err := refuseNewerToolkit(r, cfg, []string{"home-a"}, host, "deployment publish", true); err != nil {
		t.Fatalf("an unanswering site refused the publish: %v", err)
	}
	if !strings.Contains(r.Lines(), "home-a was not checked for a newer paisans") {
		t.Errorf("no warning for the site passed over:\n%s", r.Lines())
	}
	if err := refuseNewerToolkit(&ui.Recorder{}, cfg, []string{"home-a"}, host, "deployment apply", false); err == nil {
		t.Error("apply went on past a site it could not read")
	}
	if err := refuseNewerToolkit(&ui.Recorder{}, cfg, []string{"home-a", "home-b"}, host, "deployment publish", true); err == nil {
		t.Error("passing over one site let a newer one through")
	}
}

// unansweredHost is a site whose every read fails, as one that is down.
type unansweredHost struct{ emptyHost }

func (unansweredHost) ReadFile(string) (string, bool, error) {
	return "", false, errors.New("ssh: connect to host home-a port 22: Connection refused")
}

// refusedAsNewer fails the test unless err is the refusal of command, as
// typed, over site, which 99.0.0 last applied.
func refusedAsNewer(t *testing.T, err error, site, command string) {
	t.Helper()
	text := problemText(err)
	if err == nil || !strings.Contains(text, site+": paisans 99.0.0") || !strings.Contains(text, "run "+progname.Cmd()+" "+command+" again") {
		t.Fatalf("err = %v\n%s", err, text)
	}
}

// readAll fails the test unless every site in want was read.
func readAll(t *testing.T, read []string, want ...string) {
	t.Helper()
	for _, site := range want {
		if !slices.Contains(read, site) {
			t.Errorf("read %v, and not %s", read, site)
		}
	}
}

// atToolkitCheck calls at once, as the first site's manifest is read, so a
// test can compare what reached a host by then with what reached one at the
// end.
func atToolkitCheck(t *testing.T, at func()) {
	t.Helper()
	saved := toolkitCheck
	first := true
	toolkitCheck = func(site string, h apply.Transport, d deployment.Deployment) error {
		if first {
			first = false
			at()
		}
		return saved(site, h, d)
	}
	t.Cleanup(func() { toolkitCheck = saved })
}

// sentAfterCheck returns what loggingSSH has logged to log since the first
// manifest was read.
func sentAfterCheck(t *testing.T, log string) func() string {
	t.Helper()
	var at string
	atToolkitCheck(t, func() { at = logged(t, log) })
	return func() string { return strings.TrimPrefix(logged(t, log), at) }
}

// site prepare is refused before anything but the check reaches its host.
func TestSitePrepareIsRefusedWhenANewerPaisansLastAppliedIt(t *testing.T) {
	log := loggingSSH(t, "")
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	rec, err := prepare(t, noTerminalStdin(), io.Discard, "--execute")
	refusedAsNewer(t, err, "home-a", "site prepare --site home-a")
	readAll(t, *read, "home-a")
	if sent := logged(t, log); sent != "" {
		t.Errorf("a refused prepare reached its host:\n%s\n%s", sent, rec.Lines())
	}
}

// site add is refused when a joined site was last applied by a newer
// paisans, before its review, any admission check or any write.
func TestSiteAddIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	path := joinConfig(t)
	before, _ := os.ReadFile(path)
	log := loggingSSH(t, "")
	recordConverge(t)
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	after := sentAfterCheck(t, log)
	err := runSiteAdd([]string{"home-b", "--config", path, "--secrets", mustAbs(t, fixtureSecretsPath()), "--execute"}, noTerminalStdin(), io.Discard)
	refusedAsNewer(t, err, "home-a", "site add home-b")
	readAll(t, *read, "home-a", "home-b")
	if sent := after(); sent != "" || checked(logged(t, log)) {
		t.Errorf("a refused join reached a host after the check:\n%s", sent)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(before, now) {
		t.Error("a refused join wrote the configuration")
	}
}

// storage init is refused before it reads Garage.
func TestStorageInitIsRefusedWhenANewerPaisansLastAppliedIt(t *testing.T) {
	log := loggingSSH(t, "")
	recordConverge(t)
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	after := sentAfterCheck(t, log)
	err := runStorageInit([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--site", "home-a", "--execute"})
	refusedAsNewer(t, err, "home-a", "storage init --site home-a")
	readAll(t, *read, "home-a")
	if sent := after(); sent != "" {
		t.Errorf("a refused storage init reached its host after the check:\n%s", sent)
	}
}

// storage add is refused before it reads any Garage site.
func TestStorageAddIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	log := loggingSSH(t, "")
	recordConverge(t)
	read := appliedByNewer(t, map[string]string{"home-b": "99.0.0"})
	after := sentAfterCheck(t, log)
	err := runStorageAdd([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--execute"})
	refusedAsNewer(t, err, "home-b", "storage add")
	readAll(t, *read, "home-a", "home-b")
	if sent := after(); sent != "" {
		t.Errorf("a refused storage add reached a host after the check:\n%s", sent)
	}
}

// prune is refused before it reads the site's volumes.
func TestPruneIsRefusedWhenANewerPaisansLastAppliedIt(t *testing.T) {
	log := loggingSSH(t, "")
	recordConverge(t)
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	after := sentAfterCheck(t, log)
	err := runPrune([]string{"--config", fixtureConfig(), "--site", "home-a", "--execute"})
	refusedAsNewer(t, err, "home-a", "prune --site home-a")
	readAll(t, *read, "home-a")
	if sent := after(); sent != "" {
		t.Errorf("a refused prune reached its host after the check:\n%s", sent)
	}
}

// oidc client create --execute is refused before Pocket ID is asked
// anything, and records no client.
func TestOIDCClientCreateIsRefusedWhenANewerPaisansLastAppliedItsSite(t *testing.T) {
	fake := withIDPFake(t)
	path := tempSecrets(t)
	before, _ := os.ReadFile(path)
	withRecorder(t, false)
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	err := runOIDCClientCreate([]string{"--config", fixtureDraft(t, fixtureDeploymentID), "--secrets", path, "--app", "talk", "--execute"})
	refusedAsNewer(t, err, "home-a", "oidc client create")
	readAll(t, *read, "home-a")
	if len(fake.commands) != 0 || fake.mutated {
		t.Errorf("a refused create reached Pocket ID: %q", fake.commands)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a refused create wrote the secrets file")
	}
}

// secrets rotate is refused after every site answers and before the
// cluster's health is read or any next value is written.
func TestSecretsRotateIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	before, _ := os.ReadFile(secretsPath)
	read := appliedByNewer(t, map[string]string{"watch": "99.0.0"})
	var at int
	atToolkitCheck(t, func() { at = len(w.commands) })
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "storage.garage.admin_token", "--execute"), terminalStdin(t, "", false), &bytes.Buffer{})
	refusedAsNewer(t, err, "watch", "secrets rotate")
	readAll(t, *read, "home-a", "home-b", "watch")
	if len(w.commands) != at || w.checked() {
		t.Errorf("a refused rotation reached a host after the check:\n%s\n%s", strings.Join(w.commands[at:], "\n"), rec.Lines())
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("a refused rotation wrote the secrets file")
	}
}

// operator register reads every site before it gathers their versions, and
// is refused with nothing pushed and no key recorded.
func TestOperatorRegisterIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	shas, answer, files, key, pub := storedHistoryKey(t, 1, 0)
	var runs []string
	l := registerWorkstation(t, answer, files[0], &runs, pub)
	savedSign := configSignWith
	configSignWith = func(string) string { return key }
	t.Cleanup(func() { configSignWith = savedSign })
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	err := runOperator([]string{"register", "--root", shas[0], "--config", l.config(), "--execute"}, noTerminalStdin(), &bytes.Buffer{})
	refusedAsNewer(t, err, "home-a", "operator register")
	readAll(t, *read, "home-a", "home-b")
	if len(runs) != 0 {
		t.Errorf("a refused register reached a site: %q", runs)
	}
	if k, _ := l.sys.SigningKey(); k != "" {
		t.Errorf("a refused register recorded key %q", k)
	}
}

// storage rotate-key is refused after its host checks and before it reads
// the app's key or writes the secrets.
func TestStorageRotateKeyIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	log := loggingSSH(t, "")
	configPath, secretsPath := rotateDeployment(t, nil)
	before, _ := os.ReadFile(secretsPath)
	recordConverge(t)
	read := appliedByNewer(t, map[string]string{"home-b": "99.0.0"})
	after := sentAfterCheck(t, log)
	err := runStorageRotateKey([]string{"--app", "talk", "--config", configPath, "--secrets", secretsPath, "--execute"})
	refusedAsNewer(t, err, "home-b", "storage rotate-key --app talk")
	readAll(t, *read, "home-a", "home-b")
	if sent := after(); sent != "" {
		t.Errorf("a refused rotation reached a host after the check:\n%s", sent)
	}
	if now, _ := os.ReadFile(secretsPath); !bytes.Equal(before, now) {
		t.Error("a refused rotation wrote the secrets file")
	}
}

// oldClient is Pocket ID holding a client for the app being removed, and
// keeping what it was asked to delete.
type oldClient struct{ deleted *[]string }

func (oldClient) OIDCClientByID(context.Context, string) (*pocketid.OIDCClient, error) {
	return nil, nil
}

func (oldClient) FindOIDCClient(_ context.Context, name string) (*pocketid.OIDCClient, error) {
	return &pocketid.OIDCClient{ID: "c-old", Name: name}, nil
}

func (c oldClient) DeleteOIDCClient(_ context.Context, id string) error {
	*c.deleted = append(*c.deleted, id)
	return nil
}

// app remove reads every site and the app's client, and is refused before
// it removes anything from either.
func TestAppRemoveIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	log := loggingSSH(t, "")
	var deleted []string
	saved := removeClients
	removeClients = func(*config.Config, string, string) appremove.Clients { return oldClient{&deleted} }
	t.Cleanup(func() { removeClients = saved })
	recordConverge(t)
	read := appliedByNewer(t, map[string]string{"watch": "99.0.0"})
	after := sentAfterCheck(t, log)
	err := runAppRemove([]string{"old", "--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--execute"}, strings.NewReader(""), io.Discard)
	refusedAsNewer(t, err, "watch", "app remove old")
	readAll(t, *read, "home-a", "home-b", "vm", "watch")
	if sent := after(); sent != "" || len(deleted) != 0 {
		t.Errorf("a refused removal deleted %v and reached a host after the check:\n%s", deleted, sent)
	}
}

// admittedRotateHost is a rotateFake that holds the admission record admit
// writes for its site, so mesh rekey reads it as running the deployment,
// and whose Patroni is not paused.
type admittedRotateHost struct {
	apply.Transport
	path, record string
}

func (h admittedRotateHost) ReadFile(path string) (string, bool, error) {
	if path == h.path {
		return h.record, true, nil
	}
	return h.Transport.ReadFile(path)
}

func (h admittedRotateHost) Run(command string) (string, error) {
	if strings.Contains(command, "patronictl") && strings.Contains(command, " show-config") {
		return "loop_wait: 10\n", nil
	}
	return h.Transport.Run(command)
}

// mesh rekey is refused after it reads every site and before it saves a
// next key or pauses Patroni.
func TestMeshRekeyIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	quietSSH(t)
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	before, _ := os.ReadFile(secretsPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	saved := rekeyHost
	rekeyHost = func(name string, site config.Site) apply.Transport {
		return admittedRotateHost{rotateHost(name, site), admittedPath(cfg), admittedRecord(t, cfg, name)}
	}
	t.Cleanup(func() { rekeyHost = saved })
	read := appliedByNewer(t, map[string]string{"home-b": "99.0.0"})
	var at int
	atToolkitCheck(t, func() { at = len(w.commands) })
	err = runMeshRekey([]string{"home-a", "--config", configPath, "--secrets", secretsPath, "--execute"})
	refusedAsNewer(t, err, "home-b", "mesh rekey")
	readAll(t, *read, "home-a", "home-b", "vm", "watch")
	if len(w.commands) != at {
		t.Errorf("a refused rekey reached a host after the check:\n%s\n%s", strings.Join(w.commands[at:], "\n"), rec.Lines())
	}
	if now, _ := os.ReadFile(secretsPath); !bytes.Equal(before, now) {
		t.Error("a refused rekey wrote the secrets file")
	}
}

// gatewayHost is a site as gateway move reaches it: empty, or holding the
// deployment record when it is the former gateway, and keeping each
// command and write it was sent.
type gatewayHost struct {
	emptyHost
	record string
	sent   *[]string
}

func (h gatewayHost) Run(command string) (string, error) {
	*h.sent = append(*h.sent, command)
	return h.emptyHost.Run(command)
}

func (h gatewayHost) RunInput(command, stdin string) (string, error) {
	*h.sent = append(*h.sent, command)
	return "", nil
}

func (h gatewayHost) ReadFile(path string) (string, bool, error) {
	return "{}", h.record != "" && path == h.record, nil
}

func (h gatewayHost) WriteFile(path, _ string, _ uint32) error {
	*h.sent = append(*h.sent, "write "+path)
	return nil
}

// gateway move finds home-a still holding the gateway's record, and is
// refused before its first stage: no record is written to the zone and
// nothing reaches either gateway after the check.
func TestGatewayMoveIsRefusedWhenANewerPaisansLastAppliedAGateway(t *testing.T) {
	path := joinConfig(t)
	body, _ := os.ReadFile(path)
	edited := strings.Replace(strings.Replace(string(body), "provider: desec", "provider: cloudflare", 1), "    endpoint: 192.0.2.1:51820\n", "    endpoint: 192.0.2.1:51820\n    public_address: 198.51.100.7\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	zone := dnstest.New()
	zone.Zones["example.org"] = "zone-1"
	savedHost, savedOptions := gatewayTransport, gatewayMoveOptions
	t.Cleanup(func() { gatewayTransport, gatewayMoveOptions = savedHost, savedOptions })
	gatewayTransport = func(name string, _ config.Site) apply.Transport {
		h := gatewayHost{sent: &sent}
		if name == "home-a" {
			h.record = deployrecord.Path(cfg.Deployment())
		}
		return h
	}
	gatewayMoveOptions = func() gatewaymove.Options { return gatewaymove.Options{DNSProvider: zone.For("cloudflare")} }
	quietSSH(t)
	rec := recordConverge(t)
	read := appliedByNewer(t, map[string]string{"home-a": "99.0.0"})
	var at int
	atToolkitCheck(t, func() { at = len(sent) })
	err = gatewayMoveCommand([]string{"--config", path, "--secrets", mustAbs(t, fixtureSecretsPath()), "--execute"})
	refusedAsNewer(t, err, "home-a", "gateway move")
	readAll(t, *read, "home-a", "vm")
	if len(sent) != at || len(zone.InZone["zone-1"]) != 0 {
		t.Errorf("a refused move sent %q and wrote %v to the zone\n%s", sent[at:], zone.InZone["zone-1"], rec.Lines())
	}
}

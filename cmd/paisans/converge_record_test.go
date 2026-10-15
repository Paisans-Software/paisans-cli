package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const orphanWarning = "secrets: sites.monitor-a names a site paisans-stack.yaml does not declare"

// orphanedDeployment is a deployment directory with the fixture
// configuration and the fixture secrets, plus a secret for monitor-a, a site
// paisans-stack.yaml does not declare.
func orphanedDeployment(t *testing.T) (configPath, secretsPath string) {
	t.Helper()
	configPath, secretsPath = secretsDir(t, "")
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	secrets.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "not-a-real-key-0003"}
	if err := config.WriteSecrets(secretsPath, secrets, nil); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

// readsGateway is a gateway holding the fixture's deployment record, which
// counts each time it is reached.
type readsGateway struct{ reached map[string]int }

func (g readsGateway) Describe() string { return "ubuntu@vm.example.org" }

func (g readsGateway) ReadFile(string) (string, bool, error) {
	g.reached["vm"]++
	return fixtureRecord(), true, nil
}

func (g readsGateway) Run(string) (string, error) {
	g.reached["vm"]++
	return fixtureRecord(), nil
}

// noRecord is a gateway that answers without a deployment record.
type noRecord struct{}

func (noRecord) Describe() string                      { return "ubuntu@vm.example.org" }
func (noRecord) ReadFile(string) (string, bool, error) { return "", false, nil }
func (noRecord) Run(string) (string, error)            { return "__PAISANS_ABSENT__\n", nil }

// fakeGatewayReads points registryHost at a readsGateway, and returns how
// often each gateway was reached.
func fakeGatewayReads(t *testing.T) map[string]int {
	t.Helper()
	reached := map[string]int{}
	saved := registryHost
	registryHost = func(name string, _ config.Site, _ string) registry.Runner {
		if name != "vm" {
			t.Errorf("%s reached as a gateway", name)
		}
		return readsGateway{reached}
	}
	t.Cleanup(func() { registryHost = saved })
	return reached
}

// mentions is each event of rec's that names monitor-a's orphaned secret.
func mentions(rec *ui.Recorder) []ui.Event {
	var out []ui.Event
	for _, e := range rec.Events {
		if strings.Contains(e.Text, "monitor-a") || strings.Contains(e.Extra, "monitor-a") {
			out = append(out, e)
		}
	}
	return out
}

// A full apply checks the secrets against the deployment record once, under
// a section of its own, whether it is a dry run or not.
func TestConvergeWarnsAboutAnOrphanedSecretOnce(t *testing.T) {
	configPath, secretsPath := orphanedDeployment(t)
	for _, extra := range [][]string{nil, {"--execute"}} {
		fakeConverge(t, nil)
		fakeChecks(t, nil)
		reached := fakeGatewayReads(t)
		rec := recordConverge(t)
		var err error
		captureStdout(t, func() {
			err = runApply(append([]string{"--config", configPath, "--secrets", secretsPath}, extra...), strings.NewReader(""), io.Discard)
		})
		if err != nil {
			t.Fatal(err)
		}
		if m := mentions(rec); len(m) != 1 || m[0].Kind != "warn" || !strings.HasPrefix(m[0].Text, orphanWarning) {
			t.Errorf("%v: the orphan is not warned about exactly once: %+v\n%s", extra, m, rec.Lines())
		}
		if reached["vm"] != 1 {
			t.Errorf("%v: vm's record was read %d times", extra, reached["vm"])
		}
		s, read, warn := rec.Index("section", "secrets"), rec.Index("done", "read the deployment record"), rec.Index("warn", orphanWarning)
		if s < 0 || read < s || warn < read {
			t.Errorf("%v: section %d, read %d, warning %d:\n%s", extra, s, read, warn, rec.Lines())
		}
	}
}

// failingSSH puts an ssh on PATH that answers every command but the
// architecture's with a failure, and logs each command it is given, decoded,
// to the file it returns.
func failingSSH(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	sshAnswering(t, `#!/bin/sh
for last; do :; done
b=${last#*printf %s }
b=${b%% |*}
{ printf '%s\n' "$*"; printf %s "$b" | base64 -d 2>/dev/null; echo; } >> `+log+`
if [ "$(printf %s "$b" | base64 -d 2>/dev/null)" = "dpkg --print-architecture" ]; then echo amd64; exit 0; fi
echo "fake: refused" >&2
exit 1
`)
	return log
}

// The applies a full apply runs as its install lines neither read the record
// nor warn about the secrets again, even with -v: the run has done both once.
func TestConvergeInstallLinesDoNotRepeatTheSecretsCheck(t *testing.T) {
	configPath, secretsPath := orphanedDeployment(t)
	fakeConverge(t, nil)
	fake := convergeRun
	convergeRun = func(args []string) error {
		if args[0] == "apply" {
			return runStep(args)
		}
		return fake(args)
	}
	reached := fakeGatewayReads(t)
	log := failingSSH(t)
	rec := withRecorder(t, true)
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", configPath, "--secrets", secretsPath, "--execute", "-v"}, strings.NewReader(""), io.Discard)
	})
	if err == nil {
		t.Fatal("an apply through a failing ssh succeeded")
	}
	if m := mentions(rec); len(m) != 1 || m[0].Kind != "warn" {
		t.Errorf("the orphan is not warned about exactly once: %+v\n%s", m, rec.Lines())
	}
	if reached["vm"] != 1 {
		t.Errorf("vm's record was read %d times", reached["vm"])
	}
	cfg, _ := config.Load(configPath)
	if data, _ := os.ReadFile(log); strings.Contains(string(data), filepath.Base(deployrecord.Path(cfg.Deployment()))) {
		t.Errorf("an install line read the record over ssh:\n%s", data)
	}
	if !rec.Has("section", "install") {
		t.Errorf("no install line ran:\n%s", rec.Lines())
	}
}

// apply --site still reads the record and warns itself.
func TestApplySiteStillWarnsAboutAnOrphanedSecret(t *testing.T) {
	configPath, secretsPath := orphanedDeployment(t)
	reached := fakeGatewayReads(t)
	failingSSH(t)
	rec := withRecorder(t, false)
	var err error
	captureStdout(t, func() {
		err = runApply([]string{"--config", configPath, "--secrets", secretsPath, "--site", "home-a"}, strings.NewReader(""), io.Discard)
	})
	if err == nil {
		t.Fatal("an apply through a failing ssh succeeded")
	}
	if m := mentions(rec); len(m) != 1 || m[0].Kind != "warn" || !strings.HasPrefix(m[0].Text, orphanWarning) {
		t.Errorf("the orphan is not warned about exactly once: %+v\n%s", m, rec.Lines())
	}
	if reached["vm"] != 1 {
		t.Errorf("vm's record was read %d times", reached["vm"])
	}
}

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/registry"
)

// sitesDown replaces every way a command reaches a site with one that finds
// the site down, so no test reaches the network.
func sitesDown(t *testing.T) {
	t.Helper()
	down := func(name string) *storeHost { return &storeHost{name: name, down: true} }
	savedSite, savedRotate, savedKey, savedRekey, savedRemove := configSiteTransport, rotateHost, rotateKeyHost, rekeyHost, removeSiteHost
	savedRegistry, savedDoctor := registryHost, doctorTransport
	t.Cleanup(func() {
		configSiteTransport, rotateHost, rotateKeyHost, rekeyHost, removeSiteHost = savedSite, savedRotate, savedKey, savedRekey, savedRemove
		registryHost, doctorTransport = savedRegistry, savedDoctor
	})
	fake := func(name string, _ config.Site) apply.Transport { return down(name) }
	configSiteTransport, rotateHost, rotateKeyHost, rekeyHost, removeSiteHost = fake, fake, fake, fake, fake
	registryHost = func(name string, _ config.Site, _ string) registry.Runner { return down(name) }
	doctorTransport = func(tr apply.SSHTransport) apply.Transport { return down(tr.Site) }
}

// writerCopy is the fixture deployment's system copy, whose head holds the
// fixture's paisans-stack.yaml and secrets, and the head's files. Every site
// is down, so a command that goes on to reach hosts finds none.
func writerCopy(t *testing.T) (configstore.Local, configstore.Files) {
	t.Helper()
	_, pub := testKey(t)
	secrets, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	return systemCopyOf(t, snapshotted(configstore.Files{Paisans: []byte(fixtureAs(t, pub)), Secrets: secrets, SOPS: []byte("creation_rules: []\n")}))
}

// systemCopyOf is the fixture deployment's system copy, whose head holds f,
// on a workstation reaching no site.
func systemCopyOf(t *testing.T, f configstore.Files) (configstore.Local, configstore.Files) {
	t.Helper()
	l := withSystemCopy(t, fixtureDeploymentID)
	key, pub := testKey(t)
	s := newStore(t)
	sha, err := s.repo.Commit(f, nil, 1, key, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := l.Cache()
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.FetchBundle(mustBundle(t, s.answer(t))); err != nil {
		t.Fatal(err)
	}
	if err := l.Checkout(sha); err != nil {
		t.Fatal(err)
	}
	if err := l.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	if err := l.Remember(1, sha); err != nil {
		t.Fatal(err)
	}
	sitesDown(t)
	savedR := reporterOverride
	t.Cleanup(func() { reporterOverride = savedR })
	plainReporter(t)
	return l, f
}

// sysFiles is the three files of the system copy's directory as they are.
func sysFiles(t *testing.T, sys configstore.Local) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		b, _ := os.ReadFile(filepath.Join(sys.Dir, name))
		got[name] = string(b)
	}
	return got
}

// A command that writes the deployment's files, run in a folder with no
// draft on a workstation holding the system copy, starts a draft there from
// the system copy and makes its change to the draft; the system copy's files
// are as they were.
func TestWritersStartADraft(t *testing.T) {
	for _, c := range []struct {
		name  string
		run   func(t *testing.T, cfgPath string) error
		check func(t *testing.T, got configstore.Files)
	}{
		{"operator add", func(t *testing.T, p string) error {
			return runOperatorAdd([]string{"dora", "--name", "Dora Example", "--email", "dora@example.org",
				"--key", createCarolKey, "--site", "home-a", "--config", p}, notATerminal(t), io.Discard)
		}, func(t *testing.T, got configstore.Files) {
			cfg, err := config.LoadBytes(got.Paisans)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cfg.Operators["dora"]; !ok {
				t.Error("the draft does not name dora")
			}
		}},
		{"operator add with --id", func(t *testing.T, p string) error {
			return runOperatorAdd([]string{"dora", "--name", "Dora Example", "--email", "dora@example.org",
				"--key", createCarolKey, "--site", "home-a", "--config", p, "--id", fixtureDeploymentID}, notATerminal(t), io.Discard)
		}, nil},
		{"secrets set", func(t *testing.T, p string) error {
			return runSecretsSet([]string{"external.acme_dns_token", "--config", p}, strings.NewReader("a-new-token\n"))
		}, func(t *testing.T, got configstore.Files) {
			if !bytes.Contains(got.Secrets, []byte("a-new-token")) {
				t.Error("the draft's secrets do not hold the new value")
			}
		}},
		{"secrets rotate", func(t *testing.T, p string) error {
			return runSecretsRotate([]string{"cluster.admin_password", "--config", p}, strings.NewReader(""), io.Discard)
		}, nil},
		{"secrets rotate --execute", func(t *testing.T, p string) error {
			return runSecretsRotate([]string{"cluster.admin_password", "--execute", "--config", p}, strings.NewReader(""), io.Discard)
		}, nil},
		{"secrets prune", func(t *testing.T, p string) error {
			return runSecretsPrune([]string{"--without-record", "--config", p}, strings.NewReader(""), io.Discard)
		}, nil},
		{"storage rotate-key", func(t *testing.T, p string) error {
			return runStorageRotateKey([]string{"--app", "talk", "--config", p})
		}, nil},
		{"oidc client create", func(t *testing.T, p string) error {
			withIDPFake(t)
			return runOIDCClientCreate([]string{"--app", "talk", "--config", p})
		}, nil},
		{"site remove", func(t *testing.T, p string) error {
			return runSiteRemove([]string{"home-b", "--config", p}, strings.NewReader(""), io.Discard)
		}, nil},
		{"catalog add", func(t *testing.T, p string) error {
			repo, _ := catalogRepo(t)
			return runCatalogAdd([]string{"ex", repo, "--config", p})
		}, func(t *testing.T, got configstore.Files) {
			cfg, err := config.LoadBytes(got.Paisans)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cfg.Catalogs["ex"]; !ok {
				t.Error("the draft does not pin the catalog")
			}
		}},
		{"doctor --execute", func(t *testing.T, p string) error {
			return runDoctor([]string{"--site", "home-a", "--execute", "--config", p})
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			sys, head := writerCopy(t)
			before := sysFiles(t, sys)
			dir := filepath.Join(t.TempDir(), "work")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			err := c.run(t, filepath.Join(dir, config.FileName))
			d := draft.Draft{Dir: dir}
			if !d.Has() {
				t.Fatalf("no draft was started (%v)", err)
			}
			got, ferr := d.Files()
			if ferr != nil {
				t.Fatal(ferr)
			}
			m, ok := draft.ReadMarker(got.Paisans)
			if want := (draft.Marker{Revision: 1}); !ok || m.Revision != want.Revision || m.SHA == "" {
				t.Errorf("the draft's line is %+v (%v)", m, ok)
			}
			if c.check != nil {
				if err != nil {
					t.Fatalf("%v", err)
				}
				got.Paisans = draft.StripMarker(got.Paisans)
				c.check(t, got)
			}
			if after := sysFiles(t, sys); !mapsEqual(before, after) {
				t.Error("the system copy's files changed")
			}
			if before[config.FileName] != string(head.Paisans) {
				t.Error("the system copy's paisans-stack.yaml is not the head's")
			}
			noFolderState(t, dir)
		})
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// init, given the output of create on a new deployment, writes the draft
// beside it and makes no system copy: the first deploy does.
func TestInitNewDeploymentHasNoSystemCopyUntilDeploy(t *testing.T) {
	withDataDir(t)
	path, _ := initWorld(t, nil, fakeSites(), []byte{212, 37})
	if err := runInitQuietly(t, path); err != nil {
		t.Fatal(problemText(err), err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "secrets.enc.yaml")); err != nil {
		t.Errorf("init wrote no secrets: %v", err)
	}
	st := must(openStore())
	if listed, err := st.List(); err != nil || len(listed) != 0 {
		t.Errorf("the workstation holds %v (%v)", listed, err)
	}
	noFolderState(t, filepath.Dir(path))
}

// init in an empty folder, on a workstation holding a deployment's system
// copy, starts the draft from it first and makes its secrets there.
func TestInitInAnEmptyFolderStartsADraft(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "paisans-stack.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sys, _ := systemCopyOf(t, configstore.Files{Paisans: example})
	initWorld(t, nil, fakeSites(), []byte{212, 37})
	before := sysFiles(t, sys)
	dir := t.TempDir()
	var runErr error
	captureStdout(t, func() {
		runErr = runInit([]string{"--config", filepath.Join(dir, config.FileName)}, strings.NewReader(""), io.Discard)
	})
	if runErr != nil {
		t.Fatal(problemText(runErr), runErr)
	}
	got, err := draft.Draft{Dir: dir}.Files()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := draft.ReadMarker(got.Paisans); !ok || m.Revision != 1 {
		t.Errorf("the draft's line is %+v (%v)", m, ok)
	}
	if len(got.Secrets) == 0 {
		t.Error("init made no secrets in the draft")
	}
	if after := sysFiles(t, sys); !mapsEqual(before, after) {
		t.Error("the system copy's files changed")
	}
	noFolderState(t, dir)
}

// site remove names the draft folder, not --id, which names a host there,
// when the workstation holds several deployments and the folder no draft.
func TestSiteRemoveSeveralDeploymentsDoesNotOfferID(t *testing.T) {
	systemCopyOf(t, configstore.Files{Paisans: []byte("id: " + fixtureDeploymentID + "\n")})
	addSystemCopy(t, otherID)
	err := runSiteRemove([]string{"home-b", "--config", filepath.Join(t.TempDir(), config.FileName)}, strings.NewReader(""), io.Discard)
	text := problemText(err)
	if err == nil || !strings.Contains(text, "more than one deployment") || strings.Contains(text, "Name one with --id") || !strings.Contains(text, "deployment checkout") {
		t.Errorf("%v\n%s", err, text)
	}
}

// The writers that run beside a draft without being asked for leave the
// system copy alone: an apply's dry run fills the secrets it lacks into the
// draft, and an apply with --execute records the users it logs in as there.
func TestHiddenWritersLeaveTheSystemCopy(t *testing.T) {
	t.Run("dry run generates secrets", func(t *testing.T) {
		configPath, secretsPath := encryptedDeployment(t, dropGenerated)
		dir := filepath.Dir(configPath)
		head, err := draft.Draft{Dir: dir}.Files()
		if err != nil {
			t.Fatal(err)
		}
		sys, _ := systemCopyOf(t, head)
		before := sysFiles(t, sys)
		dryRun(t, configPath, secretsPath)
		after := mustSecrets(t, secretsPath)
		if after.Cluster.AdminPassword == "" || after.Sites["home-a"].HeartbeatToken == "" {
			t.Error("the draft lacks the generated secrets")
		}
		if got := sysFiles(t, sys); !mapsEqual(before, got) {
			t.Error("the system copy's files changed")
		}
	})
	t.Run("execute records the ssh user", func(t *testing.T) {
		path := fixtureWithoutUsers(t)
		secrets, err := os.ReadFile(fixtureSecretsPath())
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sys, _ := systemCopyOf(t, snapshotted(configstore.Files{Paisans: body, Secrets: secrets}))
		before := sysFiles(t, sys)
		fakeConverge(t, nil)
		archReadLogsIn(t)
		recordConverge(t)
		gatherFiles(t)
		failingSSH(t)
		loginNotes(t)
		fake := convergeRun
		convergeRun = func(args []string) error {
			if isApply(args) {
				return nil
			}
			return fake(args)
		}
		var runErr error
		captureStdout(t, func() {
			runErr = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath(), "--execute"}, noTerminalStdin(), io.Discard)
		})
		if runErr != nil {
			t.Fatal(problemText(runErr), runErr)
		}
		if got, _ := os.ReadFile(path); strings.Count(string(got), "user: paisans-") != 4 {
			t.Errorf("the draft records no ssh user:\n%s", got)
		}
		if got := sysFiles(t, sys); !mapsEqual(before, got) {
			t.Error("the system copy's files changed")
		}
	})
}

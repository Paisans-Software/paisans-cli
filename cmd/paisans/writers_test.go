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
)

// writerCopy is the fixture deployment's system copy, whose head holds the
// fixture's paisans-stack.yaml and secrets, and the head's files. Every site
// is down, so a command that goes on to reach hosts finds none.
func writerCopy(t *testing.T) (configstore.Local, configstore.Files) {
	t.Helper()
	l := withSystemCopy(t, fixtureDeploymentID)
	key, pub := testKey(t)
	secrets, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	f := snapshotted(configstore.Files{Paisans: []byte(fixtureAs(t, pub)), Secrets: secrets, SOPS: []byte("creation_rules: []\n")})
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
	savedSite, savedR := configSiteTransport, reporterOverride
	t.Cleanup(func() { configSiteTransport, reporterOverride = savedSite, savedR })
	configSiteTransport = func(name string, _ config.Site) apply.Transport {
		return &storeHost{name: name, down: true}
	}
	reporterOverride = nil
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
			if string(head.Paisans) != before[config.FileName] && before[config.FileName] != "" {
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
// copy, starts the draft from it first.
func TestInitInAnEmptyFolderStartsADraft(t *testing.T) {
	sys, _ := writerCopy(t)
	dir := t.TempDir()
	_ = runInit([]string{"--config", filepath.Join(dir, config.FileName)}, strings.NewReader(""), io.Discard)
	m, ok := draft.ReadMarker(must(draft.Draft{Dir: dir}.Files()).Paisans)
	if !ok || m.Revision != 1 {
		t.Errorf("the draft's line is %+v (%v)", m, ok)
	}
	_ = sys
}

// The writers that run beside a draft without being asked for (the secrets
// apply's dry run fills, the user a login records, the architectures a
// pre-flight reads) write the draft and never the system copy.
func TestHiddenWritersLeaveTheSystemCopy(t *testing.T) {
	sys, _ := writerCopy(t)
	before := sysFiles(t, sys)
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, config.FileName)
	if _, err := ensureDraft(p, ""); err != nil {
		t.Fatal(problemText(err), err)
	}
	// The fixture's first site records its architecture; this draft does not.
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Replace(body, []byte("    arch: amd64\n"), nil, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites["home-a"].Arch != "" {
		t.Fatal("the fixture's first site is not home-a, or records no arch")
	}
	if _, _, _, err := convergeGenerate(cfg, filepath.Join(dir, configstore.FileSecrets)); err != nil {
		t.Log(problemText(err))
	}
	savedLogins := recordLogins
	recordLogins = true
	t.Cleanup(func() { recordLogins = savedLogins })
	recordSSHUser(p, "home-a", "ubuntu")()
	if _, err := recordArchitectures(stdoutReporter(), p, cfg, map[string]string{"home-a": "amd64"}, true); err != nil {
		t.Fatal(err)
	}
	if c, err := config.Load(p); err != nil || c.Sites["home-a"].Arch != "amd64" {
		t.Error("the draft records no architecture")
	}
	if after := sysFiles(t, sys); !mapsEqual(before, after) {
		t.Error("the system copy's files changed")
	}
}

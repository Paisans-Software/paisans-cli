package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

func withKeystore(t *testing.T, s keystore.Store) {
	t.Helper()
	saved := config.AgeKeystore
	config.AgeKeystore = s
	t.Cleanup(func() { config.AgeKeystore = saved })
}

// runInitShowing runs init and returns its error and what reached stdout.
func runInitShowing(t *testing.T, args ...string) (error, string) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runInit(args, strings.NewReader(""), io.Discard) })
	return err, out
}

// lyingStore stores, then reads back a different key.
type lyingStore struct{ *keystore.Memory }

func (lyingStore) Get(string) (string, error) {
	return "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ", nil
}

// failingStore fails to store, as secret-tool does with no D-Bus session.
type failingStore struct{ keystore.Unavailable }

func (failingStore) Put(string, string, string) error {
	return errors.New("secret-tool store: exit status 1: Cannot autolaunch D-Bus without X11 $DISPLAY")
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// From scratch, init makes a key, keeps it under the deployment id, names
// its recipient in .sops.yaml, encrypts to it, and never shows the key.
func TestInitMakesAgeKey(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	path, rec := initWorld(t, nil, fakeSites(), nil)
	err, out := runInitShowing(t, "--config", path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := ks.Items[cfg.ID]
	if !ok {
		t.Fatalf("no item for %s: %v", cfg.ID, ks.Items)
	}
	identity, err := age.ParseX25519Identity(item.Key)
	if err != nil || identity.Recipient().String() != item.Recipient {
		t.Fatalf("item %v does not hold its recipient's key: %v", item, err)
	}
	dir := filepath.Dir(path)
	if got, _ := config.Recipients(dir); len(got) != 1 || got[0] != item.Recipient {
		t.Errorf(".sops.yaml names %v, want %s", got, item.Recipient)
	}
	secrets, err := config.LoadDeploymentSecrets(filepath.Join(dir, "secrets.enc.yaml"), cfg.ID)
	if err != nil || !secrets.Encrypted {
		t.Fatalf("secrets = %+v, %v, want encrypted and readable", secrets, err)
	}
	if !rec.Has("done", "make age key") || !rec.Has("warn", "only copy of the age key") || rec.Has("warn", "was written in plaintext") {
		t.Errorf("unexpected report:\n%s", rec.Lines())
	}
	if strings.Contains(rec.Lines()+out, "AGE-SECRET-KEY") {
		t.Error("the private key was shown")
	}
	if !strings.Contains(rec.Lines()+out, item.Recipient) {
		t.Error("the recipient was not shown")
	}
}

func TestInitMakesNoKeyWhen(t *testing.T) {
	other := func(t *testing.T) string {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		return id.Recipient().String()
	}
	for _, c := range []struct {
		name  string
		store keystore.Store
		setup func(t *testing.T, dir, id string, ks keystore.Store)
		// refusal is part of the error init returns, or "" for success.
		refusal string
		// sopsAfter is whether .sops.yaml exists afterwards.
		sopsAfter, secretsAfter bool
	}{
		{name: "a recipient is named", setup: func(t *testing.T, dir, _ string, _ keystore.Store) {
			if err := config.WriteSOPSConfig(dir, other(t)); err != nil {
				t.Fatal(err)
			}
		}, sopsAfter: true, secretsAfter: true},
		{name: "the secrets file exists", setup: func(t *testing.T, dir, _ string, _ keystore.Store) {
			if err := os.WriteFile(filepath.Join(dir, "secrets.enc.yaml"), []byte("version: 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, secretsAfter: true},
		{name: "an item exists", setup: func(t *testing.T, _, id string, ks keystore.Store) {
			key, _ := age.GenerateX25519Identity()
			if err := ks.Put(id, key.String(), key.Recipient().String()); err != nil {
				t.Fatal(err)
			}
		}, refusal: "already holds an age key"},
		{name: "no keystore", store: keystore.Unavailable{}, refusal: "no keystore"},
		{name: "store fails", store: failingStore{}, refusal: "no keystore"},
		{name: "read back differs", store: lyingStore{keystore.NewMemory()}, refusal: "read back"},
		{name: ".sops.yaml names no recipient", setup: func(t *testing.T, dir, _ string, _ keystore.Store) {
			if err := os.WriteFile(filepath.Join(dir, config.SOPSConfigName), []byte("creation_rules: []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, refusal: "names no age recipient", sopsAfter: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			withoutAgeKey(t)
			store := c.store
			if store == nil {
				store = keystore.NewMemory()
			}
			withKeystore(t, store)
			path, _ := initWorld(t, nil, fakeSites(), nil)
			id, _, err := config.EnsureID(path)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(path)
			if c.setup != nil {
				c.setup(t, dir, id, store)
			}
			before := map[string]keystore.MemoryItem{}
			if m, ok := store.(*keystore.Memory); ok {
				for k, v := range m.Items {
					before[k] = v
				}
			}
			err, _ = runInitShowing(t, "--config", path)
			switch {
			case c.refusal == "" && err != nil:
				t.Fatal(err)
			case c.refusal != "" && (err == nil || !strings.Contains(err.Error(), c.refusal)):
				t.Fatalf("init = %v, want a refusal containing %q", err, c.refusal)
			}
			if m, ok := store.(*keystore.Memory); ok && len(m.Items) != len(before) {
				t.Errorf("items changed from %v to %v", before, m.Items)
			}
			if got := exists(t, filepath.Join(dir, config.SOPSConfigName)); got != c.sopsAfter {
				t.Errorf(".sops.yaml exists = %v, want %v", got, c.sopsAfter)
			}
			if got := exists(t, filepath.Join(dir, "secrets.enc.yaml")); got != c.secretsAfter {
				t.Errorf("secrets.enc.yaml exists = %v, want %v", got, c.secretsAfter)
			}
		})
	}
}

// --age-private-key-file writes the key there, at 0600, and the keystore is
// not touched.
func TestInitAgePrivateKeyFile(t *testing.T) {
	withoutAgeKey(t)
	ks := keystore.NewMemory()
	withKeystore(t, ks)
	path, rec := initWorld(t, nil, fakeSites(), nil)
	keyFile := filepath.Join(t.TempDir(), "deployment.key")
	err, out := runInitShowing(t, "--config", path, "--age-private-key-file", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v, %v, want mode 0600", info, err)
	}
	data, _ := os.ReadFile(keyFile)
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Recipients(filepath.Dir(path)); len(got) != 1 || got[0] != identity.Recipient().String() {
		t.Errorf(".sops.yaml names %v", got)
	}
	if len(ks.Items) != 0 {
		t.Errorf("the keystore was written: %v", ks.Items)
	}
	if !strings.Contains(rec.Lines()+out, "SOPS_AGE_KEY_FILE='"+keyFile+"'") || strings.Contains(rec.Lines()+out, "AGE-SECRET-KEY") {
		t.Errorf("unexpected output:\n%s%s", rec.Lines(), out)
	}
}

func TestInitAgePrivateKeyFileRefusals(t *testing.T) {
	t.Run("the file exists", func(t *testing.T) {
		withoutAgeKey(t)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		keyFile := filepath.Join(t.TempDir(), "deployment.key")
		if err := os.WriteFile(keyFile, []byte("kept\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err, _ := runInitShowing(t, "--config", path, "--age-private-key-file", keyFile)
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("init = %v, want a refusal", err)
		}
		if data, _ := os.ReadFile(keyFile); string(data) != "kept\n" {
			t.Error("the existing file was changed")
		}
	})
	t.Run("relative path inside", func(t *testing.T) {
		withoutAgeKey(t)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		t.Chdir(filepath.Dir(path))
		err, _ := runInitShowing(t, "--config", "paisans-stack.yaml", "--age-private-key-file", filepath.Join("keys", "..", "deployment.key"))
		if err == nil || !strings.Contains(err.Error(), "beside paisans-stack.yaml") {
			t.Fatalf("init = %v, want a refusal", err)
		}
		if !strings.Contains(err.Error(), "once ~/.config/paisans exists") {
			t.Errorf("init = %v, want it to say the suggested directory must exist", err)
		}
		// The directory is named in full: "inside ." says nothing.
		if wd, _ := os.Getwd(); !strings.Contains(problemText(err), "is inside "+wd+",") {
			t.Errorf("init = %v, want it to name %s in full", problemText(err), wd)
		}
		if exists(t, "deployment.key") || exists(t, config.SOPSConfigName) {
			t.Error("something was written")
		}
	})
	t.Run("the deployment has a key", func(t *testing.T) {
		withoutAgeKey(t)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		if err, _ := runInitShowing(t, "--config", path); err != nil {
			t.Fatal(err)
		}
		keyFile := filepath.Join(t.TempDir(), "deployment.key")
		err, _ := runInitShowing(t, "--config", path, "--age-private-key-file", keyFile)
		if err == nil || !strings.Contains(err.Error(), "only for a new key") {
			t.Fatalf("init = %v, want a refusal", err)
		}
		if exists(t, keyFile) {
			t.Error("a key file was written")
		}
	})
}

// A key file inside a git repository is refused unless git ignores it, and
// a directory that holds no paisans-stack.yaml, as operator register
// --invite runs in, is no reason to refuse one.
func TestWriteKeyFileGitRepository(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	key := identity.String()
	const id = "ba69b17d-83bf-43d2-a326-587bcf051b1d"
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	nowhere := filepath.Join(t.TempDir(), config.FileName)

	_, err = writeKeyFile("operator register", id, nowhere, filepath.Join(repo, "keys", "..", "jane.key"), key)
	if err == nil || !strings.Contains(err.Error(), "inside a git repository") {
		t.Fatalf("writeKeyFile = %v, want a refusal", err)
	}
	if real, _ := filepath.EvalSymlinks(repo); !strings.Contains(problemText(err), "is inside "+real+", a git repository") {
		t.Errorf("writeKeyFile = %v, want it to name %s", problemText(err), real)
	}
	if exists(t, filepath.Join(repo, "jane.key")) {
		t.Error("the key file was written")
	}

	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeKeyFile("operator register", id, nowhere, filepath.Join(repo, "jane.key"), key); err != nil {
		t.Errorf("writeKeyFile ignored by git = %v, want it written", err)
	}

	dir := t.TempDir()
	if _, err := writeKeyFile("operator register", id, filepath.Join(dir, config.FileName), filepath.Join(dir, "jane.key"), key); err != nil {
		t.Errorf("writeKeyFile beside no %s = %v, want it written", config.FileName, err)
	}
}

// lookupStore is a keystore with a lookup command, as the Keychain and the
// Secret Service have.
type lookupStore struct{ *keystore.Memory }

func (lookupStore) LookupCommand(id string) string { return "lookup-age-key " + id }

func TestInitWithoutAgeVariableFindsTheKey(t *testing.T) {
	withoutAgeKey(t)
	withKeystore(t, lookupStore{keystore.NewMemory()})
	path, rec := initWorld(t, nil, fakeSites(), nil)
	err, out := runInitShowing(t, "--config", path)
	if err != nil {
		t.Fatal(err)
	}
	if shown := rec.Lines() + out; !strings.Contains(shown, "finds the key on its own") || !strings.Contains(shown, "SOPS_AGE_KEY_CMD='lookup-age-key ") {
		t.Errorf("unexpected output:\n%s", shown)
	}
}

// sopsRacingStore stores the key, then a .sops.yaml appears before init
// writes its own, so the write fails with the key already kept.
type sopsRacingStore struct {
	*keystore.Memory
	dir string
}

func (s sopsRacingStore) Put(id, key, recipient string) error {
	if err := s.Memory.Put(id, key, recipient); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, config.SOPSConfigName), []byte("creation_rules: []\n"), 0o644)
}

// When .sops.yaml cannot be written after the key was kept, the error says
// where the key was left, so a re-run's refusal is no surprise.
func TestInitSaysWhatWasLeftWhenSOPSConfigFails(t *testing.T) {
	withoutAgeKey(t)
	path, _ := initWorld(t, nil, fakeSites(), nil)
	ks := keystore.NewMemory()
	withKeystore(t, sopsRacingStore{Memory: ks, dir: filepath.Dir(path)})
	err, _ := runInitShowing(t, "--config", path)
	if err == nil {
		t.Fatal("init succeeded although .sops.yaml could not be written")
	}
	cfg, lerr := config.Load(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if want := ks.Describe(cfg.ID); !strings.Contains(err.Error(), want+", holding the new age key, was left in place") {
		t.Errorf("init = %v, want it to say %s was left in place", err, want)
	}
}

// Review Focus 1: with no keystore init refuses, and the re-run the refusal
// names, with --age-private-key-file, then succeeds.
func TestInitNoKeystoreThenKeyFile(t *testing.T) {
	withoutAgeKey(t)
	withKeystore(t, keystore.Unavailable{})
	path, _ := initWorld(t, nil, fakeSites(), nil)
	err, _ := runInitShowing(t, "--config", path)
	if err == nil || !strings.Contains(err.Error(), "--age-private-key-file") {
		t.Fatalf("init = %v, want a refusal naming --age-private-key-file", err)
	}
	keyFile := filepath.Join(t.TempDir(), "deployment.key")
	if err, _ := runInitShowing(t, "--config", path, "--age-private-key-file", keyFile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Recipients(filepath.Dir(path)); len(got) != 1 || got[0] != identity.Recipient().String() {
		t.Errorf(".sops.yaml names %v, want %s", got, identity.Recipient())
	}
}

// initDefaultOutput runs init with the reporter an operator gets without
// --verbose, and returns what it printed.
func initDefaultOutput(t *testing.T, args ...string) string {
	t.Helper()
	var b strings.Builder
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = nil })
	if err, _ := runInitShowing(t, args...); err != nil {
		t.Fatal(err)
	}
	t.Logf("init printed:\n%s", b.String())
	return b.String()
}

// Without --verbose, init still says where it kept the private key, on the
// line after the one that made it, and never shows the key itself.
func TestInitSaysWhereTheAgeKeyIs(t *testing.T) {
	t.Run("keystore", func(t *testing.T) {
		withoutAgeKey(t)
		ks := keystore.NewMemory()
		withKeystore(t, ks)
		path, _ := initWorld(t, nil, fakeSites(), nil)
		out := initDefaultOutput(t, "--config", path)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "  ok   make age key\n  ok   save age key: " + ks.Describe(cfg.ID) + "\n"
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
		if strings.Contains(out, "AGE-SECRET-KEY") {
			t.Error("the private key was shown")
		}
	})
	t.Run("file", func(t *testing.T) {
		withoutAgeKey(t)
		withKeystore(t, keystore.NewMemory())
		path, _ := initWorld(t, nil, fakeSites(), nil)
		keyFile := filepath.Join(t.TempDir(), "deployment.key")
		out := initDefaultOutput(t, "--config", path, "--age-private-key-file", keyFile)
		want := "  ok   make age key\n  ok   save age key: " + ui.ShortPath(keyFile) + "\n"
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
		// paisans cannot find a key file on its own, so the line that says
		// how to point it there is not left to --verbose.
		if !strings.Contains(out, "export SOPS_AGE_KEY_FILE='"+keyFile+"'") {
			t.Errorf("output lacks the export line:\n%s", out)
		}
		if strings.Contains(out, "AGE-SECRET-KEY") {
			t.Error("the private key was shown")
		}
	})
}

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

func ageIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// writeTemp writes data to a new file named name in a temporary directory
// and returns its path.
func writeTemp(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// sshKey makes an ed25519 SSH private key in OpenSSH form, protected by
// passphrase when it is not empty, and returns it and its age recipient.
func sshKey(t *testing.T, passphrase string) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

// From scratch with a SOPS_AGE_* variable set, init uses the variable's
// key: .sops.yaml names its public key, the keystore is left alone, the
// step says which variable the key came from at the default verbosity,
// and nothing claims to be the only copy of a key init did not make.
func TestInitUsesAgeKeyFromVariable(t *testing.T) {
	for _, c := range []struct {
		name string
		// set sets the variables and returns the recipient they give and
		// the variables the step should name.
		set func(t *testing.T) (recipient, from string)
		// decrypts is whether the test reads the file back with the key.
		decrypts bool
	}{
		{name: "SOPS_AGE_KEY", decrypts: true, set: func(t *testing.T) (string, string) {
			id := ageIdentity(t)
			t.Setenv("SOPS_AGE_KEY", id.String())
			return id.Recipient().String(), "SOPS_AGE_KEY"
		}},
		{name: "SOPS_AGE_KEY_FILE", decrypts: true, set: func(t *testing.T) (string, string) {
			id := ageIdentity(t)
			t.Setenv("SOPS_AGE_KEY_FILE", writeTemp(t, "keys.txt", "# created: today\n# public key: "+id.Recipient().String()+"\n"+id.String()+"\n"))
			return id.Recipient().String(), "SOPS_AGE_KEY_FILE"
		}},
		{name: "SOPS_AGE_KEY_CMD", decrypts: true, set: func(t *testing.T) (string, string) {
			id := ageIdentity(t)
			t.Setenv("SOPS_AGE_KEY_CMD", "cat "+writeTemp(t, "key", id.String()+"\n"))
			return id.Recipient().String(), "SOPS_AGE_KEY_CMD"
		}},
		{name: "SOPS_AGE_SSH_PRIVATE_KEY_FILE", decrypts: true, set: func(t *testing.T) (string, string) {
			key, recipient := sshKey(t, "")
			t.Setenv("SOPS_AGE_SSH_PRIVATE_KEY_FILE", writeTemp(t, "id_ed25519", key))
			return recipient, "SOPS_AGE_SSH_PRIVATE_KEY_FILE"
		}},
		{name: "SOPS_AGE_SSH_PRIVATE_KEY_FILE with a passphrase", set: func(t *testing.T) (string, string) {
			key, recipient := sshKey(t, "correct horse")
			t.Setenv("SOPS_AGE_SSH_PRIVATE_KEY_FILE", writeTemp(t, "id_ed25519", key))
			return recipient, "SOPS_AGE_SSH_PRIVATE_KEY_FILE"
		}},
		{name: "SOPS_AGE_SSH_PRIVATE_KEY_CMD", decrypts: true, set: func(t *testing.T) (string, string) {
			key, recipient := sshKey(t, "")
			t.Setenv("SOPS_AGE_SSH_PRIVATE_KEY_CMD", "cat "+writeTemp(t, "id_ed25519", key))
			return recipient, "SOPS_AGE_SSH_PRIVATE_KEY_CMD"
		}},
		{name: "the same key in two variables", decrypts: true, set: func(t *testing.T) (string, string) {
			id := ageIdentity(t)
			t.Setenv("SOPS_AGE_KEY", id.String())
			t.Setenv("SOPS_AGE_KEY_CMD", "cat "+writeTemp(t, "key", id.String()+"\n"))
			return id.Recipient().String(), "SOPS_AGE_KEY and SOPS_AGE_KEY_CMD"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			withoutAgeKey(t)
			ks := keystore.NewMemory()
			withKeystore(t, ks)
			path, _ := initWorld(t, nil, fakeSites(), nil)
			recipient, from := c.set(t)
			out := initDefaultOutput(t, "--config", path)

			dir := filepath.Dir(path)
			if got, _ := config.Recipients(dir); len(got) != 1 || got[0] != recipient {
				t.Errorf(".sops.yaml names %v, want %s", got, recipient)
			}
			if len(ks.Items) != 0 {
				t.Errorf("the keystore was written: %v", ks.Items)
			}
			if want := "  ok   age key: from " + from + ", public key " + recipient + "\n"; !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
			if strings.Contains(out, "make age key") || strings.Contains(out, "only copy") {
				t.Error("init spoke of a key it made")
			}
			if !strings.Contains(out, "secrets.enc.yaml is encrypted to this one age key\n") {
				t.Error("init did not say the file has one recipient")
			}
			if strings.Contains(out, "AGE-SECRET-KEY") || strings.Contains(out, "PRIVATE KEY") {
				t.Error("the private key was shown")
			}
			if !c.decrypts {
				return
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			secrets, err := config.LoadDeploymentSecrets(filepath.Join(dir, "secrets.enc.yaml"), cfg.ID)
			if err != nil || !secrets.Encrypted {
				t.Fatalf("secrets = %+v, %v, want encrypted and readable with the variable's key", secrets, err)
			}
		})
	}
}

// A variable init cannot take one key from is refused, and nothing is
// written or made in its place: not a key, not .sops.yaml, not secrets.
func TestInitRefusesAgeVariableWithoutOneKey(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(t *testing.T)
		args    []string
		refusal string
	}{
		{name: "several keys in one variable", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY", ageIdentity(t).String()+" "+ageIdentity(t).String())
		}, refusal: "hold 2 age keys"},
		{name: "different keys in two variables", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY", ageIdentity(t).String())
			t.Setenv("SOPS_AGE_KEY_FILE", writeTemp(t, "keys.txt", ageIdentity(t).String()+"\n"))
		}, refusal: "hold 2 age keys"},
		{name: "a file that is not there", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY_FILE", filepath.Join(t.TempDir(), "missing.txt"))
		}, refusal: "SOPS_AGE_KEY_FILE is set, but no age key could be read from it"},
		{name: "a value that is not a key", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY", "not-a-key")
		}, refusal: "SOPS_AGE_KEY is set, but no age key could be read from it"},
		{name: "a command that fails", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY_CMD", "false")
		}, refusal: "SOPS_AGE_KEY_CMD is set, but no age key could be read from it"},
		{name: "a command that prints nothing", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY_CMD", "true")
		}, refusal: "holds no age key"},
		{name: "a passphrase protected SSH key from a command", set: func(t *testing.T) {
			key, _ := sshKey(t, "correct horse")
			t.Setenv("SOPS_AGE_SSH_PRIVATE_KEY_CMD", "cat "+writeTemp(t, "id_ed25519", key))
		}, refusal: "passphrase protected"},
		{name: "--age-private-key-file as well", set: func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY", ageIdentity(t).String())
		}, args: []string{"--age-private-key-file", "KEYFILE"}, refusal: "--age-private-key-file asks for a new key, and SOPS_AGE_KEY names one"},
	} {
		t.Run(c.name, func(t *testing.T) {
			withoutAgeKey(t)
			ks := keystore.NewMemory()
			withKeystore(t, ks)
			path, _ := initWorld(t, nil, fakeSites(), nil)
			c.set(t)
			keyFile := filepath.Join(t.TempDir(), "deployment.key")
			args := []string{"--config", path}
			for _, a := range c.args {
				args = append(args, strings.ReplaceAll(a, "KEYFILE", keyFile))
			}
			err, out := runInitShowing(t, args...)
			if err == nil || !strings.Contains(err.Error(), c.refusal) {
				t.Fatalf("init = %v, want a refusal containing %q", err, c.refusal)
			}
			if strings.Contains(err.Error()+out, "AGE-SECRET-KEY") {
				t.Error("a private key was shown")
			}
			dir := filepath.Dir(path)
			if len(ks.Items) != 0 || exists(t, keyFile) || exists(t, filepath.Join(dir, config.SOPSConfigName)) || exists(t, filepath.Join(dir, "secrets.enc.yaml")) {
				t.Errorf("something was written: items %v", ks.Items)
			}
		})
	}
}

// keystoreAndVariable sets up a deployment whose keystore item holds one
// key while SOPS_AGE_KEY holds another, both recipients of its secrets
// file, and returns the configuration's path.
func keystoreAndVariable(t *testing.T, ks keystore.Store) string {
	t.Helper()
	withoutAgeKey(t)
	withKeystore(t, ks)
	path, _ := initWorld(t, nil, fakeSites(), nil)
	id, _, err := config.EnsureID(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, other := ageIdentity(t), ageIdentity(t)
	if err := ks.Put(id, saved.String(), saved.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	recipients := []string{saved.Recipient().String(), other.Recipient().String()}
	if err := config.WriteSOPSConfig(dir, strings.Join(recipients, ",")); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteSecrets(filepath.Join(dir, "secrets.enc.yaml"), &config.Secrets{Version: config.SecretsVersion, ID: id}, recipients); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY", other.String())
	return path
}

// A variable used in place of the deployment's saved key is said at the
// default verbosity, once, naming the item.
func TestAgeVariableOverKeystoreIsShown(t *testing.T) {
	ks := keystore.NewMemory()
	path := keystoreAndVariable(t, ks)
	out := initDefaultOutput(t, "--config", path)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	hint := "SOPS_AGE_KEY is set, so the saved age key is not used"
	if strings.Count(out, hint) != 1 || !strings.Contains(out, ks.Describe(cfg.ID)+" holds this deployment's age key") {
		t.Errorf("output does not say once that %s was passed over:\n%s", ks.Describe(cfg.ID), out)
	}
}

// Every command reports through the same notice: render, which decrypts
// and reaches no host, shows it too.
func TestAgeVariableOverKeystoreIsShownByRender(t *testing.T) {
	path := keystoreAndVariable(t, keystore.NewMemory())
	var b strings.Builder
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = nil })
	if err := runRender([]string{"--config", path, "--out", t.TempDir()}); err != nil {
		t.Logf("render: %v", err)
	}
	if !strings.Contains(b.String(), "SOPS_AGE_KEY is set, so the saved age key is not used") {
		t.Errorf("render did not say the saved key was passed over:\n%s", b.String())
	}
}

// A variable that names the saved key itself, as the export line init
// prints does, is not reported.
func TestAgeVariableNamingTheSavedKeyIsQuiet(t *testing.T) {
	ks := lookupStore{keystore.NewMemory()}
	path := keystoreAndVariable(t, ks)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := ks.Get(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY", saved)
	out := initDefaultOutput(t, "--config", path)
	if strings.Contains(out, "saved age key is not used") {
		t.Errorf("init reported the saved key passed over by itself:\n%s", out)
	}
}

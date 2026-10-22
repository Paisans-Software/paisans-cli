package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// ensureAgeKey makes the age key of a deployment built from scratch: no
// secrets file and no .sops.yaml beside it. When a SOPS_AGE_* variable is
// set it uses that key instead (useVariableKey). Otherwise it keeps the key
// in the keystore under the deployment id, or in keyFile when one is named,
// reads it back, and only then writes .sops.yaml, so nothing is ever
// encrypted to a key that was not kept. Any other deployment already
// answered where its key lives, and is left as it is. The private key is
// never shown: a secret printed to a terminal is in a scrollback buffer, and
// this one opens every other. README, "Rendered configuration is a build
// artifact".
func ensureAgeKey(r ui.Reporter, id, configPath, secretsPath, keyFile string) error {
	dir := filepath.Dir(secretsPath)
	sopsPath := filepath.Join(dir, config.SOPSConfigName)
	_, secretsErr := os.Stat(secretsPath)
	_, sopsErr := os.Stat(sopsPath)
	if !errors.Is(secretsErr, os.ErrNotExist) || !errors.Is(sopsErr, os.ErrNotExist) {
		recipients, err := config.Recipients(dir)
		if err != nil {
			return err
		}
		if errors.Is(secretsErr, os.ErrNotExist) && len(recipients) == 0 {
			return &ui.Problem{
				Hint:    config.SOPSConfigName + " names no age recipient",
				Explain: fmt.Sprintf("init makes a key only when there is no %s, since that file is yours once it exists. Add your public key under creation_rules, or move the file aside and re-run init to have a key made.", ui.ShortPath(sopsPath)),
			}
		}
		if keyFile != "" {
			return &ui.Problem{
				Hint:    "--age-private-key-file is only for a new key",
				Explain: "This deployment already has its age key, named in " + config.SOPSConfigName + " or already used for " + ui.ShortPath(secretsPath) + ", so there is no key to write. Drop the flag.",
			}
		}
		return nil
	}
	if variables := config.AgeVariablesSet(); len(variables) > 0 {
		return useVariableKey(r, variables, dir, sopsPath, secretsPath, keyFile)
	}

	step := r.Step("make age key")
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		step.Fail(err)
		return err
	}
	step.Done("")
	// Where the key went is its own line, shown at every verbosity: the
	// operator has to know which item or file is now the only copy, and a
	// line that only --verbose shows is one they never see.
	step = r.Step("save age key")
	key, recipient := identity.String(), identity.Recipient().String()
	var where, shown, export string
	if keyFile != "" {
		where, err = writeKeyFile(id, configPath, keyFile, key)
		shown = ui.ShortPath(where)
		export = "SOPS_AGE_KEY_FILE=" + shellQuote(where)
	} else {
		where, err = storeKey(id, key, recipient)
		shown = where
		if command := config.AgeKeystore.LookupCommand(id); command != "" {
			export = "SOPS_AGE_KEY_CMD=" + shellQuote(command)
		}
	}
	if err == nil {
		if werr := config.WriteSOPSConfig(dir, recipient); werr != nil {
			// The key is not removed, since deleting a key is not init's
			// decision. Saying where it is makes the next run's refusal of
			// an existing item or file expected rather than a surprise.
			err = &ui.Problem{
				Hint:    config.SOPSConfigName + " could not be written after the age key was kept",
				Explain: fmt.Sprintf("%s, holding the new age key, was left in place. Nothing was encrypted to it yet. Remove it before re-running init, or init refuses to make another.", where),
				Cause:   werr,
			}
		}
	}
	if err != nil {
		step.Fail(err)
		return err
	}
	step.Detail("public key %s, written to %s", recipient, ui.ShortPath(sopsPath))
	step.Done(shown)
	switch {
	case keyFile != "":
		// paisans does not look for a key file on its own, so without this
		// line the next command cannot decrypt.
		r.Note("set this before running "+progname.Name()+" or sops", "export "+export)
	case export != "":
		r.Detail("paisans finds the key on its own. To run sops by hand: export %s", export)
	}
	r.Warn("this is the only copy of the age key", "Losing it loses every secret in "+ui.ShortPath(secretsPath)+". Add a second recipient to "+config.SOPSConfigName+", another operator's key or an offline backup key, then run sops updatekeys "+ui.ShortPath(secretsPath)+".")
	return nil
}

// useVariableKey is ensureAgeKey for an operator who brings the key through
// a SOPS_AGE_* variable: every later command decrypts with what the
// variables name, so a key made and saved here would never be used. Its
// public key is written to .sops.yaml and nothing is kept, since the key is
// already wherever the variable finds it. A variable that gives no key, or
// variables that give several, are refused rather than answered with a new
// key the operator did not choose. The private key is never shown.
func useVariableKey(r ui.Reporter, variables []string, dir, sopsPath, secretsPath, keyFile string) error {
	if keyFile != "" {
		return &ui.Problem{
			Hint:    "--age-private-key-file asks for a new key, and " + andList(variables) + " names one",
			Explain: "init makes a key only when no SOPS_AGE_* variable names one, since every later command decrypts with the variable's key. Drop the flag to use that key, or unset " + andList(variables) + " to have a new key kept in the file.",
		}
	}
	step := r.Step("age key")
	key, err := config.ReadVariableKey()
	if err == nil {
		err = config.WriteSOPSConfig(dir, key.Recipient)
	}
	if err != nil {
		step.Fail(err)
		return err
	}
	step.Detail("public key written to %s", ui.ShortPath(sopsPath))
	step.Done("from " + andList(key.Variables) + ", public key " + key.Recipient)
	// Where the variable's key comes from, and whether anything else holds
	// it, is the operator's to know: the toolkit can say only that the file
	// is encrypted to this one key.
	r.Warn(ui.ShortPath(secretsPath)+" is encrypted to this one age key", "If what "+andList(key.Variables)+" reads is the only copy of the key, losing it loses every secret in the file. Add a second recipient to "+config.SOPSConfigName+", another operator's key or an offline backup key, then run sops updatekeys "+ui.ShortPath(secretsPath)+".")
	return nil
}

// storeKey keeps key in the keystore under id and reads it back.
func storeKey(id, key, recipient string) (string, error) {
	ks := config.AgeKeystore
	item := ks.Describe(id)
	err := ks.Put(id, key, recipient)
	if errors.Is(err, keystore.ErrExists) {
		return "", &ui.Problem{
			Hint:    "the keystore already holds an age key for this deployment",
			Explain: item + " is there from an earlier init that stopped. It is not replaced, since something may be encrypted to its key. Look at it, remove it if nothing uses it, and re-run init.",
			Cause:   err,
		}
	}
	if err != nil {
		return "", &ui.Problem{
			Hint:    "no keystore to keep the age key in",
			Explain: "init keeps this deployment's age key in the macOS Keychain or the Secret Service (secret-tool), and neither could store it. Either:\n- re-run with --age-private-key-file <path> to keep it in a file you choose, outside this directory\n- bring your own key: write its public key to " + config.SOPSConfigName + " under creation_rules and re-run init",
			Cause:   err,
		}
	}
	got, err := ks.Get(id)
	if err != nil || got != key {
		return "", &ui.Problem{
			Hint:    "the age key read back from the keystore is not the one stored",
			Explain: "Nothing was encrypted to it and " + config.SOPSConfigName + " was not written. Look at " + item + ", remove it, and re-run init.",
			Cause:   err,
		}
	}
	return item, nil
}

// writeKeyFile writes key to path at 0600, never over an existing file and
// never inside the directory that holds the configuration, which is usually
// a git repository: there the key would be one `git add .` from being
// committed. It reads the key back, and returns the absolute path.
func writeKeyFile(id, configPath, path, key string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if inside(filepath.Dir(configPath), abs) {
		// The directory is named in full: shortened relative to the
		// current directory it is often ".", which says nothing.
		dir := filepath.Dir(configPath)
		if a, err := filepath.Abs(dir); err == nil {
			dir = a
		}
		return "", &ui.Problem{
			Hint:    "the age key file would sit beside " + config.FileName,
			Explain: fmt.Sprintf("%s is inside %s, which is usually a git repository, so the key would be one git add from being committed. Name a path in an existing directory outside it, Eg: ~/.config/paisans/%s.key once ~/.config/paisans exists.", ui.ShortPath(abs), dir, deployment.Deployment{ID: id}.Token()),
		}
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return "", &ui.Problem{
			Hint:    ui.ShortPath(abs) + " already exists",
			Explain: "init never replaces a key file, since something may be encrypted to the key in it. Name a new path.",
		}
	}
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil || strings.TrimSpace(string(data)) != key {
		return "", &ui.Problem{
			Hint:    "the age key read back from " + ui.ShortPath(abs) + " is not the one written",
			Explain: "Nothing was encrypted to it and " + config.SOPSConfigName + " was not written. Remove the file and re-run init.",
			Cause:   err,
		}
	}
	return abs, nil
}

// shellQuote quotes s for a POSIX shell, so a printed export line can be
// pasted as it is, whatever the path holds.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// inside reports whether path is dir or below it, after symlinks, so a
// relative path or a symlinked directory cannot slip past.
func inside(dir, path string) bool {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real
		}
		// The file does not exist yet: resolve its directory.
		if real, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			return filepath.Join(real, filepath.Base(abs))
		}
		return abs
	}
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

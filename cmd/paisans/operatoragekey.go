package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans operator age-key` (docs/specs/2026-10-24-operator-invite.md,
// *paisans operator age-key*): the operator's age public key for a
// deployment, made first when there is none. operator register --invite
// takes the same step.

func runOperatorAgeKey(args []string, stdout io.Writer) error {
	fs := newFlagSet("operator age-key", flag.ExitOnError)
	reporter := commonFlags(fs)
	id := fs.String("id", "", "the deployment id the key is kept under (default: the id in the configuration beside it)")
	configPath := fs.String("config", config.FileName, configHelp)
	keyFile := fs.String("age-private-key-file", "", "keep the key in this file, outside the configuration's directory and any git repository that does not ignore it, rather than the keystore; an existing file is read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "operator age-key takes no argument", Explain: fmt.Sprintf("Got %v.", fs.Args())}
	}
	if *id == "" {
		cfg, err := config.Load(*configPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return &ui.Problem{Hint: "no deployment id to keep the age key under", Explain: "Name it with --id <deployment id>, or run this beside " + config.FileName + " or with --config."}
		case err != nil:
			return err
		}
		*id = cfg.ID
	}
	r := reporter()
	k, err := operatorAgeKey(r, "operator age-key", *id, *configPath, *keyFile)
	if err != nil {
		return err
	}
	// The recipient is the last line, alone, so a script can take it.
	_, err = fmt.Fprintln(stdout, k.Recipient)
	return err
}

// operatorAgeKey, for command, finds the operator's age key for the deployment id, or
// makes it, and returns its public key. The sources are taken in this order,
// and the first present wins: a SOPS_AGE_* variable, which must hold exactly
// one key and which nothing is kept for; the keystore item for id, used as
// it is; keyFile, read when it exists and made when it does not; and last a
// new keystore item. It never writes .sops.yaml, and never replaces a
// keystore item or a key file. The private key is never shown.
func operatorAgeKey(r ui.Reporter, command, id, configPath, keyFile string) (ageKey, error) {
	if variables := config.AgeVariablesSet(); len(variables) > 0 {
		step := r.Step("age key")
		key, err := config.ReadVariableKeyFor(command)
		if err != nil {
			step.Fail(err)
			return ageKey{}, err
		}
		step.Done("from " + andList(key.Variables) + ", public key " + key.Recipient)
		if _, err := config.AgeKeystore.Get(id); err == nil {
			r.Warn("the keystore holds an age key for this deployment, and "+andList(key.Variables)+" is set", "Every later command decrypts with what "+andList(key.Variables)+" reads, so "+config.AgeKeystore.Describe(id)+" is not used while it is set. Unset it to use the keystore's key.")
		}
		return ageKey{Recipient: key.Recipient, Source: "from " + andList(key.Variables)}, nil
	}

	stored, err := config.AgeKeystore.Get(id)
	switch {
	case err == nil:
		step := r.Step("age key")
		recipient, err := recipientOf(stored)
		if err != nil {
			err = &ui.Problem{
				Hint:    "the age key in the keystore cannot be read",
				Explain: config.AgeKeystore.Describe(id) + " does not hold one age secret key. It is not replaced, since something may be encrypted to its key. Look at it, remove it if nothing uses it, and run this again.",
				Cause:   err,
			}
			step.Fail(err)
			return ageKey{}, err
		}
		source := config.AgeKeystore.Describe(id)
		step.Done(source + ", public key " + recipient)
		return ageKey{Recipient: recipient, Source: source}, nil
	case !errors.Is(err, keystore.ErrNotFound) && !errors.Is(err, keystore.ErrUnavailable):
		err = &ui.Problem{Hint: "the keystore could not be read", Explain: "Looking for " + config.AgeKeystore.Describe(id) + " failed.", Cause: err}
		r.Step("age key").Fail(err)
		return ageKey{}, err
	}

	if keyFile != "" {
		abs, err := filepath.Abs(keyFile)
		if err != nil {
			return ageKey{}, err
		}
		if info, err := os.Stat(abs); err == nil {
			step := r.Step("age key")
			if info.IsDir() {
				err := &ui.Problem{Hint: ui.ShortPath(abs) + " is a directory", Explain: "--age-private-key-file names the file that holds the age key, or the one to make. Name a file. Nothing was made or written."}
				step.Fail(err)
				return ageKey{}, err
			}
			recipient, err := readKeyFile(abs)
			if err != nil {
				step.Fail(err)
				return ageKey{}, err
			}
			source := ui.ShortPath(abs)
			step.Done(source + ", public key " + recipient)
			// A key file is found only through SOPS_AGE_KEY_FILE, so the
			// operator needs the line whether the file was made or read.
			noteKeyHome(r, keyFile, "SOPS_AGE_KEY_FILE="+shellQuote(abs))
			return ageKey{Recipient: recipient, Source: source, where: abs}, nil
		}
	}

	k, export, err := makeAgeKey(r, command, id, configPath, keyFile, nil)
	if err != nil {
		return ageKey{}, err
	}
	noteKeyHome(r, keyFile, export)
	r.Warn("this is the only copy of the age key", "Losing it loses access to every secret encrypted to it. Keep a backup of "+k.Source+".")
	return k, nil
}

// boundAgeKey is the age key bound to deployment id, whose public key is
// bound and which the answer that bound it found at boundSource. Every
// source operatorAgeKey takes is searched for it, and the first in that
// order that holds it is used, even where an earlier source holds another
// key: a SOPS_AGE_* variable, the keystore item for id, and keyFile when it
// exists. None holding it is refused, and no key is made in its place.
func boundAgeKey(r ui.Reporter, id, bound, boundSource, keyFile string) (ageKey, error) {
	step := r.Step("age key")
	if variables := config.VariablesHolding(bound); len(variables) > 0 {
		step.Done("bound to this deployment, from " + andList(variables) + ", public key " + bound)
		return ageKey{Recipient: bound, Source: "from " + andList(variables)}, nil
	}
	// A key found elsewhere while a variable is set is one the later
	// commands do not decrypt with.
	besides := func() {
		if set := config.AgeVariablesSet(); len(set) > 0 {
			r.Warn(andList(set)+" is set and does not hold the age key bound to this deployment", "Every later command decrypts with what "+andList(set)+" reads, so the bound key is not used while it is set. Unset it, or point it at the bound key.")
		}
	}
	stored, err := config.AgeKeystore.Get(id)
	switch {
	case err == nil:
		if recipient, err := recipientOf(stored); err == nil && recipient == bound {
			source := config.AgeKeystore.Describe(id)
			step.Done("bound to this deployment, " + source + ", public key " + bound)
			besides()
			return ageKey{Recipient: bound, Source: source}, nil
		}
	case !errors.Is(err, keystore.ErrNotFound) && !errors.Is(err, keystore.ErrUnavailable):
		err = &ui.Problem{Hint: "the keystore could not be read", Explain: "Looking for " + config.AgeKeystore.Describe(id) + " failed. Nothing was made or written.", Cause: err}
		step.Fail(err)
		return ageKey{}, err
	}
	if keyFile != "" {
		abs, err := filepath.Abs(keyFile)
		if err != nil {
			return ageKey{}, err
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			if recipient, err := readKeyFile(abs); err == nil && recipient == bound {
				source := ui.ShortPath(abs)
				step.Done("bound to this deployment, " + source + ", public key " + bound)
				besides()
				noteKeyHome(r, keyFile, "SOPS_AGE_KEY_FILE="+shellQuote(abs))
				return ageKey{Recipient: bound, Source: source, where: abs}, nil
			}
		}
	}
	binding, err := ageBindingPath(id)
	if err != nil {
		return ageKey{}, err
	}
	from := ""
	if boundSource != "" {
		from = ", which it took from " + strings.TrimPrefix(boundSource, "from ")
	}
	err = &ui.Problem{
		Hint:    "no age key here is the one bound to this deployment",
		Explain: fmt.Sprintf("The first answer for deployment %s named the age key %s%s, and every later answer names the same one, so that what the inviter accepts opens with it. No SOPS_AGE_* variable, %s or --age-private-key-file holds it. Make it reachable through one of them and run this again, or remove %s to start over with another key. Nothing was made or written.", id, bound, from, config.AgeKeystore.Describe(id), ui.ShortPath(binding)),
	}
	step.Fail(err)
	return ageKey{}, err
}

// recipientOf is the public key of the age secret key in key.
func recipientOf(key string) (string, error) {
	identity, err := age.ParseX25519Identity(key)
	if err != nil {
		return "", err
	}
	return identity.Recipient().String(), nil
}

// readKeyFile is the public key of the one age secret key in the file at
// path. The file is not changed.
func readKeyFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	identities, err := age.ParseIdentities(f)
	if err != nil {
		return "", &ui.Problem{Hint: ui.ShortPath(path) + " holds no age key", Explain: "--age-private-key-file reads an existing file only when it holds one AGE-SECRET-KEY. It is not changed.", Cause: err}
	}
	if len(identities) != 1 {
		return "", &ui.Problem{Hint: fmt.Sprintf("%s holds %d age keys, and one is needed", ui.ShortPath(path), len(identities)), Explain: "Name a file holding the one key for this deployment. It is not changed."}
	}
	x, ok := identities[0].(*age.X25519Identity)
	if !ok {
		return "", &ui.Problem{Hint: ui.ShortPath(path) + " holds an age key that is not an X25519 key", Explain: "Name a file holding an AGE-SECRET-KEY. It is not changed."}
	}
	return x.Recipient().String(), nil
}

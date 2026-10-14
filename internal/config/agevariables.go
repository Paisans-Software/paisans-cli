package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	sopsage "github.com/getsops/sops/v3/age"
	"github.com/google/shlex"
	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// AgeVariablesSet is every SOPS_AGE_* variable that is set, in the order
// ageVariables lists them.
func AgeVariablesSet() []string {
	var out []string
	for _, name := range ageVariables {
		if os.Getenv(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

// VariableKey is the one age key the SOPS_AGE_* variables give: its public
// recipient, for .sops.yaml, and every variable that gave it.
type VariableKey struct {
	Recipient string
	Variables []string
}

// ReadVariableKey reads the key the set SOPS_AGE_* variables name, for init
// to write .sops.yaml with when the operator brings a key through the
// environment. Each variable is read the way the embedded sops (v3.13.3,
// age/keysource.go) reads it: SOPS_AGE_KEY holds keys separated by spaces or
// lines, SOPS_AGE_KEY_FILE names a file of one key per line, SOPS_AGE_KEY_CMD
// is split as a shell would and run without a shell, and the SSH pair name
// or print an SSH private key. Lines that are empty or start with "#" are
// skipped, as sops skips them.
//
// sops has no order among the variables: it reads every one that is set and
// tries every key they hold. So does this, and the answer is refused unless
// all of them together hold exactly one key, since .sops.yaml is written for
// one and nothing here can tell which of several is the deployment's. A
// variable that is set and cannot be read, or holds no key, is refused too:
// sops would fail on it later, and a key made in its place would be one the
// operator never chose. The private key is never kept or shown.
func ReadVariableKey() (VariableKey, error) {
	sources := map[string][]string{}
	var order []string
	for _, name := range AgeVariablesSet() {
		recipients, err := variableRecipients(name, os.Getenv(name))
		if err != nil {
			return VariableKey{}, &ui.Problem{
				Hint:    name + " is set, but no age key could be read from it",
				Explain: fmt.Sprintf("init uses the key a SOPS_AGE_* variable names rather than making one, and does not make one in its place. Fix %s, or unset it to have init make a key, and re-run init.", name),
				Cause:   err,
			}
		}
		for _, r := range recipients {
			if _, seen := sources[r]; !seen {
				order = append(order, r)
			}
			if s := sources[r]; len(s) == 0 || s[len(s)-1] != name {
				sources[r] = append(s, name)
			}
		}
	}
	if len(order) == 1 {
		return VariableKey{Recipient: order[0], Variables: sources[order[0]]}, nil
	}
	if len(order) == 0 {
		return VariableKey{}, errors.New("no SOPS_AGE_* variable is set")
	}
	lines := make([]string, len(order))
	for i, r := range order {
		lines[i] = "- " + r + ", from " + strings.Join(sources[r], " and ")
	}
	return VariableKey{}, &ui.Problem{
		Hint:    fmt.Sprintf("the SOPS_AGE_* variables hold %d age keys, and init needs one", len(order)),
		Explain: fmt.Sprintf("init names one public key in %s, and cannot tell which of these is this deployment's:\n%s\nSet the variables so they hold the one key, or write %s yourself, naming each public key to encrypt to under creation_rules, and re-run init.", SOPSConfigName, strings.Join(lines, "\n"), SOPSConfigName),
	}
}

// variableRecipients is the public key of every identity the variable name,
// set to value, holds.
func variableRecipients(name, value string) ([]string, error) {
	var recipients []string
	var err error
	switch name {
	case sopsage.SopsAgeKeyEnv:
		recipients, err = ageRecipients(strings.Join(strings.Fields(value), "\n"))
	case sopsage.SopsAgeKeyFileEnv:
		var data []byte
		if data, err = os.ReadFile(value); err == nil {
			recipients, err = ageRecipients(string(data))
		}
	case sopsage.SopsAgeKeyCmdEnv:
		var out []byte
		if out, err = keyCommand(value); err == nil {
			recipients, err = ageRecipients(string(out))
		}
	case sopsage.SopsAgeSshPrivateKeyFileEnv:
		var data []byte
		if data, err = os.ReadFile(value); err == nil {
			recipients, err = sshRecipient(data, value)
		}
	case sopsage.SopsAgeSshPrivateKeyCmdEnv:
		var out []byte
		if out, err = keyCommand(value); err == nil {
			recipients, err = sshRecipient(out, "")
		}
	default:
		return nil, fmt.Errorf("%s is not a SOPS_AGE_* variable", name)
	}
	if err == nil && len(recipients) == 0 {
		err = errors.New("it holds no age key")
	}
	return recipients, err
}

// ageRecipients parses text, one identity per line, with sops' own parser,
// and returns each identity's public key, sorted and without repeats.
func ageRecipients(text string) ([]string, error) {
	var ids sopsage.ParsedIdentities
	if err := ids.Import(text); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		var r string
		switch id := id.(type) {
		case *age.X25519Identity:
			r = id.Recipient().String()
		case *age.HybridIdentity:
			r = id.Recipient().String()
		default:
			return nil, errors.New("it holds an age plugin identity, whose public key init cannot work out")
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out, nil
}

// sshRecipient is the age recipient of an SSH private key, the public key
// in authorized_keys form without its comment. A passphrase protected key
// is read from a file without its passphrase, from the public key the file
// carries or the .pub beside it, as sops reads it; from a command, sops
// refuses one, and so does this.
func sshRecipient(data []byte, path string) ([]string, error) {
	var pub ssh.PublicKey
	signer, err := ssh.ParsePrivateKey(data)
	var locked *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &locked) && path == "":
		return nil, errors.New("the SSH key is passphrase protected, which sops does not accept from SOPS_AGE_SSH_PRIVATE_KEY_CMD")
	case errors.As(err, &locked):
		pub = locked.PublicKey
		if pub == nil {
			data, rerr := os.ReadFile(path + ".pub")
			if rerr != nil {
				return nil, fmt.Errorf("the SSH key is passphrase protected and its public key could not be read: %w", rerr)
			}
			if pub, _, _, _, err = ssh.ParseAuthorizedKey(data); err != nil {
				return nil, fmt.Errorf("parsing %s.pub: %w", path, err)
			}
		}
	case err != nil:
		return nil, fmt.Errorf("parsing the SSH private key: %w", err)
	default:
		pub = signer.PublicKey()
	}
	recipient := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if _, err := agessh.ParseRecipient(recipient); err != nil {
		return nil, fmt.Errorf("the SSH key cannot be an age recipient: %w", err)
	}
	return []string{recipient}, nil
}

// keyCommand runs command as sops runs SOPS_AGE_KEY_CMD: split as a shell
// would and started without one, its stdout the keys. sops also sets
// SOPS_AGE_RECIPIENT to the public key it wants; before a deployment has
// one there is none to set.
func keyCommand(command string) ([]byte, error) {
	args, err := shlex.Split(command)
	if err != nil {
		return nil, fmt.Errorf("parsing the command: %w", err)
	}
	if len(args) == 0 {
		return nil, errors.New("the command is empty")
	}
	out, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		return nil, fmt.Errorf("running %s: %w", args[0], err)
	}
	return out, nil
}

// KeystoreBypassed, when set, is told that a deployment's secrets were
// decrypted while a SOPS_AGE_* variable is set and the keystore holds the
// deployment's own key, which the variables do not: the key the operator
// saved is then not what opens the file, and nothing else would say so.
// cmd/paisans points it at the reporter of the command being run.
var KeystoreBypassed func(variables []string, item string)

// noticeBypass tells KeystoreBypassed when the keystore holds a key for id
// that the set variables do not hold. A variable that is the keystore's own
// lookup command holds it by construction; SOPS_AGE_KEY and
// SOPS_AGE_KEY_FILE are read to see. A command is not run a second time to
// find out, so another command counts as not holding it. A keystore that
// cannot be read says nothing: decryption already succeeded without it.
func noticeBypass(id string) {
	if KeystoreBypassed == nil {
		return
	}
	saved, err := AgeKeystore.Get(id)
	if err != nil {
		return
	}
	savedRecipients, err := ageRecipients(saved)
	if err != nil || len(savedRecipients) != 1 {
		return
	}
	set := AgeVariablesSet()
	for _, name := range set {
		value := os.Getenv(name)
		switch name {
		case sopsage.SopsAgeKeyCmdEnv:
			if command := AgeKeystore.LookupCommand(id); command != "" && value == command {
				return
			}
		case sopsage.SopsAgeKeyEnv, sopsage.SopsAgeKeyFileEnv:
			recipients, err := variableRecipients(name, value)
			if err != nil {
				continue
			}
			for _, r := range recipients {
				if r == savedRecipients[0] {
					return
				}
			}
		}
	}
	KeystoreBypassed(set, AgeKeystore.Describe(id))
}

// Package keystore keeps a deployment's age private key in the workstation's
// own secret store: the macOS Keychain through `security`, or the Secret
// Service through `secret-tool`. An item is named for the deployment id, so
// the configuration, the secrets file and the key are tied by one value and
// several deployments on one workstation never share an item.
//
// The key reaches each command on stdin and never in its arguments, where
// `ps` shows it to every user on the machine. README, "Rendered
// configuration is a build artifact".
package keystore

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"

	"github.com/paisans-software/paisans-stack/internal/deployment"
)

// Service is the service name every item is stored under.
const Service = "paisans-age"

var (
	// ErrNotFound is an id with no item.
	ErrNotFound = errors.New("no keystore item")
	// ErrExists is a Put for an id that already has an item. An item is
	// never replaced: something may be encrypted to the key in it.
	ErrExists = errors.New("a keystore item already exists")
	// ErrUnavailable is a workstation with no keystore paisans can use.
	ErrUnavailable = errors.New("no keystore is available")
)

// Store is a keystore. Tests replace it.
type Store interface {
	// Get returns the key stored for the deployment id, or ErrNotFound.
	Get(id string) (string, error)
	// Put stores key for id with its public recipient beside it, or returns
	// ErrExists and changes nothing.
	Put(id, key, recipient string) error
	// Describe names the item for id in an operator's words.
	Describe(id string) string
	// LookupCommand is a command line that prints the key for id, for
	// SOPS_AGE_KEY_CMD, or "" when there is none.
	LookupCommand(id string) string
}

// Platform is the keystore this workstation has, or Unavailable.
func Platform() Store {
	switch runtime.GOOS {
	case "darwin":
		if bin, err := exec.LookPath("security"); err == nil {
			return keychain{bin: bin}
		}
	case "linux":
		if bin, err := exec.LookPath("secret-tool"); err == nil {
			return secretService{bin: bin}
		}
	}
	return Unavailable{}
}

var (
	keyPattern       = regexp.MustCompile(`^AGE-SECRET-KEY-1[0-9A-Z]+$`)
	recipientPattern = regexp.MustCompile(`^age1[0-9a-z]+$`)
)

// checkID refuses anything but a deployment id, since it reaches a command.
// The deployment package spells what an id is, so the two never disagree.
func checkID(id string) error {
	if !deployment.ValidID(id) {
		return fmt.Errorf("keystore: %q is not a deployment id", id)
	}
	return nil
}

// checkItem refuses a malformed id, key or recipient before any command
// runs. The keychain's store is a command line written to stdin, so a value
// with a space or a quote in it would change what that line does.
func checkItem(id, key, recipient string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if !keyPattern.MatchString(key) {
		return errors.New("keystore: the key is not an age private key")
	}
	if !recipientPattern.MatchString(recipient) {
		return fmt.Errorf("keystore: %q is not an age recipient", recipient)
	}
	return nil
}

func label(id string) string { return "paisans age key " + id }

// Unavailable is a workstation with no keystore.
type Unavailable struct{}

func (Unavailable) Get(string) (string, error)       { return "", ErrUnavailable }
func (Unavailable) Put(string, string, string) error { return ErrUnavailable }
func (Unavailable) Describe(id string) string        { return "no keystore" }
func (Unavailable) LookupCommand(string) string      { return "" }

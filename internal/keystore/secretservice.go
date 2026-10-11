package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// secretService is the freedesktop Secret Service, through `secret-tool`.
// lookup exits 1 with nothing on stderr for a missing item; a failure, Eg:
// no D-Bus session, exits 1 with a message.
type secretService struct{ bin string }

func (s secretService) Get(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(s.bin, "lookup", "service", Service, "account", id)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if exitCode(err) == 1 && strings.TrimSpace(stderr.String()) == "" {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("secret-tool lookup: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Put reads the key from stdin, which `secret-tool store` takes as the
// secret. store replaces an item silently, so the item is looked for first.
func (s secretService) Put(id, key, recipient string) error {
	if err := checkItem(id, key, recipient); err != nil {
		return err
	}
	if _, err := s.Get(id); err == nil {
		return ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.Command(s.bin, "store", "--label="+label(id), "service", Service, "account", id, "recipient", recipient)
	cmd.Stdin, cmd.Stderr = strings.NewReader(key), &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret-tool store: %w: %s", err, redact(stderr.String(), key))
	}
	return nil
}

func (secretService) Describe(id string) string {
	return "Secret Service item service=" + Service + " account=" + id
}

func (secretService) LookupCommand(id string) string {
	return "secret-tool lookup service " + Service + " account " + id
}

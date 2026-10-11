package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// keychain is the macOS Keychain, through the `security` command. Exit
// status 44 is errSecItemNotFound and 45 is errSecDuplicateItem.
type keychain struct{ bin string }

func (k keychain) Get(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(k.bin, "find-generic-password", "-s", Service, "-a", id, "-w")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if exitCode(err) == 44 {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("security find-generic-password: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Put writes add-generic-password to `security -i` on stdin, so the key is
// never in an argument list. The comment (-j) carries the recipient, so an
// item can be matched to a .sops.yaml without decrypting anything.
func (k keychain) Put(id, key, recipient string) error {
	if err := checkItem(id, key, recipient); err != nil {
		return err
	}
	if _, err := k.Get(id); err == nil {
		return ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	line := fmt.Sprintf("add-generic-password -s %s -a %s -l \"%s\" -j %s -w %s\n", Service, id, label(id), recipient, key)
	var stderr bytes.Buffer
	cmd := exec.Command(k.bin, "-i")
	cmd.Stdin, cmd.Stderr = strings.NewReader(line), &stderr
	err := cmd.Run()
	if exitCode(err) == 45 {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("security add-generic-password: %w: %s", err, redact(stderr.String(), key))
	}
	return nil
}

func (keychain) Describe(id string) string { return "Keychain item " + Service + "/" + id }

func (keychain) LookupCommand(id string) string {
	return "security find-generic-password -s " + Service + " -a " + id + " -w"
}

// exitCode is err's exit status, or -1 when it is not an exit.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// redact removes key from a command's own message before it becomes an
// error, in case the command echoes what it was given.
func redact(message, key string) string {
	return strings.TrimSpace(strings.ReplaceAll(message, key, "<key>"))
}

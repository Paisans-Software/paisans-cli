// Package hostadmit holds what runs on a host as its owner, as root, for one
// deployment: the steps that admit it and evict it. Every other command runs
// as the deployment's own user.
package hostadmit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// Local is the transport for a command run on the host itself: each command
// through sh -c on this machine, each file through the filesystem. It is how
// host admit and host evict run from a request, by the owner, with no ssh.
// It is reached only through this function, and only this package's
// commands call it, so a mistyped flag on a workstation cannot run a host's
// plan against the workstation.
func Local() apply.Transport { return local{} }

type local struct{}

func (local) Describe() string {
	if name, err := os.Hostname(); err == nil && name != "" {
		return "this machine (" + name + ")"
	}
	return "this machine"
}

func (l local) Run(command string) (string, error) { return l.run(command, nil) }

func (l local) RunInput(command, stdin string) (string, error) { return l.run(command, &stdin) }

// run runs command with sh -c, with stdin when it is not nil, and returns
// its combined output. A non zero exit is an error naming the command's
// first line, as the ssh transport's is.
func (l local) run(command string, stdin *string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %s: %w\n%s", l.Describe(), firstLine(command), err, strings.TrimRight(out.String(), "\n"))
	}
	return out.String(), nil
}

// ReadFile reads a regular file. A missing path, and one that is not a
// regular file, read as absent, as `[ -f ]` does over ssh.
func (local) ReadFile(path string) (string, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// WriteFile writes content to a temporary file beside path, sets its mode
// and renames it over path, creating the parent directories, so a failed
// write leaves the previous file whole.
func (local) WriteFile(path, content string, mode uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".paisans-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, os.FileMode(mode)); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

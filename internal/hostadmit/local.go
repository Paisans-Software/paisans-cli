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
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
	// A command starts in the running user's home, as it does over ssh.
	cmd.Dir = "/"
	if home, err := os.UserHomeDir(); err == nil {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			cmd.Dir = home
		}
	}
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), ui.RedactError(fmt.Errorf("%s: %s: %w\n%s", l.Describe(), firstLine(command), err, strings.TrimRight(out.String(), "\n")))
	}
	return out.String(), nil
}

// linkedSystemFiles are the system files a stock host may hold as links,
// which ReadFile alone reads through a link: on Ubuntu, /etc/os-release
// points into /usr/lib and, with systemd-resolved, /etc/resolv.conf into
// /run. They are read and never written. A path paisans owns is never on
// this list: the records, the registry, sudoers, /etc/paisans and the
// deployment's home are read with every link refused.
var linkedSystemFiles = []string{"/etc/os-release", "/etc/resolv.conf"}

// ReadFile reads a regular file. The directories on the way are opened one
// by one by descriptor and a link in any of them is an error naming it, as
// is a link as the file itself, because this process runs as root and the
// directories belong to the deployment's user. A missing path, and one
// through a regular file, read as absent, as `[ -f ]` does over ssh; any
// other kind of file, a FIFO among them, is an error and is never waited on.
// The path must be absolute.
//
// The one exception is a path in linkedSystemFiles: its links are resolved
// first, and the file they name is then read under the same rules, so a
// link that changes in between is refused. A link to nothing reads as
// absent.
func (l local) ReadFile(path string) (string, bool, error) {
	content, found, err := readLocal(path)
	if err != nil {
		err = ui.RedactError(fmt.Errorf("%s: reading %s: %w", l.Describe(), path, err))
	}
	return content, found, err
}

// WriteFile writes content to a new file with a random name in path's
// directory, sets its mode, including setuid, setgid and sticky, and renames
// it over path, so a failed write leaves the previous file whole. Missing
// directories are created. Every directory on the way is opened by
// descriptor without following links, and a link in any of them is an error
// naming it; the temporary file is created exclusively and changed only
// through its descriptor. A link planted by the directory's owner is
// therefore never followed by this process, which runs as root. The path
// must be absolute.
func (l local) WriteFile(path, content string, mode uint32) error {
	if err := writeFile(path, content, mode); err != nil {
		return ui.RedactError(fmt.Errorf("%s: writing %s: %w", l.Describe(), path, err))
	}
	return nil
}

// readLocal is readFile, with a path in linkedSystemFiles resolved first.
func readLocal(path string) (string, bool, error) {
	if !slices.Contains(linkedSystemFiles, path) {
		return readFile(path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return readFile(resolved)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

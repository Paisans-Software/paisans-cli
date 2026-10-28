package apply

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
)

// ShellArgs is ssh's argument list for an operator's own session on the
// host, with the public key files at keyFiles: a login shell, or command
// when it is given, as `ssh user@host command...` runs it.
//
// It is the site's login as every command makes it, its user, port and
// keys under IdentitiesOnly (see SSHArgs), with three differences, each
// because a person is at the other end rather than a command. There is no
// -T, so ssh gives a terminal when it would for a plain ssh: for a shell on
// a terminal, not for a command. There is no shared connection, since a
// session outlives the run that shares one. And command goes as written,
// not wrapped by RemoteCommand, since the operator wrote it for the login
// shell they get. The destination comes after `--`, so a word of command
// is never read as an option of ssh's own.
//
// It is for a transport built from a site's ssh section; Destination is not
// used.
func (t SSHTransport) ShellArgs(keyFiles []string, command []string) []string {
	args := t.sectionOptions()
	for _, f := range keyFiles {
		args = append(args, "-i", f)
	}
	return append(append(args, "--", t.User+"@"+t.Host), command...)
}

// Shell runs ssh to the host as ShellArgs builds it, on stdin, stdout and
// stderr, and returns when the session ends. An ssh that exits non-zero is
// an *exec.ExitError carrying its status, which is the remote command's or
// 255 for ssh's own failure, as ssh(1) says. The key files live for the
// session and are removed after it.
//
// An interrupt is left to ssh while it runs. A Ctrl-C at the terminal
// reaches both processes, and ssh hands it to the session (to the remote
// terminal, or by ending the command), so the session ends as a plain ssh's
// does and the key files are still removed.
func (t SSHTransport) Shell(command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(t.PublicKeys) == 0 {
		return fmt.Errorf("%s: no public key to offer. Name an admin with a key in the site's ssh.keys", t.Describe())
	}
	dir, err := os.MkdirTemp("", "paisans-ssh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	files, err := writeKeyFiles(dir, t.PublicKeys)
	if err != nil {
		return err
	}
	cmd := exec.Command("ssh", t.ShellArgs(files, command)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	return cmd.Run()
}

package apply_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// placeholder stands in for a sudo password. It is not anyone's.
const placeholder = "not-a-real-password"

// fakeSudo is a sudo for shellHost's PATH. `sudo -n` fails as it does where
// a password is needed. Otherwise it runs the command, after reading the
// password line from stdin only when reads is set: unset, it is a sudo that
// lets the command through without asking, as one does for a NOPASSWD rule
// that matches or a PAM module that needs no password, and the line it was
// sent is left on stdin.
func fakeSudo(t *testing.T, reads bool) string {
	t.Helper()
	dir := t.TempDir()
	read := ""
	if reads {
		read = "IFS= read -r password\n"
	}
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -n) echo 'sudo: a password is required' >&2; exit 1 ;;\n" +
		"    -k|-S) shift ;;\n" +
		"    -p) shift 2 ;;\n" +
		"    *) break ;;\n" +
		"  esac\n" +
		"done\n" +
		read +
		"exec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// shellHost runs every remote command in a real sh on this machine, with
// the fake sudo in dir first on its PATH, so what reaches each command's
// stdin is what a host's would get.
func shellHost(t *testing.T, dir string) {
	t.Helper()
	restore := apply.FakeSSH(func(args []string, stdin string) (string, int) {
		cmd := exec.Command("sh", "-c", args[len(args)-1])
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}, nil, func(time.Duration) {}, &strings.Builder{})
	t.Cleanup(restore)
}

// Whether or not the host's sudo reads the password line, the command run
// under it gets exactly its own stdin: a command that prints its stdin
// prints nothing of the password, a shell reading commands from stdin never
// runs it, and WriteFile writes the content alone.
func TestSudoNeverHandsThePasswordToTheCommand(t *testing.T) {
	for _, reads := range []bool{false, true} {
		shellHost(t, fakeSudo(t, reads))
		p := &prompter{answer: placeholder}
		tr := sudoTransport(apply.NewSudoAuth(p.prompt))

		out, err := tr.Run("cat")
		if err != nil || out != "" {
			t.Errorf("reads=%v: cat printed %q, %v; want nothing", reads, out, err)
		}
		out, err = tr.Run("sh")
		if err != nil || out != "" {
			t.Errorf("reads=%v: a shell reading stdin printed %q, %v; want nothing", reads, out, err)
		}
		out, err = tr.RunInput("cat", "payload\nline two\n")
		if err != nil || out != "payload\nline two\n" {
			t.Errorf("reads=%v: RunInput's command read %q, %v; want the payload alone", reads, out, err)
		}
		path := filepath.Join(t.TempDir(), "file")
		if err := tr.WriteFile(path, "content", 0o600); err != nil {
			t.Fatalf("reads=%v: %v", reads, err)
		}
		if got, _ := os.ReadFile(path); string(got) != "content" {
			t.Errorf("reads=%v: WriteFile wrote %q, want the content alone", reads, got)
		}
		if len(p.asked) != 1 {
			t.Errorf("reads=%v: asked %d times", reads, len(p.asked))
		}
	}
}

// echoHost is a host that prints back the first line of whatever stdin it
// is sent, as one that runs commands on a terminal does, and fails what is
// run after the password check.
func echoHost(t *testing.T, failCheck bool) {
	t.Helper()
	restore := apply.FakeSSH(func(args []string, stdin string) (string, int) {
		command := args[len(args)-1]
		switch {
		case command == "uname -n":
			return "box-01\n", 0
		case command == "sudo -n true":
			return "sudo: a password is required\n", 1
		}
		line, _, _ := strings.Cut(stdin, "\n")
		if strings.HasSuffix(command, "; true'") && !failCheck {
			return line + "\n", 0
		}
		return line + "\nsudo: 1 incorrect password attempt\n", 1
	}, nil, func(time.Duration) {}, &strings.Builder{})
	t.Cleanup(restore)
}

// A host that prints the password back, which no stdin layout can prevent,
// still never gets it into an error: each the transport returns is
// redacted, its text and a Problem's every field, and the password is
// registered as a secret the moment it is typed, so the reporter strips it
// from whatever else quotes the host's output.
func TestTheTransportsErrorsNeverCarryThePassword(t *testing.T) {
	echoHost(t, false)
	tr := sudoTransport(apply.NewSudoAuth((&prompter{answer: placeholder}).prompt))
	_, err := tr.Run("anything")
	if err == nil {
		t.Fatal("the failing command succeeded")
	}
	_, inErr := tr.RunInput("anything", "payload")
	writeErr := tr.WriteFile("/srv/x", "content", 0o600)
	for _, err := range []error{err, inErr, writeErr} {
		if err == nil || strings.Contains(err.Error(), placeholder) {
			t.Errorf("error %v carries the password, or is missing", err)
		}
	}
	if ui.Redact(placeholder) == placeholder {
		t.Error("the password typed at the prompt is not registered as a secret")
	}

	echoHost(t, true)
	_, err = sudoTransport(apply.NewSudoAuth((&prompter{answer: placeholder}).prompt)).Run("anything")
	var problem *ui.Problem
	if !errors.As(err, &problem) || !errors.Is(err, apply.ErrSudo) {
		t.Fatalf("a refused password is %v, want a Problem and ErrSudo", err)
	}
	if strings.Contains(err.Error(), placeholder) || strings.Contains(problem.Explain, placeholder) || strings.Contains(problem.Hint, placeholder) {
		t.Errorf("the refusal carries the password: %v", err)
	}
	var b strings.Builder
	ui.PrintError(&b, err, true)
	if strings.Contains(b.String(), placeholder) {
		t.Errorf("printed with --verbose, the refusal shows the password:\n%s", b.String())
	}
}

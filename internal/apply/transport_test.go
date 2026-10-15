package apply_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Obviously fake: an ed25519 key whose 32 bytes are all zero, and all one.
const (
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
)

// The argument list is the whole of what the transport decides about a
// connection, so it is pinned exactly.
func TestSSHArgsForASection(t *testing.T) {
	tr := apply.SSHTransport{User: "ubuntu", Host: "203.0.113.10", Port: 2222, PublicKeys: []string{keyA, keyB}}
	got := tr.SSHArgs([]string{"/tmp/k/key-1.pub", "/tmp/k/key-2.pub"}, "uptime")
	want := append(apply.MuxArgs(), "-T", "-p", "2222", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=10", "-i", "/tmp/k/key-1.pub", "-i", "/tmp/k/key-2.pub", "ubuntu@203.0.113.10", apply.RemoteCommand("", "uptime"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh args:\n got %q\nwant %q", got, want)
	}
	if d := tr.Describe(); d != "ubuntu@203.0.113.10 port 2222" {
		t.Errorf("Describe() = %q", d)
	}
}

// A connect timeout is one more option before the keys. A host that is
// switched off, or a VM that has been deleted, would otherwise cost the
// operating system's TCP connect timeout on every attempt, so a transport
// that sets none gets ten seconds; one that sets its own gets that.
func TestSSHArgsConnectTimeout(t *testing.T) {
	for _, c := range []struct {
		timeout int
		want    string
	}{{0, "ConnectTimeout=10"}, {30, "ConnectTimeout=30"}} {
		tr := apply.SSHTransport{User: "ubuntu", Host: "vm.example.org", PublicKeys: []string{keyA}, ConnectTimeout: c.timeout}
		got := tr.SSHArgs([]string{"k1"}, "true")
		want := append(apply.MuxArgs(), "-T", "-p", "22", "-o", "IdentitiesOnly=yes", "-o", c.want, "-i", "k1", "ubuntu@vm.example.org", apply.RemoteCommand("", "true"))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ConnectTimeout %d, ssh args:\n got %q\nwant %q", c.timeout, got, want)
		}
	}
}

func TestSSHArgsDefaultToPort22(t *testing.T) {
	tr := apply.SSHTransport{User: "ubuntu", Host: "vm.example.org", PublicKeys: []string{keyA}}
	got := tr.SSHArgs([]string{"k1"}, "true")[len(apply.MuxArgs())+1:]
	if got[0] != "-p" || got[1] != "22" {
		t.Fatalf("an undeclared port is not 22: %q", got)
	}
	if d := tr.Describe(); d != "ubuntu@vm.example.org" {
		t.Errorf("Describe() = %q, want no port when it is 22", d)
	}
}

// --ssh is an escape hatch: given verbatim, with nothing of the section
// added. Only the connection sharing options and -T go before it.
func TestSSHArgsDestinationOverrideIsVerbatim(t *testing.T) {
	tr := apply.SSHTransport{Destination: "jump-alias", User: "ubuntu", Host: "203.0.113.10", Port: 2222, PublicKeys: []string{keyA}}
	if got, want := tr.SSHArgs(nil, "true"), append(apply.MuxArgs(), "-T", "jump-alias", apply.RemoteCommand("", "true")); !reflect.DeepEqual(got, want) {
		t.Fatalf("override args: got %q, want %q", got, want)
	}
	args, cleanup, err := apply.SSHCommand(tr, "true")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if want := append(append([]string{"ssh"}, apply.MuxArgs()...), "-T", "jump-alias", apply.RemoteCommand("", "true")); !reflect.DeepEqual(args, want) {
		t.Fatalf("override command: got %q, want %q", args, want)
	}
}

// The key files exist, private, for as long as the command needs them, and
// not after.
func TestSSHCommandWritesPrivateKeyFilesAndRemovesThem(t *testing.T) {
	tr := apply.SSHTransport{User: "ubuntu", Host: "203.0.113.10", PublicKeys: []string{keyA, keyB}}
	args, cleanup, err := apply.SSHCommand(tr, "true")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for i, a := range args {
		if a == "-i" {
			files = append(files, args[i+1])
		}
	}
	if len(files) != 2 {
		t.Fatalf("want one -i per key, got %q", args)
	}
	dir := filepath.Dir(files[0])
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("key directory mode %o, want 700", info.Mode().Perm())
	}
	for i, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %o, want 600", f, info.Mode().Perm())
		}
		body, _ := os.ReadFile(f)
		if want := []string{keyA, keyB}[i] + "\n"; string(body) != want {
			t.Errorf("%s holds %q, want %q", f, body, want)
		}
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("key directory survives cleanup: %v", err)
	}
}

func TestSSHCommandRefusesASectionWithoutKeys(t *testing.T) {
	_, _, err := apply.SSHCommand(apply.SSHTransport{User: "ubuntu", Host: "203.0.113.10"}, "true")
	if err == nil || !strings.Contains(err.Error(), "no public key") {
		t.Fatalf("want a refusal naming the missing key, got %v", err)
	}
}

// sshScript is a fake ssh that answers each call in turn, and records what it
// was given.
type sshScript struct {
	answers []sshAnswer
	calls   int
	stdins  []string
	slept   []time.Duration
	log     bytes.Buffer
}

type sshAnswer struct {
	out  string
	exit int
}

func (s *sshScript) install(t *testing.T) {
	t.Helper()
	restore := apply.FakeSSH(func(args []string, stdin string) (string, int) {
		s.stdins = append(s.stdins, stdin)
		a := s.answers[len(s.answers)-1]
		if s.calls < len(s.answers) {
			a = s.answers[s.calls]
		}
		s.calls++
		return a.out, a.exit
	}, []time.Duration{2 * time.Second, 4 * time.Second}, func(d time.Duration) { s.slept = append(s.slept, d) }, &s.log)
	t.Cleanup(restore)
}

const sshTimeout = "ssh: connect to host 203.0.113.10 port 22: Operation timed out\n"

// A connection that never opened is tried again, at most three times in all,
// two and then four seconds apart, with one line naming the destination for
// each retry. Every entry point retries: Run, RunInput, ReadFile, WriteFile.
func TestAConnectionThatNeverOpenedIsRetried(t *testing.T) {
	tr := apply.SSHTransport{User: "ubuntu", Host: "203.0.113.10", PublicKeys: []string{keyA}}
	for name, call := range map[string]func() error{
		"Run":      func() error { _, err := tr.Run("true"); return err },
		"RunInput": func() error { _, err := tr.RunInput("cat", "in"); return err },
		"ReadFile": func() error { _, _, err := tr.ReadFile("/srv/x"); return err },
		"WriteFile": func() error {
			return tr.WriteFile("/srv/x", "content", 0o600)
		},
	} {
		s := &sshScript{answers: []sshAnswer{{sshTimeout, 255}, {"kex_exchange_identification: Connection closed by remote host\n", 255}, {"ok\n", 0}}}
		s.install(t)
		if err := call(); err != nil {
			t.Errorf("%s: a connection that opened on the third attempt still failed: %v", name, err)
		}
		if s.calls != 3 {
			t.Errorf("%s: ssh ran %d times, want 3", name, s.calls)
		}
		if want := []time.Duration{2 * time.Second, 4 * time.Second}; !reflect.DeepEqual(s.slept, want) {
			t.Errorf("%s: waited %v, want %v", name, s.slept, want)
		}
		if lines := strings.Count(s.log.String(), "\n"); lines != 2 || !strings.Contains(s.log.String(), "ubuntu@203.0.113.10") {
			t.Errorf("%s: want one line per retry naming the destination, got %q", name, s.log.String())
		}
		if name == "WriteFile" || name == "RunInput" {
			for _, in := range s.stdins {
				if in == "" {
					t.Errorf("%s: a retry sent no stdin: %q", name, s.stdins)
				}
			}
		}
	}
}

// Three attempts, then the failure is reported as unreachable, which a caller
// can tell from an answer.
func TestAHostThatStaysUnreachableIsReportedAsSuch(t *testing.T) {
	s := &sshScript{answers: []sshAnswer{{sshTimeout, 255}}}
	s.install(t)
	tr := apply.SSHTransport{Destination: "vm"}
	_, err := tr.Run("true")
	if !errors.Is(err, apply.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
	if s.calls != 3 {
		t.Errorf("ssh ran %d times, want 3", s.calls)
	}
}

// A command that ran and failed is the host's answer, and running it again
// could repeat what it did. Exit 255 without a connection error is a remote
// command's own status; a connection error in the output with another status
// is something the command printed; and a session that had opened before it
// dropped may have run the command.
func TestOnlyAConnectionFailureIsRetried(t *testing.T) {
	for _, a := range []sshAnswer{
		{"mv: cannot move\n", 1},
		{"the remote command exited 255 itself\n", 255},
		{"curl: (7) Failed to connect: Connection refused\n", 7},
		{"partial output\nclient_loop: send disconnect: Connection reset by peer\n", 255},
		{"Connection to 203.0.113.10 closed by remote host.\n", 255},
	} {
		s := &sshScript{answers: []sshAnswer{a}}
		s.install(t)
		_, err := apply.SSHTransport{Destination: "vm"}.Run("true")
		if err == nil {
			t.Fatalf("%q: a failure was reported as success", a.out)
		}
		if s.calls != 1 {
			t.Errorf("%q (exit %d) was retried: ssh ran %d times", a.out, a.exit, s.calls)
		}
		if errors.Is(err, apply.ErrUnreachable) {
			t.Errorf("%q (exit %d) was called unreachable", a.out, a.exit)
		}
	}
}

// ssh asks on the terminal itself on a first connection: a host key to
// accept, or a key's passphrase. The progress display is held around every
// attempt until the host has answered once, so the question stays readable,
// and is not held again for that host, so the spinner runs for the rest.
func TestTheFirstConnectionToAHostHoldsTheDisplay(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	held := false
	apply.SetPromptHold(func() func() {
		held = true
		return func() { held = false }
	})
	t.Cleanup(func() { apply.SetPromptHold(nil) })
	var during []bool
	answers := []sshAnswer{{sshTimeout, 255}, {"ok\n", 0}, {"ok\n", 0}, {"ok\n", 0}}
	calls := 0
	restore := apply.FakeSSH(func([]string, string) (string, int) {
		during = append(during, held)
		a := answers[calls]
		calls++
		return a.out, a.exit
	}, []time.Duration{time.Second}, func(time.Duration) {}, io.Discard)
	t.Cleanup(restore)

	a := apply.SSHTransport{Destination: "home-a"}
	if _, err := a.Run("true"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run("true"); err != nil {
		t.Fatal(err)
	}
	if _, err := (apply.SSHTransport{Destination: "home-b"}).Run("true"); err != nil {
		t.Fatal(err)
	}
	if want := []bool{true, true, false, true}; !reflect.DeepEqual(during, want) {
		t.Errorf("held during each ssh: %v, want %v (retried first contact, later command, another host's first)", during, want)
	}
	if held {
		t.Error("the display was left held")
	}
}

// ssh's question about a host key it does not know is followed by a blank
// line, so the next question or the report starts apart from the answer. A
// host whose key is known asks nothing and gets no blank line.
func TestAHostKeyQuestionIsFollowedByABlankLine(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	blanks := 0
	t.Cleanup(apply.FakeHostKeys(func(t apply.SSHTransport) bool { return t.Destination == "known" }, &blanks))
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) { return "ok\n", 0 }, nil, func(time.Duration) {}, io.Discard))

	for _, d := range []string{"new", "new", "known"} {
		if _, err := (apply.SSHTransport{Destination: d}).Run("true"); err != nil {
			t.Fatal(err)
		}
	}
	if blanks != 1 {
		t.Errorf("%d blank lines, want 1: after the new host's first connection only", blanks)
	}
}

// The blank line after a host key question is written while the display is
// still held: resumed first, the spinner would redraw on the answer's row and
// the blank line would leave that frame behind.
func TestTheHostKeyBlankLineComesBeforeTheDisplayResumes(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	held := false
	apply.SetPromptHold(func() func() {
		held = true
		return func() { held = false }
	})
	t.Cleanup(func() { apply.SetPromptHold(nil) })
	var unused int
	t.Cleanup(apply.FakeHostKeys(func(apply.SSHTransport) bool { return false }, &unused))
	var heldAtBlank []bool
	t.Cleanup(apply.OnBlankLine(func() { heldAtBlank = append(heldAtBlank, held) }))
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) { return "ok\n", 0 }, nil, func(time.Duration) {}, io.Discard))
	if _, err := (apply.SSHTransport{Destination: "new"}).Run("true"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(heldAtBlank, []bool{true}) {
		t.Errorf("held at each blank line: %v, want [true]", heldAtBlank)
	}
	if held {
		t.Error("the display was left held")
	}
}

// A failed command's error ends with ssh's last line, not a newline, so a
// caller that adds a sentence after it keeps it on the same line.
func TestARunErrorEndsWithoutANewline(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) { return "Host key verification failed.\n", 1 }, nil, func(time.Duration) {}, io.Discard))
	_, err := (apply.SSHTransport{Destination: "home-a"}).Run("true")
	if err == nil || strings.HasSuffix(err.Error(), "\n") {
		t.Errorf("err = %q", err)
	}
}

func problemOf(t *testing.T, err error) *ui.Problem {
	t.Helper()
	var p *ui.Problem
	if !errors.As(err, &p) {
		t.Fatalf("not a ui.Problem: %v", err)
	}
	return p
}

// A host that stays unreachable is said as such, with ssh's own reason, and
// is still ErrUnreachable to a caller that asks.
func TestUnreachableIsAProblem(t *testing.T) {
	s := &sshScript{answers: []sshAnswer{{sshTimeout, 255}}}
	s.install(t)
	_, err := apply.SSHTransport{User: "ubuntu", Host: "203.0.113.10", PublicKeys: []string{keyA}}.Run("true")
	if !errors.Is(err, apply.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
	p := problemOf(t, err)
	if p.Hint != "ubuntu@203.0.113.10 cannot be reached over ssh" {
		t.Errorf("hint: %q", p.Hint)
	}
	for _, want := range []string{"Operation timed out", "3 attempts", "is up"} {
		if !strings.Contains(p.Explain, want) {
			t.Errorf("the explanation lacks %q: %q", want, p.Explain)
		}
	}
}

// ssh refusing the host's key, not knowing it, or the host refusing the
// operator's key each say what to do, and none is retried or called
// unreachable: the host answered.
func TestSSHAuthenticationFailuresAreProblems(t *testing.T) {
	changed := "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\nHost key verification failed.\n"
	for _, c := range []struct {
		out, hint, explain string
	}{
		{changed, "ubuntu@203.0.113.10 port 2222 offered a host key other than the one known", "ssh-keygen -R '[203.0.113.10]:2222'"},
		{"Host key verification failed.\n", "ubuntu@203.0.113.10 port 2222's host key is not known", "ssh -p 2222 ubuntu@203.0.113.10"},
		{"ubuntu@203.0.113.10: Permission denied (publickey).\n", "ubuntu@203.0.113.10 port 2222 did not accept your ssh key", "authorized_keys"},
	} {
		apply.ForgetContacts()
		s := &sshScript{answers: []sshAnswer{{c.out, 255}}}
		s.install(t)
		_, err := apply.SSHTransport{User: "ubuntu", Host: "203.0.113.10", Port: 2222, PublicKeys: []string{keyA}}.Run("true")
		if s.calls != 1 || errors.Is(err, apply.ErrUnreachable) {
			t.Errorf("%q: retried or called unreachable: %d calls, %v", c.out, s.calls, err)
		}
		p := problemOf(t, err)
		if p.Hint != c.hint || !strings.Contains(p.Explain, c.explain) {
			t.Errorf("%q: got %q\n%q", c.out, p.Hint, p.Explain)
		}
	}
	apply.ForgetContacts()
}

// A site that names no ssh.user is reached as the deployment's own user,
// which exists on a host only once its owner has admitted the deployment.
// A host that refuses that login is said to be one that has not admitted
// it, with the request to send its owner, rather than as a key problem.
func TestARefusedDefaultUserIsNotAdmitted(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	s := &sshScript{answers: []sshAnswer{{"paisans-f2a9@203.0.113.10: Permission denied (publickey).\n", 255}}}
	s.install(t)
	tr := apply.SSHTransport{Site: "home-a", User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DefaultUser: true}
	_, err := tr.Run("true")
	if s.calls != 1 || errors.Is(err, apply.ErrUnreachable) {
		t.Errorf("retried or called unreachable: %d calls, %v", s.calls, err)
	}
	p := problemOf(t, err)
	if p.Hint != "site home-a is not admitted on 203.0.113.10" {
		t.Errorf("hint %q", p.Hint)
	}
	for _, want := range []string{"paisans-f2a9", "paisans host request --site home-a > home-a.admit", "sites.home-a.ssh.user"} {
		if !strings.Contains(p.Explain, want) {
			t.Errorf("explain lacks %q:\n%s", want, p.Explain)
		}
	}
}

// The other refusal ssh gives for a key it will not take reads the same.
func TestARefusedDefaultUserIsNotAdmittedWhateverSshSaid(t *testing.T) {
	for _, out := range []string{
		"Too many authentication failures\n",
		"Received disconnect from 203.0.113.10 port 22:2: Too many authentication failures\n",
	} {
		apply.ForgetContacts()
		s := &sshScript{answers: []sshAnswer{{out, 255}}}
		s.install(t)
		tr := apply.SSHTransport{Site: "home-a", User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DefaultUser: true}
		_, err := tr.Run("true")
		if p := problemOf(t, err); p.Hint != "site home-a is not admitted on 203.0.113.10" {
			t.Errorf("%q: hint %q", out, p.Hint)
		}
	}
	apply.ForgetContacts()
}

// A transport built from a Destination names the host it reaches, never an
// empty one.
func TestANotAdmittedDestinationNamesItsHost(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	s := &sshScript{answers: []sshAnswer{{"Permission denied (publickey).\n", 255}}}
	s.install(t)
	tr := apply.SSHTransport{Site: "home-a", Destination: "paisans-f2a9@203.0.113.10", PublicKeys: []string{keyA}, DefaultUser: true}
	_, err := tr.Run("true")
	p := problemOf(t, err)
	if p.Hint != "site home-a is not admitted on 203.0.113.10" {
		t.Errorf("hint %q", p.Hint)
	}
	for _, bad := range []string{" on .", " on ,", "user, and "} {
		if strings.Contains(p.Explain, bad) {
			t.Errorf("explain has empty text %q:\n%s", bad, p.Explain)
		}
	}
	if !strings.Contains(p.Explain, "paisans-f2a9, and 203.0.113.10 refused it") {
		t.Errorf("explain:\n%s", p.Explain)
	}
}

// A user the site names is the operator's choice, so its refusal is a key
// problem; so is a default user on a transport that names no site, which has
// no request to send.
func TestARefusedDeclaredUserIsAKeyProblem(t *testing.T) {
	for _, tr := range []apply.SSHTransport{
		{Site: "home-a", User: "ubuntu", Host: "203.0.113.10", PublicKeys: []string{keyA}},
		{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DefaultUser: true},
	} {
		apply.ForgetContacts()
		s := &sshScript{answers: []sshAnswer{{"Permission denied (publickey).\n", 255}}}
		s.install(t)
		_, err := tr.Run("true")
		if p := problemOf(t, err); !strings.Contains(p.Hint, "did not accept your ssh key") {
			t.Errorf("%+v: hint %q", tr, p.Hint)
		}
	}
	apply.ForgetContacts()
}

// A sudo probe that never reached ssh (here: no key to offer) is that
// failure, not sudo refusing the user, which it never asked.
func TestASudoProbeThatNeverRanIsNotARefusal(t *testing.T) {
	tr := apply.SSHTransport{User: "ubuntu", Host: "192.0.2.10", Sudo: true, Auth: apply.NewSudoAuth(nil)}
	_, err := tr.Run("true")
	if err == nil || strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "no public key") {
		t.Errorf("err = %v", err)
	}
}

// An ssh failure during the sudo probe (a key the host did not accept) is
// reported as that, not as sudo refusing the user.
func TestAnSSHFailureInTheSudoProbeIsNotARefusal(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	t.Cleanup(apply.FakeSSH(func([]string, string) (string, int) {
		return "ubuntu@192.0.2.10: Permission denied (publickey).\n", 255
	}, nil, func(time.Duration) {}, io.Discard))
	tr := apply.SSHTransport{Destination: "ubuntu@192.0.2.10", Sudo: true, Auth: apply.NewSudoAuth(nil)}
	_, err := tr.Run("true")
	if err == nil || strings.Contains(err.Error(), "sudo") || !strings.Contains(err.Error(), "did not accept your ssh key") {
		t.Errorf("err = %v", err)
	}
}

// writeFileScript runs WriteFile through a stand-in for ssh that executes the
// remote command locally under sh, with the content on its stdin, and returns
// the script the host would have run.
func writeFileScript(t *testing.T, path, content string, mode uint32) (script string, err error) {
	t.Helper()
	restore := apply.FakeSSH(func(args []string, stdin string) (string, int) {
		script = args[len(args)-1]
		cmd := exec.Command("sh", "-c", script)
		cmd.Stdin = strings.NewReader(stdin)
		out, rerr := cmd.CombinedOutput()
		if rerr != nil {
			return string(out), 1
		}
		return string(out), 0
	}, nil, func(time.Duration) {}, io.Discard)
	defer restore()
	err = apply.SSHTransport{Destination: "vm"}.WriteFile(path, content, mode)
	return script, err
}

// The temporary file is made by mktemp in the target's directory: created
// exclusively with mode 600, so a link planted at a guessable name is never
// followed, and a fresh name per write, so two writes of one path do not meet.
func TestWriteFileTempFileComesFromMktempBesideTheTarget(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	script, err := writeFileScript(t, filepath.Join(dir, "app.env"), "A=1\n", 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, ".paisans-tmp") {
		t.Errorf("a fixed temporary name is still used:\n%s", script)
	}
	want := "mktemp '" + dir + "/.app.env.XXXXXX'"
	if !strings.Contains(script, want) {
		t.Errorf("script lacks %q:\n%s", want, script)
	}
	if !strings.Contains(script, "mv -f") {
		t.Errorf("the temporary file is not moved with mv -f:\n%s", script)
	}
}

// Links planted where the old fixed name was, and where a temporary name
// would be guessed, are left alone, and the target still gets the content.
func TestWriteFileDoesNotFollowPlantedLinks(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "app.env")
	for _, name := range []string{"app.env.paisans-tmp", ".app.env.XXXXXX", ".app.env.000000", ".app.env.tmp"} {
		if err := os.Symlink(victim, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeFileScript(t, target, "A=1\n", 0o640); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "precious" {
		t.Errorf("a planted link was written through: %q", got)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o644 {
		t.Errorf("a planted link was chmodded: %v", fi.Mode().Perm())
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "A=1\n" {
		t.Fatalf("target = %q, %v", got, err)
	}
	if li, _ := os.Lstat(target); li.Mode()&os.ModeSymlink != 0 || li.Mode().Perm() != 0o640 {
		t.Errorf("target mode %v", li.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".app.env.*"))
	if len(left) != 3 {
		t.Errorf("a temporary file was left behind: %v", left)
	}
}

// A write that fails part way leaves no temporary file. The mv is made to
// fail by a stand-in earlier on the PATH.
func TestWriteFileRemovesItsTempFileOnFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mv"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	if _, err := writeFileScript(t, filepath.Join(dir, "app.env"), "A=1\n", 0o600); err == nil {
		t.Fatal("a failing mv was reported as success")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*app.env*"))
	if len(left) != 0 {
		t.Errorf("temporary file left behind: %v", left)
	}
}

// A target that is a link or a directory is refused before the rename, so mv
// can never move the temporary file into a linked directory or write through
// a linked file; the temporary file is removed.
func TestWriteFileRefusesALinkOrDirectoryTarget(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	linkedDir := filepath.Join(dir, "linked-dir")
	linkedFile := filepath.Join(dir, "linked-file")
	plainDir := filepath.Join(dir, "plain-dir")
	for _, d := range []string{linkedDir, plainDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(linkedFile, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ link, want string }{
		"symlink to a directory": {linkedDir, "symbolic link"},
		"symlink to a file":      {linkedFile, "symbolic link"},
		"directory":              {"", "directory"},
	} {
		target := filepath.Join(dir, "t-"+strings.ReplaceAll(name, " ", "-"))
		if c.link != "" {
			if err := os.Symlink(c.link, target); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		err := func() error { _, err := writeFileScript(t, target, "A=1\n", 0o600); return err }()
		if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want a refusal naming the path and %q, got %v", name, c.want, err)
		}
	}
	if got, _ := os.ReadFile(linkedFile); string(got) != "precious" {
		t.Errorf("linked file changed: %q", got)
	}
	for _, d := range []string{linkedDir, plainDir} {
		if e, _ := os.ReadDir(d); len(e) != 0 {
			t.Errorf("%s gained %d entries", d, len(e))
		}
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".t-*"))
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

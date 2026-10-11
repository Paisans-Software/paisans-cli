package apply

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// sudoProbe asks whether sudo runs without a password. -n makes sudo fail at
// once instead of waiting for one, and it prints "a password is required"
// when that is the only thing missing (sudo 1.9).
const sudoProbe = "sudo -n true"

// sudoWithPassword is sudo reading its password from the first line of
// stdin. -S reads it from stdin rather than a terminal; -p ” prints no
// prompt, since the output is read by the toolkit; and -k ignores any cached
// credential, so sudo reads that line on every command where it asks at
// all. sudo reads the password one byte at a time up to the newline, so
// whatever follows it on stdin reaches the command unchanged.
//
// It is never run bare: withPassword puts it in front of every command, with
// the stdin that command needs.
const sudoWithPassword = "sudo -k -S -p ''"

// withPassword is command run through sudo with password, and the stdin to
// send it: the password, then a fence line, then the command's own stdin.
//
// Whether sudo reads the password line is up to the host, not to us. -k
// makes sudo ask on every command, but a host can still let a command
// through without asking: a NOPASSWD rule that matches it, or a PAM module
// that authenticates without a password. sudo then reads nothing, and the
// password line is left at the top of the command's stdin, where a command
// that reads stdin takes it as its own input: `cat` prints it into the
// output, WriteFile writes it into a file, and a shell runs it as a command
// and prints it back in its error.
//
// So the command never reads that stdin directly. The shell sudo starts
// first reads lines up to the fence, at most two: the fence alone when sudo
// read the password, the password and then the fence when it did not. Only
// then does the command run, on what follows the fence, so it sees exactly
// its own stdin either way and the password never reaches it. The line read
// is held in a variable that is emptied and printed nowhere, and a stdin
// whose first two lines hold no fence stops the command before it runs.
// The shell's read builtin, like sudo, reads a pipe one byte at a time, so
// it takes nothing past the fence.
//
// The fence is random for each command, so no password and no line of a
// command's input can be mistaken for it.
func withPassword(password, command string, stdin *string) (string, *string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	fence := "paisans-fence-" + hex.EncodeToString(b[:])
	in := password + "\n" + fence + "\n"
	if stdin != nil {
		in += *stdin
	}
	return sudoWithPassword + " sh -c " + shellQuote(fenceGuard(fence)+command), &in, nil
}

// fenceGuard is the shell that reads stdin up to fence before the command
// after it runs: see withPassword.
func fenceGuard(fence string) string {
	return "IFS= read -r paisans_line && { [ \"$paisans_line\" = " + fence + " ] || { IFS= read -r paisans_line && [ \"$paisans_line\" = " + fence + " ]; }; } || { paisans_line=; echo 'paisans: the stdin sudo passed on was not as sent, so the command was not run' >&2; exit 125; }; paisans_line=; "
}

// SudoAuth is how sudo is satisfied on one host. It asks the host once
// whether sudo needs a password and, if it does, asks the operator once, and
// every copy of the transport sharing it reuses the answer. The password is
// kept in memory only, and reaches the host on the stdin of each ssh
// session, never on a command line.
type SudoAuth struct {
	// Prompt asks the operator for the password of the host it names: the
	// site, the destination and the host's own name, as far as each is known
	// (see promptLabel). Nil means there is no terminal to ask on, and a host
	// that wants a password is refused.
	Prompt func(host string) (string, error)

	mu       sync.Mutex
	settled  bool
	password string
	err      error
}

// NewSudoAuth returns a SudoAuth that asks with prompt, or refuses a host
// that wants a password when prompt is nil.
func NewSudoAuth(prompt func(host string) (string, error)) *SudoAuth {
	return &SudoAuth{Prompt: prompt}
}

// SudoPreparer is a transport whose sudo can be settled before its first
// command, so that a run reaching several hosts asks every password at its
// start. A test's fake transport implements it to stand in for a host.
type SudoPreparer interface {
	// SettlesSudo reports whether commands run through sudo with an Auth,
	// so that there is an answer to settle.
	SettlesSudo() bool
	// PrepareSudo settles that answer now: see PrepareSudo.
	PrepareSudo() error
}

// SettlesSudo reports whether t runs its commands through sudo with an Auth
// to settle.
func (t SSHTransport) SettlesSudo() bool { return t.Sudo && t.Auth != nil }

// PrepareSudo asks the host whether sudo needs a password and, if it does,
// asks the operator and checks it, exactly as the first sudo command would.
// The answer is kept in t's Auth, so the commands after it never ask.
func (t SSHTransport) PrepareSudo() error {
	if !t.SettlesSudo() {
		return nil
	}
	_, err := t.Auth.resolve(t)
	return err
}

// SettlesSudo reports whether t is a SudoPreparer with sudo to settle.
func SettlesSudo(t Transport) bool {
	p, ok := t.(SudoPreparer)
	return ok && p.SettlesSudo()
}

// PrepareSudo settles sudo on t ahead of its first command, and does nothing
// for a transport with none to settle. Its errors are the first command's
// would be: ErrSudo for an answer that is kept, a refusal or a password sudo
// did not accept, and ErrUnreachable for a host that never answered, which
// is not kept, so the next command asks again.
func PrepareSudo(t Transport) error {
	if !SettlesSudo(t) {
		return nil
	}
	return t.(SudoPreparer).PrepareSudo()
}

// sudo wraps a command to run under sudo, when the transport says to, and
// returns the stdin to send with it.
func (t SSHTransport) sudo(command string, stdin *string) (string, *string, error) {
	if !t.Sudo {
		return command, stdin, nil
	}
	if t.Auth == nil {
		return "sudo sh -c " + shellQuote(command), stdin, nil
	}
	password, err := t.Auth.resolve(t)
	if err != nil {
		return "", nil, err
	}
	if password == "" {
		return "sudo sh -c " + shellQuote(command), stdin, nil
	}
	return withPassword(password, command, stdin)
}

// resolve returns the password sudo wants on this host, or "" when it wants
// none, asking the host and the operator only the first time.
//
// A refusal is kept, so a wrong password is never sent twice: repeated
// failures can lock the account where the host counts them. A probe that
// never reached the host is not kept, because it is not an answer.
func (a *SudoAuth) resolve(t SSHTransport) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled {
		return a.password, a.err
	}

	out, err := t.run(sudoProbe, nil)
	if err == nil {
		a.settled = true
		return "", nil
	}
	if errors.Is(err, ErrUnreachable) {
		return "", fmt.Errorf("%s: asking whether sudo needs a password: %w\n%s", t.Describe(), err, out)
	}
	var exit interface{ ExitCode() int }
	var sshProblem *ui.Problem
	if !errors.As(err, &exit) || errors.As(err, &sshProblem) {
		// ssh never ran (no key to offer, say), or ran and would not log in
		// (a key or host key it refused), so sudo was never asked.
		return "", err
	}
	if !strings.Contains(out, "a password is required") {
		return "", a.refuse(&ui.Problem{
			Hint:    "sudo on " + t.Describe() + " refused the ssh user",
			Explain: "It refused before asking for any password, so none was asked for. sudo said: " + strings.TrimSpace(out) + "\nGive the ssh user sudo on the host, or name a user that has it in the site's ssh section.",
			Cause:   fmt.Errorf("%s: sudo refused before asking for any password:\n%s", t.Describe(), strings.TrimSpace(out)),
		})
	}
	if a.Prompt == nil {
		return "", a.refuse(&ui.Problem{
			Hint:    "sudo on " + t.Describe() + " needs a password and there is no terminal",
			Explain: "paisans asks for a sudo password only on a terminal, so an unattended run needs sudo without one: a sudoers rule giving the ssh user NOPASSWD. That makes the ssh key alone enough for root on this host, which is a decision about the host rather than about one run.",
		})
	}
	password, err := a.Prompt(t.promptLabel())
	// Registered before anything is sent, so that nothing this run shows
	// can carry it, whether sudo takes it or not (see ui.Secret).
	ui.Secret(password)
	if err != nil {
		return "", a.refuse(&ui.Problem{
			Hint:    "the sudo password for " + t.Describe() + " could not be read",
			Explain: "Reading it from the terminal failed: " + err.Error() + ".",
			Cause:   fmt.Errorf("%s: reading the sudo password: %w", t.Describe(), err),
		})
	}
	if password == "" {
		return "", a.refuse(&ui.Problem{
			Hint:    "no sudo password was given for " + t.Describe(),
			Explain: "Run again and type it at the prompt, or give the ssh user NOPASSWD in sudoers for runs nobody watches.",
		})
	}

	check, in, err := withPassword(password, "true", nil)
	if err != nil {
		return "", err
	}
	out, err = t.run(check, in)
	if errors.Is(err, ErrUnreachable) {
		return "", fmt.Errorf("%s: checking the sudo password: %w\n%s", t.Describe(), err, out)
	}
	if err != nil {
		return "", a.refuse(&ui.Problem{
			Hint:    "sudo on " + t.Describe() + " did not accept the password",
			Explain: "It is not tried again in this run, since repeated failures can lock the account. Run again and type it carefully.",
			Cause:   fmt.Errorf("%s: sudo did not accept the password:\n%s", t.Describe(), strings.TrimSpace(out)),
		})
	}
	a.settled, a.password = true, password
	return password, nil
}

// promptLabel names the host a sudo password is asked for: the site, the
// destination ssh reached and the name the host gives itself, as far as each
// is known. Eg: `site home-a (ubuntu@192.0.2.10, host home-a-01)`, or
// `box, host home-a-01` for a transport that names no site.
//
// The site name comes from paisans.yaml and says which site the operator
// meant; the hostname comes from the host and says which machine answered.
// An address alone says neither, and an operator who does not recognise it
// cannot tell whether they are about to type a password into the wrong box.
func (t SSHTransport) promptLabel() string {
	where := t.Describe()
	if host := t.hostname(); host != "" {
		where += ", host " + host
	}
	if t.Site == "" {
		return where
	}
	return "site " + t.Site + " (" + where + ")"
}

// hostname asks the host its name, or returns "" when it does not give one
// that looks like a hostname. The answer is printed on the operator's
// terminal, so anything beyond letters, digits, dots, hyphens and
// underscores is dropped rather than shown: a host must not be able to write
// terminal escapes into the prompt that asks for its own password.
func (t SSHTransport) hostname() string {
	out, err := t.run("uname -n", nil)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(out)
	if name == "" || len(name) > 253 {
		return ""
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return ""
		}
	}
	return name
}

// ErrSudo marks a host where sudo cannot be used in this run: it refused the
// user, or wants a password nobody can give it here, or did not accept the
// one given. A caller tells it from a host that never answered with
// errors.Is.
var ErrSudo = errors.New("sudo cannot be used on this host")

type sudoRefused struct{ error }

func (e sudoRefused) Unwrap() error        { return e.error }
func (e sudoRefused) Is(target error) bool { return target == ErrSudo }

func (a *SudoAuth) refuse(err error) error {
	a.settled, a.err = true, sudoRefused{err}
	return a.err
}

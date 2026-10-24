package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/create"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans operator register --invite` (docs/specs/2026-10-24-operator-invite.md,
// *operator register --invite: answering it*): the new operator, on their own
// workstation, checks an invite, makes their age key, gives a name and an
// email, and writes a signed request to stdout. It reaches no host.

// The invite, the inviter's key and the age key the request names are
// recorded under .paisans/ for the join that follows the operator's
// acceptance. ageRecipientFile holds the age public key on its first line and where
// the answer found the key on its second.
const (
	inviteFile       = "invite"
	inviterFile      = "inviter.pub"
	ageRecipientFile = "age-recipient"
)

// answerInvite answers the signed invite with a request on stdout, and
// records the invite and inviterKey under dir/.paisans/. Nothing is made, in
// the keystore or in dir, until the invite verifies, the agent holds its
// key, and dir holds no other deployment's files. name and email are asked
// for at a terminal when they are not given.
func answerInvite(r ui.Reporter, dir string, invite []byte, inviterKey, name, email, keyFile string, stdin io.Reader, stdout io.Writer) error {
	step := r.Step("check invite")
	inv, err := operatorinvite.ParseInvite(invite, inviterKey)
	if err != nil {
		step.Fail(err)
		return &ui.Problem{Hint: "the invite cannot be used", Explain: "Nothing was made or written. Check that --inviter-key is the public key of the operator who sent it, as they gave it to you.", Cause: err}
	}
	step.Done("from " + inv.Inviter.Name + " for " + inv.Username + " at " + inv.Domain)

	step = r.Step("check ssh key")
	key, _, err := config.ParseKeyLine(inv.Key)
	if err != nil {
		step.Fail(err)
		return err
	}
	if !agentHolds(agentKeys(), key.Fingerprint) {
		err := &ui.Problem{
			Hint:    "the ssh-agent does not hold the invite's key",
			Explain: fmt.Sprintf("The invite is for %s, and the request is signed with it. Add the private key with ssh-add, and run this again. Nothing was made or written.", key.Fingerprint),
		}
		step.Fail(err)
		return err
	}
	step.Done(key.Fingerprint)

	if err := checkAnswerDir(dir, inv, invite); err != nil {
		return err
	}

	terminal := stdinIsTerminal(stdin)
	if !terminal {
		if err := flagNameAndEmail(name, email); err != nil {
			return err
		}
	}

	configPath := filepath.Join(dir, config.FileName)
	age, err := operatorAgeKey(r, "operator register", inv.Deployment, configPath, keyFile)
	if err != nil {
		return err
	}

	if terminal {
		p := create.NewPrompter(stdin, os.Stderr)
		if name, err = askAnswer(p, "Full name (first and last)", name, create.CheckFullName); err != nil {
			return err
		}
		if email, err = askAnswer(p, "Email", email, create.CheckEmail); err != nil {
			return err
		}
	}

	q := operatorinvite.Request{Version: operatorinvite.Version, Invite: string(invite), Name: name, Email: email, Age: age.Recipient}
	if problems := q.Check(); len(problems) > 0 {
		return &ui.Problem{Hint: "the request would not be valid", Explain: strings.Join(problems, "; ") + ". Nothing was written."}
	}
	body := operatorinvite.EncodeRequest(q)
	step = r.Step("sign request")
	sig, err := operatorinvite.Sign(inv.Key, operatorinvite.RequestNamespace, body)
	if err != nil {
		err = &ui.Problem{Hint: "the request could not be signed", Explain: "The signature is made through your ssh-agent, which must hold " + key.Fingerprint + ".", Cause: err}
		step.Fail(err)
		return err
	}
	step.Done("")

	if err := recordJoin(dir, invite, inviterKey, age); err != nil {
		return err
	}
	if _, err := stdout.Write(operatorinvite.Signed(body, sig)); err != nil {
		return err
	}
	r.Note("send the request to "+inv.Inviter.Name, "Once they have accepted it with "+progname.Cmd()+" operator add --request and applied it with "+progname.Cmd()+" deployment apply --execute, run "+progname.Cmd()+" operator register --execute in "+ui.ShortPath(dir)+".")
	return nil
}

// flagNameAndEmail refuses a name or email that was not given, or that
// create refuses, when there is no terminal to ask at.
func flagNameAndEmail(name, email string) error {
	for _, f := range []struct {
		flag, value string
		check       func(string) error
	}{{"--name", name, create.CheckFullName}, {"--email", email, create.CheckEmail}} {
		if f.value == "" {
			return &ui.Problem{Hint: f.flag + " is needed when stdin is not a terminal", Explain: "Without a terminal to ask at, " + progname.Cmd() + " takes the full name with --name and the email with --email. Nothing was made or written."}
		}
		if err := f.check(f.value); err != nil {
			return &ui.Problem{Hint: f.flag + " is not accepted", Explain: fmt.Sprintf("%q must be %v. Nothing was made or written.", f.value, err)}
		}
	}
	return nil
}

// askAnswer is create's prompt for a value, which keeps one already given
// and accepted.
func askAnswer(p *create.Prompter, q, given string, check func(string) error) (string, error) {
	if given != "" && check(given) == nil {
		return given, nil
	}
	if given != "" {
		p.Say("%s %q: %v", q, given, check(given))
	}
	return p.Text(q, "", check)
}

// checkAnswerDir refuses a directory that belongs to another answer: one
// holding a paisans-stack.yaml, which the join that follows would not
// apply, or an invite recorded for another deployment.
func checkAnswerDir(dir string, inv operatorinvite.Invite, invite []byte) error {
	other := func(what string) error {
		return &ui.Problem{
			Hint:    ui.ShortPath(dir) + " " + what,
			Explain: "Answer the invite in an empty directory, or name one with --config <dir>/" + config.FileName + ". Nothing was made or written.",
		}
	}
	switch cfg, err := config.Load(filepath.Join(dir, config.FileName)); {
	case err == nil && cfg.ID != inv.Deployment:
		return other("holds another deployment's " + config.FileName)
	case err == nil:
		return &ui.Problem{
			Hint:    ui.ShortPath(dir) + " already holds this deployment's " + config.FileName,
			Explain: "An operator who has the files does not join from an invite. Answer it in an empty directory, or name one with --config <dir>/" + config.FileName + ". Nothing was made or written.",
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	recorded, _, ok, err := readJoin(dir)
	if err != nil {
		return err
	}
	if ok {
		i, _, err := operatorinvite.InviteSigner(recorded)
		if err != nil {
			return &ui.Problem{Hint: "the invite recorded in " + ui.ShortPath(dir) + " cannot be read", Explain: "Remove " + ui.ShortPath(filepath.Join(dir, ".paisans", inviteFile)) + " or name another directory with --config. Nothing was made or written.", Cause: err}
		}
		if i.Deployment != inv.Deployment {
			return other("records an invite to another deployment")
		}
		if !bytes.Equal(recorded, invite) {
			return &ui.Problem{
				Hint:    ui.ShortPath(dir) + " records a different invite",
				Explain: "An earlier request answered it, and may still be on its way, so the record is not replaced. Answer this invite in a new directory with --config <dir>/" + config.FileName + ", or remove " + ui.ShortPath(filepath.Join(dir, ".paisans", inviteFile)) + " if the old invite is abandoned. Nothing was made or written.",
			}
		}
	}
	return nil
}

// recordJoin writes the inviter's key, the age key the request names and
// then the invite, verbatim, under dir/.paisans/, each through a temporary
// file and a rename. The invite is written last: readJoin takes it as the
// mark that the record is whole.
func recordJoin(dir string, invite []byte, inviterKey string, age ageKey) error {
	state := filepath.Join(dir, ".paisans")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{inviterFile, []byte(strings.TrimSpace(inviterKey) + "\n")},
		{ageRecipientFile, []byte(age.Recipient + "\n" + age.home() + "\n")},
		{inviteFile, invite},
	} {
		if err := writeFileAtomic(filepath.Join(state, f.name), f.data); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes data to path at 0600 through a synced file renamed
// over it, so a reader never sees half of it.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readJoin is the invite and inviter's key an answer recorded in dir. ok is
// false when the invite, which is written last, is not there.
func readJoin(dir string) (invite []byte, inviterKey string, ok bool, err error) {
	invite, err = os.ReadFile(filepath.Join(dir, ".paisans", inviteFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	key, err := os.ReadFile(filepath.Join(dir, ".paisans", inviterFile))
	if err != nil {
		return nil, "", false, err
	}
	return invite, string(bytes.TrimSpace(key)), true, nil
}

// readJoinAge is the age public key the answer in dir named in its request,
// and where it found the key. Both are empty when none is recorded.
func readJoinAge(dir string) (recipient, source string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, ".paisans", ageRecipientFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	recipient, source, _ = strings.Cut(strings.TrimSpace(string(data)), "\n")
	return strings.TrimSpace(recipient), strings.TrimSpace(source), nil
}

// registerInvite is operator register --invite: it reads the invite file and
// the inviter's key, and answers beside configPath. Progress goes to stderr,
// so stdout holds the request alone.
func registerInvite(inviteArg, inviterArg, name, email, keyFile, configPath string, stdin io.Reader, stdout io.Writer) error {
	if inviterArg == "" {
		return &ui.Problem{Hint: "--invite needs --inviter-key", Explain: "Give the public key of the operator who sent the invite, a .pub file or the key line, as they gave it to you out of band."}
	}
	invite, err := os.ReadFile(inviteArg)
	if err != nil {
		return &ui.Problem{Hint: "--invite names a file that cannot be read", Explain: err.Error() + ".", Cause: err}
	}
	lines, err := keyLines(inviterArg)
	if err != nil {
		return &ui.Problem{Hint: "--inviter-key is neither a public key nor a file that can be read", Explain: "Give the inviting operator's .pub file or key line.", Cause: err}
	}
	if len(lines) != 1 {
		return &ui.Problem{Hint: "--inviter-key must give one key", Explain: fmt.Sprintf("Got %d.", len(lines))}
	}
	r := reporterOverride
	if r == nil {
		r = ui.New(os.Stderr, verboseRun)
	}
	return answerInvite(r, filepath.Dir(configPath), invite, lines[0], name, email, keyFile, stdin, stdout)
}

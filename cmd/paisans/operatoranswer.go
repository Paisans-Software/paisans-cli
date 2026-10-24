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
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is `paisans operator register --invite` (docs/specs/2026-10-24-operator-invite.md,
// *operator register --invite: answering it*): the new operator, on their own
// workstation, checks an invite, makes their age key, gives a name and an
// email, and writes a signed request to stdout. It reaches no host.

// What an answer records lives in the workstation's data directory
// (localstore.Base), keyed by the deployment id, so the join can run in any
// folder: the pending invite, verbatim, and the inviter's key under
// invites/<id>/, and the age key the first answer for the deployment named
// under age/<id>.recipient, its public key on the first line and where the
// answer found it on the second.
const (
	inviteFile  = "invite"
	inviterFile = "inviter.pub"
)

// answerInvite answers the signed invite with a request on stdout, and
// records it and inviterKey as the deployment's pending invite. Nothing is
// made or written until the invite verifies, the agent holds its key, and
// no other invite to the deployment is pending. The first answer for a
// deployment binds the age key it names to it; every later one names the
// bound key. dir is only where the key file check looks; nothing in it is
// read or written. name and email are asked for at a terminal when they are
// not given.
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

	if err := checkPending(inv.Deployment, invite); err != nil {
		return err
	}
	bound, boundSource, isBound, err := readAgeBinding(inv.Deployment)
	if err != nil {
		return err
	}

	terminal := stdinIsTerminal(stdin)
	if !terminal {
		if err := flagNameAndEmail(name, email); err != nil {
			return err
		}
	}

	var age ageKey
	if isBound {
		age, err = boundAgeKey(r, inv.Deployment, bound, boundSource, keyFile)
	} else {
		age, err = operatorAgeKey(r, "operator register", inv.Deployment, filepath.Join(dir, config.FileName), keyFile)
	}
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

	if !isBound {
		if err := recordAgeBinding(inv.Deployment, age); err != nil {
			return err
		}
	}
	if err := recordPending(inv.Deployment, invite, inviterKey); err != nil {
		return err
	}
	if _, err := stdout.Write(operatorinvite.Signed(body, sig)); err != nil {
		return err
	}
	r.Note("send the request to "+inv.Inviter.Name, "Once they have accepted it with "+progname.Cmd()+" operator add --request and applied it with "+progname.Cmd()+" deployment apply --execute, run "+progname.Cmd()+" operator register --execute in an empty directory, where the files go.")
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

// invitesDir is where pending invites are kept, one directory per
// deployment id.
func invitesDir() (string, error) {
	base, err := localstore.Base()
	return filepath.Join(base, "invites"), err
}

// pendingDir is the directory of deployment id's pending invite. The id is
// checked first, since it becomes a path.
func pendingDir(id string) (string, error) {
	if !deployment.ValidID(id) {
		return "", fmt.Errorf("%q is not a deployment id", id)
	}
	dir, err := invitesDir()
	return filepath.Join(dir, id), err
}

// ageBindingPath is the file that binds deployment id to the age key its
// first answer named.
func ageBindingPath(id string) (string, error) {
	if !deployment.ValidID(id) {
		return "", fmt.Errorf("%q is not a deployment id", id)
	}
	base, err := localstore.Base()
	return filepath.Join(base, "age", id+".recipient"), err
}

// checkPending refuses an invite to a deployment whose pending invite is
// another one: the request that answered it may still be on its way, so it
// is not replaced. The same invite answers again.
func checkPending(id string, invite []byte) error {
	pending, _, ok, err := readPending(id)
	if err != nil || !ok || bytes.Equal(pending, invite) {
		return err
	}
	dir, err := pendingDir(id)
	if err != nil {
		return err
	}
	return &ui.Problem{
		Hint:    "another invite to deployment " + id + " is pending",
		Explain: "An earlier request answered it, and may still be on its way, so it is not replaced. Join it with " + progname.Cmd() + " operator register --execute once it is accepted, or remove " + ui.ShortPath(filepath.Join(dir, inviteFile)) + " if it is abandoned, then run this again. Nothing was made or written.",
	}
}

// recordPending writes the inviter's key and then the invite, verbatim, as
// deployment id's pending invite, each through a temporary file and a
// rename. The invite is written last: readPending takes it as the mark that
// the record is whole.
func recordPending(id string, invite []byte, inviterKey string) error {
	dir, err := pendingDir(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, inviterFile), []byte(strings.TrimSpace(inviterKey)+"\n")); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, inviteFile), invite)
}

// recordAgeBinding binds deployment id to age: its public key, and where it
// is kept, said in full.
func recordAgeBinding(id string, age ageKey) error {
	path, err := ageBindingPath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(age.Recipient+"\n"+age.home()+"\n"))
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

// readPending is deployment id's pending invite and the inviter's key. ok is
// false when the invite, which is written last, is not there.
func readPending(id string) (invite []byte, inviterKey string, ok bool, err error) {
	dir, err := pendingDir(id)
	if err != nil {
		return nil, "", false, err
	}
	invite, err = os.ReadFile(filepath.Join(dir, inviteFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	key, err := os.ReadFile(filepath.Join(dir, inviterFile))
	if err != nil {
		return nil, "", false, err
	}
	return invite, string(bytes.TrimSpace(key)), true, nil
}

// pendingInvites is the id of every deployment with a pending invite,
// sorted.
func pendingInvites() ([]string, error) {
	dir, err := invitesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() || !deployment.ValidID(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), inviteFile)); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// removePending removes deployment id's pending invite, once joined.
func removePending(id string) error {
	dir, err := pendingDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// readAgeBinding is the age public key bound to deployment id, and where the
// answer that bound it found the key. ok is false when none is bound.
func readAgeBinding(id string) (recipient, source string, ok bool, err error) {
	path, err := ageBindingPath(id)
	if err != nil {
		return "", "", false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	recipient, source, _ = strings.Cut(strings.TrimSpace(string(data)), "\n")
	recipient = strings.TrimSpace(recipient)
	return recipient, strings.TrimSpace(source), recipient != "", nil
}

// registerInvite is operator register --invite: it reads the invite file and
// the inviter's key, and answers them. Progress goes to stderr,
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

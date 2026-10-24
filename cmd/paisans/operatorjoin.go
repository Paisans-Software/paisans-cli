package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is the join of `paisans operator register`
// (docs/specs/2026-10-24-operator-invite.md, *operator register, again:
// joining*): run where an answer recorded an invite, before the deployment's
// files are there, register recovers them from the invite's root and sites,
// then signs their attestation with the invite's key.

// joinRecorded is the invite an answer recorded in l.Dir, checked again
// against the inviter's key recorded beside it. ok is false when none is
// recorded.
func joinRecorded(l configstore.Local) (_ operatorinvite.Invite, ok bool, err error) {
	signed, inviterKey, ok, err := readJoin(l.Dir)
	if err != nil || !ok {
		return operatorinvite.Invite{}, false, err
	}
	inv, err := operatorinvite.ParseInvite(signed, inviterKey)
	if err != nil {
		return inv, false, &ui.Problem{
			Hint:    "the invite recorded in " + ui.ShortPath(l.Dir) + " cannot be used",
			Explain: "It does not verify with the inviter's key recorded beside it, in " + ui.ShortPath(filepath.Join(l.Dir, ".paisans", inviterFile)) + ". Answer the invite again with " + progname.Cmd() + " operator register --invite <file> --inviter-key <key>. Nothing was written.",
			Cause:   err,
		}
	}
	return inv, true, nil
}

// joinInvite recovers the deployment invite names into l, from the invite's
// root and sites alone, once the newest version lists the invite's key under
// its username, and with execute signs the attestation of the recovered
// version with that key. A root a site serves is never trusted in place of
// the invite's. Whether the recovered secrets open is said, not refused.
func joinInvite(r ui.Reporter, l configstore.Local, invite operatorinvite.Invite, execute bool, ans answerer, stdin io.Reader, stdout io.Writer) error {
	key, _, err := config.ParseKeyLine(invite.Key)
	if err != nil {
		return err
	}
	if !agentHolds(agentKeys(), key.Fingerprint) {
		return &ui.Problem{Hint: "the ssh-agent does not hold the invite's key", Explain: fmt.Sprintf("The invite is for %s, and the sites let it in and the attestation is signed with it. Load it with ssh-add and run this again. Nothing was written.", key.Fingerprint)}
	}
	r.Section("join " + invite.Domain + " as " + invite.Username + " into " + l.Dir)
	var dests []config.Destination
	for _, s := range invite.Sites {
		dests = append(dests, config.Destination{User: s.User, Host: s.Host, Port: s.Port})
	}
	accept := func(head *config.Config) error {
		if len(operatorsHolding(head, []string{invite.Key})[invite.Username]) > 0 {
			return nil
		}
		return notAppliedProblem(fmt.Sprintf("The sites' newest version does not list %s under operators.%s.", key.Fingerprint, invite.Username), nil)
	}
	held := map[string]string{key.Fingerprint: invite.Key}
	err = recoverFiles(r, l, dests, configstore.Anchor{Root: invite.Root}, invite.Deployment, held, execute, ans, stdin, stdout, accept)
	var ns *configstore.PinNotServedError
	switch {
	case errors.As(err, &ns) || errors.Is(err, configstore.ErrNoAnchor):
		err = &ui.Problem{
			Hint:    "the sites serve none of the invite's root's history",
			Explain: fmt.Sprintf("The invite names the root %s, and trust starts from it alone, so a history the sites hold under another root is not trusted. Check with the inviter that the invite is for the deployment the sites hold. Nothing was written.", invite.Root),
			Cause:   err,
		}
	case errors.Is(err, apply.ErrLoginRefused):
		err = notAppliedProblem(fmt.Sprintf("No site the invite names let %s in.", key.Fingerprint), err)
	}
	if err != nil {
		return err
	}
	if !execute {
		r.Step("attest the recovered version, signed with "+key.Fingerprint).End(ui.Pending, "--execute will sign and push it")
		return nil
	}
	path := filepath.Join(l.Dir, config.FileName)
	checkJoinSecrets(r, filepath.Join(l.Dir, "secrets.enc.yaml"), invite.Deployment)
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	return attest(r, l, cfg, invite.Key, invite.Root, execute, ans, stdin, stdout)
}

// notAppliedProblem is a join refused because the inviter's acceptance has
// not reached the sites: the new operator's key reaches them, and the newest
// version lists it, only once an operator applies it.
func notAppliedProblem(what string, cause error) error {
	return &ui.Problem{
		Hint:    "the sites do not know you yet",
		Explain: what + " The sites take you in once the inviter accepts your request and applies it. Ask the inviter whether they have run " + progname.Cmd() + " deployment apply --execute since accepting it, then run this again. Nothing was written.",
		Cause:   cause,
	}
}

// checkJoinSecrets says whether the recovered secrets open with this
// workstation's age key. One that does not is a failed check and not a
// refusal, since the files are the operator's either way.
func checkJoinSecrets(r ui.Reporter, path, id string) {
	s := r.Step("open the secrets with your age key")
	if _, err := config.LoadDeploymentSecrets(path, id); err != nil {
		s.End(ui.Failed, "they do not open")
		hint, _ := ui.Describe(err)
		r.Explain(fmt.Sprintf("%s. The secrets are not encrypted to your age key, so the request was not fully accepted. Any operator runs %s operator add --request with the same request again, then %s deployment apply --execute; then run %s deployment pull here.", hint, progname.Cmd(), progname.Cmd(), progname.Cmd()))
		return
	}
	s.Done("")
}

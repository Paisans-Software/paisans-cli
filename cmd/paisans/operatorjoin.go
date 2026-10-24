package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is the join of `paisans operator register`
// (docs/specs/2026-10-24-operator-invite.md, *operator register, again:
// joining*): run in a folder without the deployment's files, with an
// invite an answer left pending, register recovers them from the invite's
// root and sites, then signs their attestation with the invite's key.

// pendingInvite is deployment id's pending invite, checked again against
// the inviter's key recorded beside it. ok is false when none is pending.
func pendingInvite(id string) (_ operatorinvite.Invite, ok bool, err error) {
	signed, inviterKey, ok, err := readPending(id)
	if err != nil || !ok {
		return operatorinvite.Invite{}, false, err
	}
	inv, err := operatorinvite.ParseInvite(signed, inviterKey)
	if err != nil {
		dir, _ := pendingDir(id)
		return inv, false, &ui.Problem{
			Hint:    "the pending invite to deployment " + id + " cannot be used",
			Explain: "It does not verify with the inviter's key recorded beside it, in " + ui.ShortPath(filepath.Join(dir, inviterFile)) + ". Answer the invite again with " + progname.Cmd() + " operator register --invite <file> --inviter-key <key>. Nothing was written.",
			Cause:   err,
		}
	}
	return inv, true, nil
}

// selectPending is the pending invite a join in a folder without files
// takes: the one for id when id is given, else the only one pending. ok is
// false when none is pending and no id is given. Which of several is never
// guessed.
func selectPending(id string) (_ operatorinvite.Invite, ok bool, err error) {
	ids, err := pendingInvites()
	if err != nil {
		return operatorinvite.Invite{}, false, err
	}
	if id != "" {
		inv, ok, err := pendingInvite(id)
		if err == nil && !ok {
			pending := "None is pending."
			if len(ids) > 0 {
				pending = "Invites to " + andList(ids) + " are pending."
			}
			err = &ui.Problem{Hint: "no invite to deployment " + id + " is pending", Explain: pending + " An invite is pending once " + progname.Cmd() + " operator register --invite has answered it. Nothing was written."}
		}
		return inv, ok, err
	}
	switch len(ids) {
	case 0:
		return operatorinvite.Invite{}, false, nil
	case 1:
		return pendingInvite(ids[0])
	}
	return operatorinvite.Invite{}, false, &ui.Problem{
		Hint:    fmt.Sprintf("invites to %d deployments are pending", len(ids)),
		Explain: "Which one to join here is not guessed: " + strings.Join(ids, ", ") + ". Name it with --id <deployment-id>. Nothing was written.",
	}
}

// joinInvite recovers the deployment invite names into l, from the invite's
// root and sites alone, once the newest version lists the invite's key under
// its username, and with execute signs the attestation of the recovered
// version with that key. A root a site serves is never trusted in place of
// the invite's. Whether the recovered secrets open is said, not refused.
// Once the files are written the invite is no longer pending, since from
// then on register in l.Dir is plain register; the age binding is kept.
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
		return notAppliedProblem(fmt.Sprintf("The sites' newest version does not list %s under operators.%s.", key.Fingerprint, invite.Username), "", nil)
	}
	held := map[string]string{key.Fingerprint: invite.Key}
	err = recoverFiles(r, l, dests, configstore.Anchor{Root: invite.Root}, invite.Deployment, held, execute, ans, stdin, stdout, accept)
	var ns *configstore.PinNotServedError
	switch {
	case errors.As(err, &ns) || errors.Is(err, configstore.ErrNoAnchor):
		err = &ui.Problem{
			Hint:    "the sites serve none of the invite's root's history",
			Explain: fmt.Sprintf("The invite names the root %s, and trust starts from it alone, so a history the sites hold under another root is not trusted. Check with the inviter that the invite is for the deployment the sites hold. No configuration file was written.", invite.Root),
			Cause:   err,
		}
	case errors.Is(err, apply.ErrLoginRefused):
		// A site that refused the login is one the acceptance has not
		// reached, or a host that is not the deployment's site at all.
		var hosts []string
		for _, d := range dests {
			hosts = append(hosts, d.String())
		}
		hostsAre := "If they have, check with them that the invite's hosts, " + andList(hosts) + ", are the deployment's sites: each one not reached is named above."
		err = notAppliedProblem(fmt.Sprintf("No site the invite names let %s in.", key.Fingerprint), hostsAre, err)
	}
	if err != nil {
		return err
	}
	if !execute {
		r.Step("attest the recovered version, signed with "+key.Fingerprint).End(ui.Pending, "--execute will sign and push it")
		return nil
	}
	if err := removePending(invite.Deployment); err != nil {
		return err
	}
	path := filepath.Join(l.Dir, config.FileName)
	checkJoinSecrets(r, l.Dir, invite.Deployment)
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	return attest(r, l, cfg, invite.Key, invite.Root, execute, ans, stdin, stdout)
}

// notAppliedProblem is a join refused because the inviter's acceptance has
// not reached the sites: the new operator's key reaches them, and the newest
// version lists it, only once an operator applies it. more, when set, follows
// the advice to ask the inviter.
func notAppliedProblem(what, more string, cause error) error {
	explain := what + " The sites take you in once the inviter accepts your request and applies it. Ask the inviter whether they have run " + progname.Cmd() + " deployment apply --execute since accepting it, then run this again."
	if more != "" {
		explain += " " + more
	}
	return &ui.Problem{
		Hint:    "the sites do not know you yet",
		Explain: explain + " No configuration file was written.",
		Cause:   cause,
	}
}

// checkJoinSecrets says whether the recovered secrets in dir open with this
// workstation's age key. One that does not is a failed check and not a
// refusal, since the files are the operator's either way. The age key bound
// to the deployment tells whose step is missing: secrets encrypted to it that
// do not open are this workstation's to fix, and secrets not encrypted to it
// are an acceptance that did not re-encrypt them.
func checkJoinSecrets(r ui.Reporter, dir, id string) {
	path := filepath.Join(dir, "secrets.enc.yaml")
	s := r.Step("open the secrets with your age key")
	_, err := config.LoadDeploymentSecrets(path, id)
	if err == nil {
		s.Done("")
		return
	}
	s.End(ui.Failed, "they do not open")
	hint, explain := ui.Describe(err)
	recipient, source, _, rerr := readAgeBinding(id)
	data, derr := os.ReadFile(path)
	if rerr == nil && derr == nil && recipient != "" && slices.Contains(config.FileRecipients(data), recipient) {
		from := ""
		if source != "" {
			from = ", which the answer took from " + strings.TrimPrefix(source, "from ")
		}
		r.Explain(fmt.Sprintf("The secrets are encrypted to your age key %s%s, so your request was accepted; what stops them opening is on this workstation: %s. %s Then run %s secrets list here to check.", recipient, from, hint, explain, progname.Cmd()))
		return
	}
	r.Explain(fmt.Sprintf("%s. The secrets are not encrypted to your age key, so the request was not fully accepted. Any operator runs %s operator add --request with the same request again, then %s deployment apply --execute; then run %s deployment pull here.", hint, progname.Cmd(), progname.Cmd(), progname.Cmd()))
}

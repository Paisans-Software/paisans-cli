package main

import (
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// What follows is the review apply and host prepare make of what
// paisans-stack.yaml grants on a host, before they change it
// (docs/specs/2026-10-11-config-review.md).
//
// Any admin of a deployment can edit paisans-stack.yaml, and whoever runs apply
// runs it with their own access, so an added key or a changed image reaches
// a host through someone else's run. Each host records the grant last
// applied (internal/grant); the review compares the configuration's grant
// with it, shows what is added, changed and removed, and, with --execute,
// asks the operator to type approve for anything added or changed before the
// first change. No flag answers it: a flag that could would be in every
// script within a week, and approving is the control. A run that changes no
// key and no image asks nothing, so routine applies stay scriptable.

// approveWord is what the operator types to approve a review.
const approveWord = "approve"

// grantRunner is how a command reaches a site's grant record: through the
// transport it already reaches the site by. Tests replace it.
var grantRunner = func(site string, t registry.Runner) registry.Runner { return t }

// grantNow is the clock a record's applied_at is written by. Tests replace
// it.
var grantNow = time.Now

// agentKeys is every public key line the operator's ssh agent holds, as
// `ssh-add -L` lists them. No agent, or one holding nothing, is an empty
// list. Tests replace it.
var agentKeys = func() []string {
	out, _ := exec.Command("ssh-add", "-L").Output()
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if _, _, err := config.ParseKeyLine(line); err == nil {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

// agentFingerprints is the fingerprint of every key the operator's ssh agent
// holds. Tests replace it.
var agentFingerprints = func() []string {
	var fps []string
	for _, line := range agentKeys() {
		if k, _, err := config.ParseKeyLine(line); err == nil {
			fps = append(fps, k.Fingerprint)
		}
	}
	return fps
}

// approver is the admin whose key the operator's ssh agent holds, when
// exactly one admin's is, and empty otherwise. It is a label for the next
// reader of the record, not a check: anyone can load any key they hold.
func approver(cfg *config.Config) string {
	held := agentFingerprints()
	var found []string
	for name, admin := range cfg.Admins {
		for _, line := range admin.KeyLines() {
			k, _, err := config.ParseKeyLine(line)
			if err == nil && slices.Contains(held, k.Fingerprint) && !slices.Contains(found, name) {
				found = append(found, name)
			}
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

// siteReview is one site's grant, compared with its record, and how to
// reach the record to write it once the command's steps have succeeded.
type siteReview struct {
	grant  grant.Grant
	scope  grant.Scope
	review grant.Review
	host   registry.Runner
	// record is the grant record the review was compared with, which also
	// holds the configuration version last applied to the host.
	record grant.Record
	// version is the configuration version the command applies, recorded
	// with what was granted; nil records none.
	version *appliedVersion
}

// reviewSite reads site's record of deployment d through host and compares
// g with it.
func reviewSite(d deployment.Deployment, site string, g grant.Grant, scope grant.Scope, host registry.Runner) (*siteReview, error) {
	host = grantRunner(site, host)
	rec, _, _, err := grant.Read(host, d)
	if err != nil {
		return nil, err
	}
	return &siteReview{grant: g, scope: scope, review: grant.Compare(site, g, rec, scope), host: host, record: rec}, nil
}

// entries is n entries to approve, as a review line's result says it.
func entries(n int) string {
	if n == 1 {
		return "1 entry to approve"
	}
	return fmt.Sprintf("%d entries to approve", n)
}

// showReview reports one site's review as a line, marked pending when
// anything needs approval, with each change below it at every verbosity:
// keys first, then images, then the web server.
func showReview(r ui.Reporter, title string, rev grant.Review, execute bool) {
	endReview(r, r.Step(title), rev, execute)
}

// endReview ends line with what a review comes to, and lists its changes
// under it. A first review says so, and why every entry is new: a host with
// no record of what was granted is reviewed once in full, which is the
// record being made, and an operator meeting a long list should know that
// it is that and not a change of everything.
func endReview(r ui.Reporter, line ui.Step, rev grant.Review, execute bool) {
	n := rev.Pending()
	what := entries(n)
	if rev.First && n > 0 {
		// Every entry of a first review is to approve, which the line
		// below it says, so the result stays one short line.
		what = "first review, " + strings.TrimSuffix(entries(n), " to approve")
	}
	switch {
	case n > 0 && execute:
		line.End(ui.Pending, what)
	case n > 0:
		line.End(ui.Pending, what+"; --execute asks for "+approveWord)
	case len(rev.Changes) > 0:
		line.End(ui.OK, "only removals, nothing to approve")
	default:
		line.End(ui.OK, "")
	}
	if rev.First && n > 0 {
		r.Explain(rev.Site + " has no record of what this deployment granted there yet, so every entry below is new. Approving it makes the record; later runs show only what changes from it.")
	}
	for _, c := range rev.Changes {
		r.Explain(c.Line())
	}
}

// approveReviews asks, once, for approval of every review that needs it.
// It asks nothing when none does. Without a terminal it refuses, before any
// host changes, since the only answer is one typed by the operator.
func approveReviews(r ui.Reporter, stdin io.Reader, stdout io.Writer, reviews []grant.Review, command string) error {
	pending := 0
	for _, rev := range reviews {
		pending += rev.Pending()
	}
	if pending == 0 {
		return nil
	}
	if !stdinIsTerminal(stdin) {
		return &ui.Problem{
			Hint:    "what " + config.FileName + " grants changed, and stdin is not a terminal",
			Explain: fmt.Sprintf("The review above adds or changes what may log in or run on a host (%s), which only an operator at a terminal approves, and no flag answers it. Run %s from an interactive shell. Nothing was changed.", grant.Summary(reviews), command),
		}
	}
	resume := ui.Hold(r)
	defer resume()
	what := fmt.Sprintf("The review above is what "+config.FileName+" grants that this host, or these hosts, did not have: %s. Check each key with its admin and each image with whoever chose it before you approve.", grant.Summary(reviews))
	if err := confirmWord(stdin, stdout, approveWord, what); err != nil {
		return &ui.Problem{Hint: "not approved", Explain: sentence(err.Error())}
	}
	return nil
}

// recordGrant writes the parts of a site's grant its command covers, once
// the command's steps for the site have all succeeded, and the configuration
// version it applied. A record already holding them is left as it is, and
// says nothing.
func recordGrant(r ui.Reporter, s *siteReview, d deployment.Deployment, approvedBy string) error {
	if s == nil {
		return nil
	}
	wrote, err := grant.Write(s.host, d, func(rec grant.Record) grant.Record {
		now := grantNow()
		out := grant.Update(rec, s.grant, s.scope, now, approvedBy)
		if s.version != nil {
			out = out.WithConfig(s.version.sha, s.version.revision, now)
		}
		return out
	})
	if err != nil {
		r.Step("record what was granted").Fail(err)
		return &ui.Problem{
			Hint:    "could not record what was granted",
			Explain: errLine(err) + ". What was applied is in place; the next run reviews the same entries again, and records them once it can.",
			Cause:   err,
		}
	}
	if wrote {
		r.Step("record what was granted").Done("")
	}
	return nil
}

// runReview is apply with no --site's review, while that run is in
// progress. The run shows one review for every site and asks once, before
// its first step; each step it runs then checks that what it is about to
// apply is what was approved, rather than asking again.
type runReview struct {
	// approved is each site's review as the operator approved it, empty in
	// a dry run, which asks nothing.
	approved map[string]grant.Review
	// approvedBy is who approved it, as approver says.
	approvedBy string
	// version is the configuration version the run published, which each
	// apply step records with what it applied; nil in a dry run.
	version *appliedVersion
}

// reviewedByRun is set while apply with no --site runs.
var reviewedByRun *runReview

// stepReview is the review of one command run for site: host prepare's or
// apply --site's. Typed by hand, the command shows it and, with execute,
// asks for approval when anything needs it. Run as a step of apply with no
// --site, which showed and approved every site's review already, a dry run
// reviews nothing, and an execution refuses anything the run's approval
// does not hold exactly, since that changed after the operator approved.
// It returns what to record once the command's steps have succeeded, and
// who approved it.
func stepReview(r ui.Reporter, stdin io.Reader, stdout io.Writer, cfg *config.Config, site string, g grant.Grant, scope grant.Scope, host registry.Runner, execute bool, command string) (*siteReview, string, error) {
	run := reviewedByRun
	if run != nil && !execute {
		return nil, "", nil
	}
	s, err := ui.Get(r, "read what was granted", func() (*siteReview, error) {
		return reviewSite(cfg.Deployment(), site, g, scope, host)
	})
	if err != nil {
		return nil, "", err
	}
	if run != nil {
		if late := s.review.Unapproved(run.approved[site]); len(late) > 0 {
			var lines []string
			for _, c := range late {
				lines = append(lines, c.Line())
			}
			return nil, "", &ui.Problem{
				Hint:    "what " + config.FileName + " grants on " + site + " changed after it was approved",
				Explain: strings.Join(lines, "\n") + "\nThese were not in the review the run asked you to approve. Nothing was changed on " + site + " by this step. Run paisans apply --execute again to review them.",
			}
		}
		return s, run.approvedBy, nil
	}
	if !execute {
		return s, "", nil
	}
	showReview(r, "review what "+config.FileName+" grants", s.review, true)
	if err := approveReviews(r, stdin, stdout, []grant.Review{s.review}, command); err != nil {
		return nil, "", err
	}
	by := ""
	if s.review.Pending() > 0 {
		by = approver(cfg)
	}
	return s, by, nil
}

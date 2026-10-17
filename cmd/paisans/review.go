package main

import (
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is the review apply and host prepare make of what
// paisans-stack.yaml grants on a host, before they change it
// (docs/specs/2026-10-11-config-review.md).
//
// Any operator of a deployment can edit paisans-stack.yaml, and whoever runs apply
// runs it with their own access, so an added key or a changed image reaches
// a host through someone else's run. Each host records the grant last
// applied (internal/grant); the review compares the configuration's grant
// with it, shows what is added, changed and removed, and, with --execute,
// asks the operator to type approve for anything added or changed before the
// first change. Approving is the control: --yes answers it only where the
// environment sets PAISANS_ALLOW_YES=1, and prints each entry it approves,
// so a command line alone never approves a key or an image. A run that
// changes no key and no image asks nothing, so routine applies stay
// scriptable. A
// foundation image is shown and not asked about, since the team reviews
// foundation.yaml in the core catalog's pull requests.

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

// approver is the operator whose key the local ssh agent holds, when
// exactly one operator's is, and empty otherwise. It is a label for the next
// reader of the record, not a check: anyone can load any key they hold.
func approver(cfg *config.Config) string {
	held := agentFingerprints()
	var found []string
	for name, operator := range cfg.Operators {
		for _, line := range operator.KeyLines() {
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
// keys first, then images, then blueprints.
func showReview(r ui.Reporter, title string, rev grant.Review, execute bool) {
	endReview(r, r.Step(title), rev, execute)
}

// endReview ends line with what a review comes to, and lists its changes
// under it. A first review says so: a host with no record of what was
// granted is reviewed once in full, which is the record being made, and an
// operator meeting a long list should know that it is that and not a change
// of everything. What --execute asks is said once, by the run's last line.
func endReview(r ui.Reporter, line ui.Step, rev grant.Review, execute bool) {
	n := rev.Pending()
	what := entries(n)
	if rev.First && n > 0 {
		what = fmt.Sprintf("%d new entries (first review)", n)
		if n == 1 {
			what = "1 new entry (first review)"
		}
	}
	// Entries approved by an earlier run, which stopped before applying
	// them, are counted and not listed or asked about again.
	earlier := ""
	if k := len(rev.Approved); k > 0 {
		earlier = fmt.Sprintf("%d approved earlier, not applied yet", k)
	}
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, "; ")
	}
	// The changes are listed in groups, each under its heading: foundation
	// images first, which are not approved here since the team reviews
	// foundation.yaml in the core catalog's pull requests, then removals,
	// which ask nothing either, and last what needs approval, under its
	// count, so the count heads exactly what it counts, above the prompt.
	var approve, removed, foundation []grant.Change
	for _, c := range rev.Changes {
		switch {
		case c.Foundation:
			foundation = append(foundation, c)
		case c.NeedsApproval():
			approve = append(approve, c)
		default:
			removed = append(removed, c)
		}
	}
	switch {
	case n > 0:
		line.End(ui.Pending, join(what, earlier))
	case len(removed) > 0 && len(foundation) > 0:
		line.End(ui.OK, join("only removals and foundation images, nothing to approve", earlier))
	case len(foundation) > 0:
		line.End(ui.OK, join("only foundation images, nothing to approve", earlier))
	case len(removed) > 0:
		line.End(ui.OK, join("only removals, nothing to approve", earlier))
	case earlier != "":
		line.End(ui.OK, earlier+", nothing new to approve")
	default:
		line.End(ui.OK, "")
	}
	group := func(heading string, changes []grant.Change) {
		if len(changes) == 0 {
			return
		}
		r.Explain(heading)
		for _, c := range changes {
			r.Explain(c.Line())
		}
	}
	group(foundationHeading, foundation)
	group(removedHeading, removed)
	group(entries(n)+":", approve)
}

// foundationHeading heads the changes to foundation images in a review, and
// removedHeading its removals.
const (
	foundationHeading = "foundation images (required):"
	removedHeading    = "removed, nothing to approve:"
)

// approveReviews asks, once, for approval of every review that needs it.
// It asks nothing when none does. Without a terminal it refuses, before any
// host changes, since the only answer is one typed by the operator, or
// --yes, through a, which approves each entry and prints a line naming it.
func approveReviews(r ui.Reporter, a answerer, stdin io.Reader, stdout io.Writer, reviews []grant.Review, command string) error {
	pending := 0
	for _, rev := range reviews {
		pending += rev.Pending()
	}
	if pending == 0 {
		return nil
	}
	if !a.canAnswer(stdin) {
		return &ui.Problem{
			Hint:    "what " + config.FileName + " grants changed, and stdin is not a terminal",
			Explain: fmt.Sprintf("The review above adds or changes what may log in or run on a host (%s), which only an operator at a terminal approves. Run %s from an interactive shell. Nothing was changed.", grant.Summary(reviews), command),
		}
	}
	resume := ui.Hold(r)
	defer resume()
	if a.yes {
		for _, rev := range reviews {
			for _, c := range rev.Changes {
				if c.NeedsApproval() {
					a.approved(stdout, rev.Site+": "+c.Line())
				}
			}
		}
		return nil
	}
	what := grant.Summary(reviews) + ". Check each key with its owner and each image with whoever chose it."
	if err := confirmWord(a, stdin, stdout, approveWord, what); err != nil {
		return &ui.Problem{Hint: "not approved", Explain: sentence(err.Error())}
	}
	return nil
}

// recordApproval writes on a site what the operator just approved there,
// before the command's first change, so a run that stops part way does not
// ask for the same entries again. A site with nothing approved is not
// written.
func recordApproval(r ui.Reporter, s *siteReview, d deployment.Deployment, approvedBy string) error {
	if s == nil || s.review.Pending() == 0 {
		return nil
	}
	_, err := grant.Write(s.host, d, func(rec grant.Record) grant.Record {
		return rec.Approve(s.review.Changes, grantNow(), approvedBy)
	})
	if err != nil {
		r.Step("record what was approved").Fail(err)
		return &ui.Problem{
			Hint:    "could not record what was approved on " + s.review.Site,
			Explain: errLine(err) + ". Nothing was changed on " + s.review.Site + ". Make the record writable and run again; the review asks for the same entries again.",
			Cause:   err,
		}
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
			Explain: errLine(err) + ". What was applied is in place; the next run applies it again without asking, since it was approved, and records it once it can.",
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
// asks for approval when anything needs it, which a answers. Run as a step
// of apply with no
// --site, which showed and approved every site's review already, a dry run
// reviews nothing, and an execution refuses anything the run's approval
// does not hold exactly, since that changed after the operator approved.
// It returns what to record once the command's steps have succeeded, and
// who approved it.
func stepReview(r ui.Reporter, a answerer, stdin io.Reader, stdout io.Writer, cfg *config.Config, site string, g grant.Grant, scope grant.Scope, host registry.Runner, execute bool, command string) (*siteReview, string, error) {
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
	if err := approveReviews(r, a, stdin, stdout, []grant.Review{s.review}, command); err != nil {
		return nil, "", err
	}
	by := ""
	if s.review.Pending() > 0 {
		by = approver(cfg)
	}
	if err := recordApproval(r, s, cfg.Deployment(), by); err != nil {
		return nil, "", err
	}
	return s, by, nil
}

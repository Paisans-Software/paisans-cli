package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// printLeftovers is apply's "left over" section: this deployment's stacks
// still on the host that the site no longer renders, its files that no apply
// renders any more, and its containers for a service a stack's rendered
// compose file no longer declares, which compose leaves running. A whole plan's left over files are what it is about
// to record; a scoped or partial plan marks nothing, so the manifest's marks
// from the last whole apply are shown. Each is a warning naming `paisans app
// remove` for the app it belongs to, which is what takes it off, so the
// remedy shows at any verbosity. It reports nothing when nothing is left
// over.
func printLeftovers(r ui.Reporter, cfg *config.Config, site string, inv *hostcheck.Inventory, plan *apply.Plan) error {
	if inv == nil {
		return nil
	}
	// A whole plan is read against this run's view, the sites pending now,
	// since its files and marks are what it is about to record with that
	// list in the manifest: read against the old manifest's view, a site
	// pending then and ready now would make what this plan renders for it
	// read as left over. A scoped or partial plan records no marks, so the
	// manifest's marks are read against the view they were made in.
	manifest, pending := inv.ManifestFiles, inv.ManifestPending
	if plan.Whole() {
		manifest, pending = plan.Leftovers, plan.Pending
	}
	report, err := classifyIn(cfg, site, pending, inv, manifest)
	if err != nil {
		return err
	}
	declared, err := plan.DeclaredServices()
	if err != nil {
		return err
	}
	report.Services = ownership.UndeclaredServices(cfg.Deployment(), inv, declared)
	lines := report.LeftoverLines()
	if len(lines) == 0 {
		return nil
	}
	r.Detail("left over: this deployment's, no longer rendered for %s. apply leaves each one in place.", site)
	// LeftoverLines describes the stacks, then the files, then the services,
	// in that order.
	hint := func(what, app string) string {
		if app == "" {
			return "left over: " + what
		}
		return fmt.Sprintf("left over: %s (`%s app remove %s`)", what, progname.Cmd(), app)
	}
	for i, s := range report.Stacks {
		app := s.Name
		if app == "infra" {
			app = ""
		}
		r.Warn(hint("stack "+s.Name, app), lines[i])
	}
	for i, f := range report.Files {
		r.Warn(hint("file /"+f.Path, ownership.FileApp(f.Path)), lines[len(report.Stacks)+i])
	}
	for i, s := range report.Services {
		r.Warn(fmt.Sprintf("left over: service %s of stack %s (`docker rm -f %s`)", s.Name, s.Stack, strings.Join(s.Containers, " ")), lines[len(report.Stacks)+len(report.Files)+i])
	}
	if len(report.Services) > 0 {
		r.Detail("%s", "A left over service's containers keep running until removed, as the deployment's user, once nothing relies on them. Its data under the stack's directory stays.")
	}
	for _, line := range removeAdvice(report) {
		r.Detail("%s", line)
	}
	return nil
}

// removeAdvice names the command that takes each app's leftovers off, once
// a whole apply has run on every site the app was on.
func removeAdvice(report ownership.Report) []string {
	var out []string
	for _, app := range report.Apps() {
		out = append(out, fmt.Sprintf("`%s app remove %s` takes %s's off every site, once a whole apply has run on each; it keeps its data unless given --delete-data", progname.Cmd(), app, app))
	}
	return out
}

// classifyLeftovers classifies a site against the view its manifest recorded:
// the declaration less the sites pending when its files were rendered. A
// manifest that names the site itself among them is read without that name,
// since the site being classified is, by being classified, not pending.
func classifyLeftovers(cfg *config.Config, site string, inv *hostcheck.Inventory, manifest []render.ManifestFile) (ownership.Report, error) {
	return classifyIn(cfg, site, inv.ManifestPending, inv, manifest)
}

// classifyIn classifies a site against the declaration less pending, less
// the site itself.
func classifyIn(cfg *config.Config, site string, pending []string, inv *hostcheck.Inventory, manifest []render.ManifestFile) (ownership.Report, error) {
	pending = slices.DeleteFunc(slices.Clone(pending), func(s string) bool { return s == site })
	return ownership.Classify(cfg.WithoutJoining(pending...), site, inv, manifest)
}

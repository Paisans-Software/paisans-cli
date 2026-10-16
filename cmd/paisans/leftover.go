package main

import (
	"fmt"
	"slices"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// printLeftovers is apply's "left over" section: this deployment's stacks
// still on the host that the site no longer renders, and its files that no
// apply renders any more. A whole plan's left over files are what it is about
// to record; a scoped or partial plan marks nothing, so the manifest's marks
// from the last whole apply are shown. Each is a warning naming `paisans app
// remove` for the app it belongs to, which is what takes it off, so the
// remedy shows at any verbosity. It reports nothing when nothing is left
// over.
func printLeftovers(r ui.Reporter, cfg *config.Config, site string, inv *hostcheck.Inventory, plan *apply.Plan) error {
	if inv == nil {
		return nil
	}
	manifest := inv.ManifestFiles
	if plan.Whole() {
		manifest = plan.Leftovers
	}
	report, err := classifyLeftovers(cfg, site, inv, manifest)
	if err != nil {
		return err
	}
	lines := report.LeftoverLines()
	if len(lines) == 0 {
		return nil
	}
	r.Detail("left over: this deployment's, no longer rendered for %s. apply leaves each one in place.", site)
	// LeftoverLines describes the stacks, then the files, in that order.
	hint := func(what, app string) string {
		if app == "" {
			return "left over: " + what
		}
		return fmt.Sprintf("left over: %s (`paisans app remove %s`)", what, app)
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
		out = append(out, fmt.Sprintf("`paisans app remove %s` takes %s's off every site, once a whole apply has run on each; it keeps its data unless given --delete-data", app, app))
	}
	return out
}

// classifyLeftovers classifies a site against the view its manifest recorded:
// the declaration less the sites pending when its files were rendered. A
// manifest that names the site itself among them is read without that name,
// since the site being classified is, by being classified, not pending.
func classifyLeftovers(cfg *config.Config, site string, inv *hostcheck.Inventory, manifest []render.ManifestFile) (ownership.Report, error) {
	pending := slices.DeleteFunc(slices.Clone(inv.ManifestPending), func(s string) bool { return s == site })
	return ownership.Classify(cfg.WithoutJoining(pending...), site, inv, manifest)
}

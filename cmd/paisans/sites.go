package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/doctor"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// runSites is the classification of the run in progress. apply without
// --site sets it once, at its start, and every step of the run reads it: one
// view of who is in the mesh for the whole run, so no two sites' files
// disagree, and no step reads every site again. A command run alone leaves
// it unset and classifies on its own.
var runSites *topology.Classification

// classifyTransports reaches every declared site to classify it. Tests
// replace it.
var classifyTransports = func(cfg *config.Config) map[string]apply.Transport {
	out := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		out[name] = siteTransport(name, cfg.Sites[name], "")
	}
	return out
}

// classifySites is the run's classification: the one apply without --site
// read, or, for a command run alone, one read now. A refusal from topology
// is an error, never a site left out: the run stops on it.
func classifySites(r ui.Reporter, cfg *config.Config) (*topology.Classification, error) {
	if runSites != nil {
		return runSites, nil
	}
	r.Section("sites")
	c, err := ui.Get(r, "classify the sites", func() (*topology.Classification, error) {
		return topology.Classify(cfg, topology.Gather(cfg, classifyTransports(cfg)))
	})
	if err != nil {
		return nil, &ui.Problem{Hint: "the sites could not be classified", Explain: err.Error(), Cause: err}
	}
	return c, nil
}

// effectiveConfig is what a step renders and plans from: the effective
// topology of the run's classification, after its refusals, site being the
// one the step names, if any. cfg is the declaration, validated; the
// effective topology is derived from it and never validated as one.
func effectiveConfig(r ui.Reporter, cfg *config.Config, site string) (*config.Config, *topology.Classification, error) {
	c, err := classifySites(r, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := c.Refuse(cfg, site); err != nil {
		return nil, nil, &ui.Problem{Hint: "the run cannot go on with the sites it has", Explain: err.Error(), Cause: err}
	}
	return c.Effective(cfg), c, nil
}

// readySites is the sites of cfg, the effective topology, that the run
// steps on: every one but the down, whose own steps are skipped.
func readySites(cfg *config.Config, c *topology.Classification) []string {
	var out []string
	for _, name := range cfg.SiteNames() {
		if c.Ready(name) {
			out = append(out, name)
		}
	}
	return out
}

// storageWaits is why storage is not laid out this run, or empty when it
// is: Garage held back for want of nodes, or a Garage node down, which
// storage add and storage init would have to reach. cfg is the effective
// topology.
func storageWaits(cfg *config.Config, c *topology.Classification) string {
	if c.StorageHeld() {
		return strings.Join(c.Warnings, "; ")
	}
	var down []string
	for _, s := range cfg.Storage.Garage.Sites {
		if !c.Ready(s) {
			down = append(down, s)
		}
	}
	if len(down) > 0 {
		verb := " is"
		if len(down) > 1 {
			verb = " are"
		}
		return "storage waits: Garage is not laid out while its " + strings.Join(down, ", ") + verb + " down"
	}
	return ""
}

// runOn is the steps of a run that it takes on the sites it has: none of a
// site that is not ready, and no storage step while storage waits.
func runOn(cfg *config.Config, c *topology.Classification, steps []convergeStep) []convergeStep {
	held := storageWaits(cfg, c) != ""
	return slices.DeleteFunc(steps, func(s convergeStep) bool {
		if held && s.Stage == stageStorage {
			return true
		}
		site := stepSite(s)
		return site != "" && !c.Ready(site)
	})
}

// showPending opens the plan: a line per pending or down site with its
// reason, a pending one's request line, and what the deployment is without,
// in doctor's words. cfg is the declaration.
func showPending(r ui.Reporter, cfg *config.Config, c *topology.Classification) {
	if !c.Degraded() && len(c.Warnings) == 0 {
		return
	}
	r.Section("pending")
	for _, name := range c.Pending() {
		s := r.Step(name)
		s.Detail("%s", c.Sites[name].Why)
		s.Detail("paisans host request --site %s > %s.admit", name, name)
		if lost := doctor.Losses(cfg, name); lost != "" {
			s.Detail("While it is pending the deployment is without %s.", lost)
		}
		s.End(ui.Pending, "pending")
	}
	for _, name := range c.Down() {
		s := r.Step(name)
		s.Detail("%s", topology.DownLine(name))
		if lost := doctor.Losses(cfg, name); lost != "" {
			s.Detail("While it is down the deployment is without %s.", lost)
		}
		s.End(ui.Warned, "down")
	}
	for _, w := range c.Warnings {
		r.Warn(w, "")
	}
}

// degradedResult is how a degraded run ends.
func degradedResult(c *topology.Classification) string {
	names := append(c.Pending(), c.Down()...)
	noun := "sites"
	if len(names) == 1 {
		noun = "site"
	}
	return fmt.Sprintf("Applied, degraded: %d %s pending or down (%s)", len(names), noun, strings.Join(names, ", "))
}

// requireAll is --require-all: a degraded run fails, for automation that
// must not pass on one.
func requireAll(c *topology.Classification, required bool) error {
	if !required || !c.Degraded() {
		return nil
	}
	return &ui.Problem{Hint: "the run is degraded and --require-all was given", Explain: degradedResult(c) + "."}
}

// pendingOptions is what an apply of one site takes from the run's
// classification: the sites pending, recorded in its manifest.
func pendingOptions(c *topology.Classification) []apply.Option {
	return []apply.Option{apply.Pending(c.Pending()...)}
}

package main

import (
	"slices"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// A site the run finds nothing to do on is left alone. Every site's apply
// otherwise runs at least twice, among the sites outside etcd and again in
// pass two, and an apply with nothing to change still reads its host,
// resolves its images, asks Pocket ID about its clients and waits on its
// gates: tens of seconds a site, for a configuration change that touched
// one of them.
//
// Whether a site has anything to do is decided by its pull, which plans it
// as its apply would, against the host as it is: a file edited there, a
// container that stopped, an action a stopped apply still owes, an image to
// sweep or a grant to run are all work, so a host that drifted is still
// applied. A client its apps need made or changed at Pocket ID is work too,
// and so is a review entry the operator approved. A site with none of these
// has no pull, and no install step: its install line records the version
// the run published, as its apply would, and says it is unchanged.
//
// Only a run whose steps are all ordinary applies leaves a site alone. One
// that founds or joins an etcd member, or moves the gateway, orders its
// applies so that each finds the others where it needs them, and runs every
// one as planned.

// leavesIdle reports whether a run of steps leaves alone a site with nothing
// to do: none of them founds or joins an etcd member or moves the gateway.
func leavesIdle(steps []convergeStep) bool {
	return !slices.ContainsFunc(steps, func(s convergeStep) bool {
		switch s.Stage {
		case stageInit, stageFounding, stageJoining, stageGateway:
			return true
		}
		return slices.Contains(s.Args, "--hold-apps")
	})
}

// idlePlan reports whether plan, a site's whole plan, gives its apply
// nothing to do.
func idlePlan(plan *apply.Plan) bool {
	return len(plan.Writes()) == 0 &&
		len(plan.Conflicts()) == 0 &&
		len(plan.Actions) == 0 &&
		plan.WireGuard == apply.WireGuardNone &&
		len(plan.SourceMark.Steps()) == 0 &&
		len(plan.Sweep) == 0 &&
		len(plan.Leftovers) == 0 &&
		plan.Disk == nil &&
		plan.Bootstrap == nil
}

// convergeIdleClients reports whether site's apply has nothing to do at
// Pocket ID: no client to make or change, no signup group to record, no app
// it would hold back, and nothing to warn about, such as a Pocket ID that
// could not be asked, which its apply would ask again once Pocket ID is up.
// It only reads. Tests replace it.
var convergeIdleClients = func(cfg *config.Config, secrets *config.Secrets, secretsPath, site string) bool {
	warned := 0
	c, err := newClientStep(warnCounter{ui.Discard, &warned}, cfg, site, "", secretsPath, secrets, nil)
	if err != nil {
		return false
	}
	if c == nil {
		return true
	}
	if err := c.ensure(false, c.pocketIDHere()); err != nil {
		return false
	}
	return c.steps == 0 && len(c.held) == 0 && c.groupsErr == nil && warned == 0
}

// warnCounter discards a report but counts its warnings.
type warnCounter struct {
	ui.Reporter
	n *int
}

func (w warnCounter) Warn(string, string) { *w.n++ }

// idleSite reports whether site, whose pull planned plan, is left alone, and
// records it if so.
func (p *puller) idleSite(site string, plan *apply.Plan) bool {
	if !p.leaves || !idlePlan(plan) {
		return false
	}
	if run := reviewedByRun; run != nil && run.approved[site].Pending() > 0 {
		return false
	}
	if !convergeIdleClients(p.cfg, p.secrets, convergeSecretsPath(p.o.Config, p.o.Secrets), site) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idle == nil {
		p.idle = map[string]bool{}
	}
	p.idle[site] = true
	return true
}

// idleSites is the sites a run leaves alone, as its install lines take
// them.
type idleSites struct {
	// sites is each one, in the configuration's order.
	sites []string
	// sha is the version the run had published when it decided: a step
	// that publishes another, recording what it made at Pocket ID, may
	// have changed what these sites render, so each is then applied after
	// all.
	sha string
	// record records on a site what its apply would once it succeeded.
	record func(r ui.Reporter, site string) error
}

// moved reports whether the run published another version since it
// decided.
func (s idleSites) moved() bool {
	v := reviewedByRun
	return v != nil && v.version != nil && v.version.sha != s.sha
}

// dropIdle is steps less every install step of a site in idle at or after
// from, and the sites it left out, in the configuration's order.
func dropIdle(cfg *config.Config, steps []convergeStep, from int, idle map[string]bool) ([]convergeStep, []string) {
	if len(idle) == 0 {
		return steps, nil
	}
	out := slices.Clone(steps[:from])
	for _, s := range steps[from:] {
		if s.Stage.installs() && s.Stage != stageStorage && idle[stepSite(s)] {
			continue
		}
		out = append(out, s)
	}
	var sites []string
	for _, name := range cfg.SiteNames() {
		if idle[name] {
			sites = append(sites, name)
		}
	}
	return out, sites
}

// leaveIdle ends site's install line as unchanged, once what its apply
// would record is recorded and checked, under the line, whose details and
// notes carry what that reports, as an install step's do.
func leaveIdle(r ui.Reporter, line ui.Track, site string, idle idleSites) error {
	line.Start()
	var reports []checkReport
	nested, done := ui.Nest(line, ui.Discard)
	err := func() error {
		defer done()
		if idle.record == nil {
			return nil
		}
		return idle.record(checkReporter{nested, r, &reports}, site)
	}()
	for _, n := range lineDetails(line, "", "", reports) {
		line.Note(n.Text, n.Detail)
	}
	if err != nil {
		line.Fail(err)
		return convergeStop("apply stopped at "+site+", left unchanged", err, resumeAdvice())
	}
	line.Done("unchanged")
	return nil
}

package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// The pull stage is every site's images, pulled on every host at once
// before anything is installed. An install runs one site at a time, and a
// site's apply pulls the images its stacks name as it recreates them, so on a
// first deployment the whole run waited on each host's downloads in turn,
// gigabytes each, while every other host sat idle. Pulled ahead, the images
// arrive on every host at the same time, and each apply then finds them
// present: its own pull is a check that the image is there.
//
// What a site pulls is exactly what its apply would: the stage plans the
// site with planSiteApply, as apply does, and pulls the plan's DiskCheck.Pulls,
// the rendered images the host lacks for the stacks the apply will recreate.
// The same plan carries the disk check, so a host short of space is refused
// here, with the words its apply would use, before any host has pulled a
// layer or installed anything. The plan is read and dropped: what it would
// refuse besides the disk (a file edited on the host, a conflict) is the
// apply's to report, at its own step, and the stage pulls regardless, since
// the images will be wanted once that is resolved.
//
// The stage pulls the reference the plan names, and the plan names Caddy by
// the digest its tag resolved to. apply resolves that tag through one cache
// for the whole run (cacheDigests), so the build pulled here is the build
// every later apply of the run plans: a tag that moved in between would
// otherwise have each apply name a build the host does not have.

// pullsAtOnce is how many images one host pulls at the same time. Docker
// already downloads a pull's layers in parallel; a few pulls at once keep the
// link busy across the gaps while one image extracts, without every image of
// a site competing for the same disk.
const pullsAtOnce = 3

// convergePullPlan is the plan the pull stage reads for one site: the plan
// apply makes. Tests replace it.
var convergePullPlan = func(cfg *config.Config, secrets *config.Secrets, site string, t apply.Transport, options ...apply.Option) (*apply.Plan, error) {
	return planSiteApply(cfg, secrets, site, t, options...)
}

// convergePullImage pulls one image on a host. Tests replace it.
var convergePullImage = apply.PullImage

// puller is the pull stage of one run: what every site's pull needs, read
// once. The secrets are read the first time a site needs them, since a dry
// run whose pull lines all wait on prepare or init never reads them.
type puller struct {
	cfg *config.Config
	o   convergeOptions

	once    sync.Once
	secrets *config.Secrets
	options []apply.Option
	err     error
}

func newPuller(cfg *config.Config, o convergeOptions) *puller {
	return &puller{cfg: cfg, o: o}
}

// load reads what every site's plan shares: the secrets, and apply's own
// options from the run's flags, as an apply step takes them.
func (p *puller) load() error {
	p.once.Do(func() {
		need := apply.DefaultMinFree
		if p.o.MinFree != "" {
			n, err := apply.ParseSize(p.o.MinFree)
			if err != nil {
				p.err = fmt.Errorf("apply: --min-free: %w", err)
				return
			}
			need = n
		}
		// The pull stage executes none of these plans, so none reads what
		// its apply's sweep would remove.
		p.options = []apply.Option{apply.MinFree(need), apply.SkipSweepRead()}
		if p.o.KeepImages {
			p.options = append(p.options, apply.KeepImages())
		}
		p.secrets, p.err = config.LoadDeploymentSecrets(convergeSecretsPath(p.o.Config, p.o.Secrets), p.cfg.ID)
	})
	return p.err
}

// reportLog is where a site's ssh retries go while its images pull, several
// at once on one transport: kept, as details of the site's line.
type reportLog struct {
	mu      *sync.Mutex
	reports *[]checkReport
}

func (w reportLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.reports = append(*w.reports, checkReport{Text: strings.TrimRight(string(p), "\n")})
	return len(p), nil
}

// plan is site's plan as its apply would make it, over a transport of its
// own whose retries are kept in reports, guarded by mu.
func (p *puller) plan(site string, mu *sync.Mutex, reports *[]checkReport) (*apply.Plan, apply.Transport, error) {
	if err := p.load(); err != nil {
		return nil, nil, err
	}
	t := siteTransport(site, p.cfg.Sites[site], "", p.o.Sudo)
	t.RetryLog = reportLog{mu, reports}
	plan, err := convergePullPlan(p.cfg, p.secrets, site, t, p.options...)
	if err != nil {
		return nil, nil, err
	}
	return plan, t, nil
}

// shortDisk is the stop for a host with too little space for its images:
// the refusal its apply would stop at, which says what could be freed.
func shortDisk(plan *apply.Plan) error {
	refusal := apply.DiskRefusal(plan)
	return &ui.Problem{
		Hint:    "too little free space to pull " + plural(len(plan.Disk.Pulls), "image"),
		Explain: refusal.Error(),
		Cause:   refusal,
	}
}

// pullStatus is a dry run's pull line for s: how many images the site's
// apply would pull and the free space they land in, nothing to pull, or the
// refusal --execute would stop at.
func (p *puller) pullStatus(s convergeStep) stepStatus {
	var st stepStatus
	var mu sync.Mutex
	plan, _, err := p.plan(stepSite(s), &mu, &st.reports)
	if err == nil && plan.Disk.Short() {
		err = shortDisk(plan)
	}
	switch {
	case err != nil:
		st.mark = ui.Failed
		st.result, st.explain = ui.Describe(err)
		st.problem = sentences(err)
	case plan.Disk == nil:
		st.mark, st.result = ui.OK, "nothing to pull"
	default:
		st.reports = append(st.reports, checkReport{Text: plan.Disk.Describe()})
		st.mark = ui.Pending
		st.result = plural(len(plan.Disk.Pulls), "image") + " to pull, " + plan.Disk.Summary()
	}
	return st
}

// pullSite pulls the images s's site will need, a few at once, on line. It
// returns what to show under the line and why it failed. Every image is
// tried, so a host that failed one has the others when the run resumes.
func (p *puller) pullSite(line ui.Line, s convergeStep) ([]checkReport, error) {
	site := stepSite(s)
	var mu sync.Mutex
	var reports []checkReport
	plan, t, err := p.plan(site, &mu, &reports)
	switch {
	case err != nil:
		return reports, err
	case plan.Disk == nil:
		line.Done("nothing to pull")
		return reports, nil
	case plan.Disk.Short():
		return reports, shortDisk(plan)
	}
	refs := plan.Disk.Pulls
	errs := make([]error, len(refs))
	slots := make(chan struct{}, pullsAtOnce)
	var wg sync.WaitGroup
	for i, ref := range refs {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			start := convergeNow()
			out, err := convergePullImage(t, ref)
			mark := ui.OK
			if err != nil {
				mark = ui.Failed
				errs[i] = pullFailure(site, ref, out, err)
			}
			mu.Lock()
			line.Detail("%s", innerLine(mark, "pull "+ref, since(start)))
			mu.Unlock()
		}()
	}
	wg.Wait()
	var failed []error
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err)
		}
	}
	switch len(failed) {
	case 0:
		line.Done(plural(len(refs), "image") + " pulled")
		return reports, nil
	case 1:
		return reports, failed[0]
	}
	var explain []string
	for _, err := range failed {
		explain = append(explain, strings.ReplaceAll(sentences(err), "\n", " "))
	}
	return reports, &ui.Problem{
		Hint:    fmt.Sprintf("could not pull %d of %s", len(failed), plural(len(refs), "image")),
		Explain: strings.Join(explain, "\n"),
		Cause:   errors.Join(failed...),
	}
}

// registryRefused is how a registry says no to an anonymous pull of an image it
// does not serve publicly.
var registryRefused = regexp.MustCompile(`(?i)unauthorized|denied`)

// pullFailure is why one image did not pull: the reference, and the last
// thing docker said. A registry that refused it means the image is private,
// which is said with what to do, since docker's own words for it ("denied",
// "unauthorized") read like a credential problem on this machine.
func pullFailure(site, ref, out string, err error) error {
	said := lastLine(strings.TrimSpace(out))
	if said == "" {
		said = errLine(err)
	}
	explain := site + ": docker pull " + ref + " failed: " + strings.TrimSuffix(said, ".") + "."
	if registryRefused.MatchString(out) || registryRefused.MatchString(err.Error()) {
		explain += " The registry refused it, so the image is private: make it public, or log the host in to that registry."
	}
	return &ui.Problem{Hint: "could not pull " + ref, Explain: explain, Cause: err}
}

// lastLine is the last line of s that is not blank.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// convergePullAll is the pull stage of a run with --execute: a line per
// site, every host at once, as prepare runs. A host that fails stops none of
// the others; once every one has ended, the run stops if any failed, naming
// each.
//
// It runs nothing through convergeQuiet or checkStep, which swap the
// process's reporterOverride and so allow one command at a time: each site's
// plan and pulls report straight to its own line, and its retries go to a
// log of its own on its transport, so nothing shared is touched while the
// hosts run at once.
func convergePullAll(r ui.Reporter, cfg *config.Config, steps []convergeStep, o convergeOptions) error {
	r.Section("pull")
	lines := ui.Group(r, siteTitles(steps)...)
	errs := make([]error, len(steps))
	p := newPuller(cfg, o)
	atOnce(len(steps), func(k int) {
		reports, err := p.pullSite(lines[k], steps[k])
		for _, n := range lineDetails(lines[k], "", "", reports) {
			lines[k].Note(n.Text, n.Detail)
		}
		errs[k] = err
		if err != nil {
			lines[k].Fail(err)
		}
	})
	return groupStop("pull", steps, errs)
}

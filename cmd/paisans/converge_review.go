package main

import (
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// convergeGrantHost is how apply with no --site reaches a site's grant
// record, to read it for the review and to write the web server's part.
// Tests replace it.
var convergeGrantHost = func(cfg *config.Config, site string, sudo bool) registry.Runner {
	return grantRunner(site, convergeHost(site, cfg.Sites[site], sudo))
}

// convergeRecord reads a site's grant record for the run's review. g is the
// grant it is compared with, which the real read has no use for. Tests
// replace it.
var convergeRecord = func(cfg *config.Config, site string, g grant.Grant, sudo bool) (grant.Record, error) {
	rec, _, _, err := grant.Read(convergeGrantHost(cfg, site, sudo), cfg.Deployment())
	return rec, err
}

// convergeRender is the render the run's review takes the images from.
// Tests replace it.
var convergeRender = func(cfg *config.Config, secrets *config.Secrets) (*render.Plan, error) {
	return render.Build(cfg, secrets)
}

// convergeReview is apply with no --site's review: one for every site, under
// a section of its own, keys, images and the web server alike, since the run
// prepares and applies them all. With execute it asks once, before the first
// step, and records what was approved for the steps to check against; a dry
// run asks nothing and ends with what --execute will ask for. It returns
// each site's review, for the web server's part the run records at its end.
//
// The images are the render's, and the render needs the secrets: a dry run
// whose secrets init has yet to generate reviews the keys and the web server
// alone, and says the images wait for init.
func convergeReview(r ui.Reporter, cfg *config.Config, o convergeOptions) (map[string]*siteReview, int, error) {
	scope := grant.AllScope
	var rendered *render.Plan
	secrets, err := config.LoadDeploymentSecrets(convergeSecretsPath(o.Config, o.Secrets), cfg.ID)
	if err == nil {
		rendered, err = convergeRender(cfg, secrets)
	}
	if err != nil {
		if o.Execute {
			return nil, 0, err
		}
		scope.Images = false
	}
	sites := cfg.SiteNames()
	reviews := make([]*siteReview, len(sites))
	errs := make([]error, len(sites))
	r.Section("review")
	for i, site := range sites {
		line := r.Step(site)
		g, err := grant.For(cfg, rendered, site)
		var rec grant.Record
		if err == nil {
			rec, err = convergeRecord(cfg, site, g, o.Sudo)
		}
		if err != nil {
			errs[i] = err
			line.Fail(err)
			if o.Execute {
				break
			}
			continue
		}
		reviews[i] = &siteReview{grant: g, scope: scope, review: grant.Compare(site, g, rec, scope), host: convergeGrantHost(cfg, site, o.Sudo), record: rec}
		endReview(r, line, reviews[i].review, o.Execute)
	}
	out := map[string]*siteReview{}
	var all []grant.Review
	pending := 0
	for i, s := range reviews {
		if errs[i] == nil && s == nil {
			continue
		}
		if errs[i] != nil {
			if o.Execute {
				return nil, 0, convergeStop("apply could not read what was granted on "+sites[i], errs[i], "Every site's review is read before anything runs. Make it reachable and run again.")
			}
			continue
		}
		out[sites[i]] = s
		all = append(all, s.review)
		pending += s.review.Pending()
	}
	if !scope.Images {
		r.Step("images").End(ui.Waiting, "reviewed once init has generated the secrets they render from")
	}
	if !o.Execute {
		return out, pending, nil
	}
	if err := approveReviews(r, o.stdin, o.stdout, all, "paisans apply --execute"); err != nil {
		return nil, 0, err
	}
	run := &runReview{approved: map[string]grant.Review{}}
	for site, s := range out {
		run.approved[site] = s.review
	}
	if pending > 0 {
		run.approvedBy = approver(cfg)
	}
	reviewedByRun = run
	return out, pending, nil
}

// convergeRecordWebServer records each site's web server part once the
// run's web server step has succeeded, since that step is what writes into
// the directory.
func convergeRecordWebServer(r ui.Reporter, cfg *config.Config, reviews map[string]*siteReview) error {
	by := ""
	if reviewedByRun != nil {
		by = reviewedByRun.approvedBy
	}
	for _, site := range cfg.SiteNames() {
		s := reviews[site]
		if s == nil {
			continue
		}
		web := *s
		web.scope = grant.Scope{WebServer: true}
		if err := recordGrant(r, &web, cfg.Deployment(), by); err != nil {
			return err
		}
	}
	return nil
}

// convergeRecords is each reviewed site's grant record, by site.
func convergeRecords(reviews map[string]*siteReview) map[string]grant.Record {
	out := map[string]grant.Record{}
	for site, s := range reviews {
		if s != nil {
			out[site] = s.record
		}
	}
	return out
}

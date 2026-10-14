package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostarch"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// readArchitectures asks each of sites for its architecture, one after
// another, and refuses a host that contradicts the arch its site records
// before anything on it changes. See hostarch.Mismatch.
func readArchitectures(r ui.Reporter, cfg *config.Config, sites []string, host func(name string) hostarch.Runner) (map[string]string, error) {
	line := r.Step("read each site's architecture")
	read := map[string]string{}
	var shown []string
	for _, name := range sites {
		arch, err := hostarch.Read(name, host(name))
		if err == nil {
			err = hostarch.Mismatch(name, cfg.Sites[name].Arch, arch)
		}
		if err != nil {
			line.Fail(err)
			return nil, err
		}
		read[name] = arch
		shown = append(shown, name+" "+arch)
	}
	line.Done(strings.Join(shown, ", "))
	return read, nil
}

// recordArchitectures puts every architecture in read that its site does not
// record yet into the configuration. With execute it writes them into the
// file at path in one edit, says so for each, and returns the configuration
// read again, as init's write is. A whole apply does this before it publishes
// the file, because each of its steps refuses bytes changed after the
// publish. A dry run writes nothing and sets them in memory, so its plan is
// checked against what the hosts reported.
//
// Either way, validate's data site rule is applied again afterwards: a site
// whose key was missing when validate ran could otherwise carry a mixed
// cluster past it.
func recordArchitectures(r ui.Reporter, path string, cfg *config.Config, read map[string]string, execute bool) (*config.Config, error) {
	missing := map[string]string{}
	for name, arch := range read {
		if cfg.Sites[name].Arch == "" {
			missing[name] = arch
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		if execute {
			if err := config.SetArch(path, missing); err != nil {
				return nil, err
			}
			for _, name := range names {
				r.Note(fmt.Sprintf("%s: arch %s recorded in %s", name, missing[name], config.FileName), "")
			}
			reread, err := config.Load(path)
			if err != nil {
				return nil, err
			}
			cfg = reread
		} else {
			// Copy before setting so the caller's configuration is left as it
			// was read.
			planned := *cfg
			planned.Sites = make(map[string]config.Site, len(cfg.Sites))
			for name, site := range cfg.Sites {
				planned.Sites[name] = site
			}
			cfg = &planned
			for _, name := range names {
				site := cfg.Sites[name]
				site.Arch = missing[name]
				cfg.Sites[name] = site
				r.Note(fmt.Sprintf("%s: arch %s, recorded in %s by --execute", name, missing[name], config.FileName), "")
			}
		}
	}
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "data-sites-differ-in-arch" {
			return nil, errors.New(f.Key + ": " + f.Message)
		}
	}
	return cfg, nil
}

// checkPlatforms refuses an image the render names for one of sites that has
// no build for the site's architecture (apply.CheckPlatforms), through the
// run's resolver, so the tags it resolves are the builds the run's plans pin.
// It reads no host, so it runs before the first change: apply's own check
// comes only once a site's host is prepared, which on a new deployment, or
// on the site site add joins, is after every other site has changed.
//
// Each refusal is shown on its own, as validate's are, and the run stops on
// them, a dry run included: no step after it would succeed.
func checkPlatforms(r ui.Reporter, cfg *config.Config, rendered *render.Plan, sites []string) error {
	line := r.Step("check every image has a build for its site")
	var notes []apply.Note
	var refusals []error
	for _, site := range sites {
		n, refused := apply.CheckPlatforms(site, cfg.Sites[site].Arch, rendered.Images[site], imageDigests)
		notes = append(notes, n...)
		refusals = append(refusals, refused...)
	}
	if len(refusals) == 0 {
		line.Done("")
		for _, n := range notes {
			r.Note(n.Hint, n.Text)
		}
		return nil
	}
	line.End(ui.Failed, plural(len(refusals), "image")+" without a build for its site")
	for _, err := range refusals {
		r.Refuse("no build for the site's architecture", err.Error())
	}
	return &ui.Problem{
		Hint:    fmt.Sprintf("the plan was refused: %s above", plural(len(refusals), "refusal")),
		Explain: "A site runs only images published for its architecture. Give the app to sites of an architecture its image is published for, or name an image that has a build for this one, then run the command again.",
		Cause:   errors.Join(refusals...),
	}
}

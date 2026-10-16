package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// admittedHosts is what every command that writes to a host does before
// anything else there: it reads each named site's admission record, as the
// deployment's user, and refuses a host that has not admitted the
// deployment, or whose admission does not cover the site, with nothing on
// any host changed. host admit claimed the host in its registry, which is
// root's; the record is how the deployment learns it was admitted, and as
// what. Sites are read in name order, each a step, so a spinner shows while
// ssh answers.
func admittedHosts(r ui.Reporter, cfg *config.Config, transports map[string]registry.Runner) (map[string]admission.Record, error) {
	sites := slices.Sorted(maps.Keys(transports))
	out := map[string]admission.Record{}
	for _, site := range sites {
		name := "site"
		if len(sites) > 1 {
			name = site
		}
		rec, err := ui.Get(r, "check "+name+"'s admission", func() (admission.Record, error) {
			rec, err := admissionForSite(transports[site], cfg.ID, site, cfg.Sites[site])
			if err != nil {
				return rec, admissionProblem(site, err)
			}
			return rec, nil
		})
		if err != nil {
			return nil, err
		}
		out[site] = rec
	}
	return out, nil
}

// registryHost is how the admission check reaches a site's host when the command has no
// transport of its own to lend it. A test replaces it.
var registryHost = func(name string, site config.Site, destination string) registry.Runner {
	return siteTransport(name, site, destination)
}

// admissionForSite is how admittedHosts reads and judges a record. A test
// replaces it.
var admissionForSite = admission.ForSite

// admittedSites is admittedHosts over the named sites, each reached through
// its own ssh section.
func admittedSites(r ui.Reporter, cfg *config.Config, sites ...string) (map[string]admission.Record, error) {
	transports := map[string]registry.Runner{}
	for _, name := range sites {
		transports[name] = registryHost(name, cfg.Sites[name], "")
	}
	return admittedHosts(r, cfg, transports)
}

// admissionProblem is a refusal in the operator's words: what the record
// lacks, and the two lines that fix it, the operator's and the owner's.
func admissionProblem(site string, err error) error {
	lines := fmt.Sprintf("Send the host's owner a new request:\n\n    paisans host request --site %[1]s > %[1]s.admit\n\nand ask them to run, on the host:\n\n    sudo paisans host admit --request %[1]s.admit", site)
	var se *admission.SiteError
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
		return &ui.Problem{Hint: site + " is not admitted", Explain: "Its host has no admission record for this deployment: its owner has not admitted it, or has evicted it. " + lines, Cause: err}
	case errors.As(err, &se):
		return &ui.Problem{Hint: site + "'s admission does not match " + config.FileName, Explain: "- " + strings.Join(se.Problems, "\n- ") + "\n" + lines, Cause: err}
	}
	return err
}

// union is the sorted set of every name in lists.
func union(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

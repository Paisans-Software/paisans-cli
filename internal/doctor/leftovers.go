package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// LeftoverProbe is one site's ownership report, or why it could not be made.
// cmd/paisans takes the host check's inventory (hostcheck.Inspect, reads
// only) and classifies it with internal/ownership.
type LeftoverProbe struct {
	Site   string
	Report ownership.Report
	Err    string
	// Manifest is whether the site has the manifest apply writes, and
	// ManifestBuild the paisans that wrote it. ManifestErr is the refusal
	// of a manifest that does not say which build wrote it.
	Manifest      bool
	ManifestBuild version.Build
	ManifestErr   string
}

// Leftovers warns about anything of this deployment left on a site that its
// configuration no longer renders there, and lists, as info, every foreign
// thing that relies on the site's Caddy. A leftover is a warning and not a
// failure: it serves nothing and breaks nothing, but it holds disk, may hold
// a port, and an operator who does not know it is there cannot decide what
// to do with it. Nothing here is removed, by doctor or by apply; the warning
// names `paisans app remove <app>` for each app the leftovers belong to.
func Leftovers(probes []LeftoverProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		if p.Err != "" {
			out = append(out, Finding{Section: SectionLeftovers, Level: Skip, Line: fmt.Sprintf("%s: the host's inventory could not be read", p.Site), More: []string{p.Err}})
			continue
		}
		switch {
		case p.ManifestErr != "":
			out = append(out, Finding{Section: SectionLeftovers, Level: Fail, Line: fmt.Sprintf("%s: its manifest does not say which build of paisans wrote it", p.Site), More: []string{p.ManifestErr}})
		case p.Manifest:
			out = append(out, Finding{Section: SectionLeftovers, Level: Info, Line: fmt.Sprintf("%s: last applied by paisans %s", p.Site, p.ManifestBuild)})
		}
		if lines := p.Report.LeftoverLines(); len(lines) > 0 {
			more := append([]string{}, lines...)
			more = append(more, "These are this deployment's and are no longer rendered for this site. apply leaves them in place.")
			for _, app := range p.Report.Apps() {
				more = append(more, fmt.Sprintf("`"+progname.Name()+" app remove %s` takes %s's off every site, once a whole apply has run on each; it keeps its data unless given --delete-data.", app, app))
			}
			out = append(out, Finding{Section: SectionLeftovers, Level: Warn, Line: fmt.Sprintf("%s: %d thing(s) of this deployment left over", p.Site, len(lines)), More: more})
		} else {
			out = append(out, Finding{Section: SectionLeftovers, Level: OK, Line: fmt.Sprintf("%s: nothing of this deployment left over", p.Site)})
		}
		if lines := p.Report.ForeignLines(); len(lines) > 0 {
			more := append([]string{}, lines...)
			more = append(more, "Not this deployment's, and never changed by it. Moving or stopping this site's Caddy takes them with it.")
			out = append(out, Finding{Section: SectionLeftovers, Level: Info, Line: fmt.Sprintf("%s: %d foreign thing(s) rely on this deployment's Caddy", p.Site, len(lines)), More: more})
		}
	}
	return out
}

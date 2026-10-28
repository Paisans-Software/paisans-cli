package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// ConvergenceProbe is what one site says it applied: the revision its grant
// record holds, which apply writes only once every step it ran for the site
// succeeded, and what the pending record says a stopped apply still owes it,
// described (apply.Owed), empty for nothing. Newest is the revision this
// workstation's copy holds, 0 when it holds none. Err is why the records
// could not be read.
type ConvergenceProbe struct {
	Site    string
	Applied int
	Newest  int
	Owed    string
	Err     string
}

func (p ConvergenceProbe) site() string { return p.Site }

// Convergence says, per site, whether what is published is what runs there.
// apply publishes a version before it changes any host, so that the store
// holds what is applied even when the run stops part way; a stopped run
// therefore leaves a version every site holds and some site does not run,
// and nothing else says so. A site behind is a warning and not a failure: it
// serves what it last applied, and the next apply resumes it.
func Convergence(probes []ConvergenceProbe) []Finding {
	resume := fmt.Sprintf("Run `%s deployment apply --execute` to resume it; the run log under runs/ in the data directory has what stopped the last one.", progname.Cmd())
	var out []Finding
	for _, p := range probes {
		f := Finding{Section: SectionConvergence}
		switch {
		case p.Err != "":
			f.Level, f.Line, f.More = Skip, p.Site+": what was applied could not be read", []string{p.Err}
		case p.Owed != "":
			f.Level, f.Line, f.More = Warn, p.Site+": an apply stopped part way and still owes "+p.Owed, []string{resume}
		case p.Applied == 0:
			f.Level, f.Line, f.More = Warn, p.Site+": no apply has finished here", []string{resume}
		case p.Newest > p.Applied:
			f.Level, f.Line, f.More = Warn, fmt.Sprintf("%s: applied revision %d; revision %d is published but not applied here", p.Site, p.Applied, p.Newest), []string{resume}
		case p.Newest == p.Applied:
			f.Level, f.Line = OK, fmt.Sprintf("%s: applied revision %d, the newest", p.Site, p.Applied)
		default:
			f.Level, f.Line = OK, fmt.Sprintf("%s: applied revision %d", p.Site, p.Applied)
		}
		out = append(out, f)
	}
	return out
}

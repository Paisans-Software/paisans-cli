package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostprep"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// AllowlistProbe is one site's key allowlist, as its host holds it: the
// fingerprints host prepare may add to the login user's authorized_keys
// (internal/hostprep/allow.go).
type AllowlistProbe struct {
	Site    string
	Found   bool
	Content string
	Err     string
}

// Allowlist says, for each site, whether its login user has a list, how many
// keys it holds, and each key ssh.keys names that it does not: the refusal
// the next host prepare would give if it had to add that key. A key missing
// from the list is a warning, not a failure, because nothing is broken now
// and a key already in authorized_keys keeps working.
func Allowlist(cfg *config.Config, probes []AllowlistProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		site := cfg.Sites[p.Site]
		user := site.SSH.User
		path := hostprep.AllowPath(user)
		switch {
		case p.Err != "":
			out = append(out, Finding{Section: SectionAllowlist, Level: Skip, Line: fmt.Sprintf("%s: could not read %s", p.Site, path), More: []string{ui.Visible(firstLine(p.Err))}})
			continue
		case !p.Found:
			out = append(out, Finding{Section: SectionAllowlist, Level: Info, Line: fmt.Sprintf("%s: no list for %s; host prepare adds any key ssh.keys names", p.Site, user)})
			continue
		}
		lines := hostprep.ParseAllow(p.Content)
		out = append(out, Finding{Section: SectionAllowlist, Level: OK, Line: fmt.Sprintf("%s: %s's list holds %d %s", p.Site, user, len(lines), plural(len(lines), "key"))})
		keys, _ := site.SSH.AuthorizedKeys()
		for _, k := range keys {
			if hostprep.Allows(lines, k.Fingerprint) {
				continue
			}
			out = append(out, Finding{Section: SectionAllowlist, Level: Warn,
				Line: fmt.Sprintf("%s: %s (%s) is not on %s's list", p.Site, ui.Visible(k.Fingerprint), ui.Visible(k.Name), user),
				More: []string{
					fmt.Sprintf("host prepare refuses to add it to %s's authorized_keys on %s, whoever runs it; one already there keeps working.", user, p.Site),
					fmt.Sprintf("The host's owner allows it, after checking the fingerprint with %s, with: paisans host allow %s %s", k.Name, p.Site, k.Name),
				}})
		}
	}
	return out
}

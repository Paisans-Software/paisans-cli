package doctor

import (
	"fmt"
	"slices"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// AllowlistProbe is one site's key allowlist, as its host holds it: the
// fingerprints host prepare may add to the login user's authorized_keys
// (internal/hostprep/allow.go).
type AllowlistProbe struct {
	Site    string
	Found   bool
	Content string
	Err     string
	// KeysRead says Authorized was read: the fingerprint of every key in the
	// login user's authorized_keys, which host prepare never appends again.
	KeysRead   bool
	Authorized []string
}

// Allowlist says, for each site, whether its login user has a list, how many
// keys it holds, and each key ssh.keys names that the next host prepare
// would append and the list does not hold: the refusal it would give. A key
// already in authorized_keys is never appended, so it is not one, whatever
// the list says. Where authorized_keys could not be read, every key the list
// lacks is reported, as the refusal host prepare would give if it had to add
// it. Each is a warning, not a failure, because nothing is broken now.
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
			if hostprep.Allows(lines, k.Fingerprint) || slices.Contains(p.Authorized, k.Fingerprint) {
				continue
			}
			line := fmt.Sprintf("%s: %s (%s) is not on %s's list, and host prepare would add it", p.Site, ui.Visible(k.Fingerprint), ui.Visible(k.Name), user)
			first := fmt.Sprintf("It is not in %s's authorized_keys on %s, so the next host prepare would add it, and refuses to, whoever runs it.", user, p.Site)
			if !p.KeysRead {
				line = fmt.Sprintf("%s: %s (%s) is not on %s's list, and host prepare would refuse it if it had to add it", p.Site, ui.Visible(k.Fingerprint), ui.Visible(k.Name), user)
				first = fmt.Sprintf("%s's authorized_keys on %s could not be read, so whether host prepare has to add it is not known. If it does, it refuses to, whoever runs it; a key already there keeps working.", user, p.Site)
			}
			out = append(out, Finding{Section: SectionAllowlist, Level: Warn, Line: line,
				More: []string{
					first,
					fmt.Sprintf("The host's owner allows it, after checking the fingerprint with %s, with: paisans host allow %s %s", ui.Visible(k.Name), p.Site, ui.Visible(k.Name)),
				}})
		}
	}
	return out
}

package doctor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// SectionSudo is what sudo lets the deployment's user run.
const SectionSudo = "sudo rights"

// SudoCommand lists what sudo lets the login user run, without asking for
// a password, in the C locale: sudo translates its answer, and the reading
// knows its English words. The transport returns stderr with stdout, so a
// refusal is in the answer too. sudo-rs's -l lists no Defaults, so where
// sudo is sudo-rs the original, installed beside it as sudo.ws, lists what
// the same sudoers grant, as site admit reads them; with no sudo.ws it
// prints sudoRSAlone.
const SudoCommand = "if sudo -V 2>/dev/null | sed -n 1p | grep -q '^sudo-rs'; then " +
	"if [ -x /usr/bin/sudo.ws ]; then LC_ALL=C /usr/bin/sudo.ws -n -l; else echo '" + sudoRSAlone + "'; fi; " +
	"else LC_ALL=C sudo -n -l; fi"

// sudoRSAlone is SudoCommand's answer on a host whose sudo is sudo-rs with
// no sudo.ws beside it.
const sudoRSAlone = "paisans: sudo is sudo-rs, and /usr/bin/sudo.ws is not installed"

// SudoProbe is SudoCommand's answer on one site.
type SudoProbe struct{ Site, Out, Err string }

func (p SudoProbe) site() string { return p.Site }

// SudoRights checks that sudo lets the deployment's user run the WireGuard
// helper and nothing else, as site admit accepts it: its one rule, no
// Defaults entry that hands the helper the user's environment or path, and
// no runas or command-specific Defaults line (admitrules.SudoList.
// BeyondHelper). Every operator of the deployment can log in as the user,
// so anything more is theirs too, and root on the host for anything past
// the helper. An answer it cannot read is a warning that says so, never a
// pass standing in for one that grants nothing.
func SudoRights(cfg *config.Config, probes []SudoProbe) []Finding {
	d := cfg.Deployment()
	helper, helperErr := apply.WGHelper{Iface: d.Interface()}.Path()
	var out []Finding
	for _, p := range probes {
		user := cfg.Sites[p.Site].SSHUser()
		missing := Finding{Section: SectionSudo, Level: Fail,
			Line: fmt.Sprintf("%s: sudo does not let %s run the WireGuard helper", p.Site, user),
			More: []string{
				"Every change to the mesh (its file, bringing it up, syncing its peers) runs the helper with sudo -n, so apply cannot manage the mesh on this site.",
				"recover:",
				fmt.Sprintf("  1. The host's owner runs `"+progname.Default+" site admit` for %s again, which installs %s and its line in /etc/sudoers.d/%s.", p.Site, helper, d.Prefix()),
			}}
		if helperErr != nil {
			out = append(out, Finding{Section: SectionSudo, Level: Fail,
				Line: fmt.Sprintf("%s: cannot name the WireGuard helper to look for: %v", p.Site, helperErr),
				More: []string{
					"The helper is named after the deployment's WireGuard interface, which comes from the `id` in its configuration file, so this is the configuration, not the host.",
					"recover:",
					"  1. Restore the `id` this deployment was created with in its configuration file; every name on its hosts is derived from it.",
					fmt.Sprintf("  2. `"+progname.Name()+" doctor --site %s` again.", p.Site),
				}})
			continue
		}
		if strings.Contains(p.Out, sudoRSAlone) {
			out = append(out, Finding{Section: SectionSudo, Level: Warn,
				Line: fmt.Sprintf("%s: sudo is sudo-rs, so what it lets %s run cannot be read", p.Site, user),
				More: []string{
					"sudo-rs's sudo -l lists no Defaults, so doctor reads them from the original sudo, installed beside it as /usr/bin/sudo.ws, which is missing here. A Defaults entry that hands the WireGuard helper the user's environment would make the user root on the host.",
					"recover:",
					"  1. The host's owner installs Ubuntu's sudo package, which installs /usr/bin/sudo.ws.",
					fmt.Sprintf("  2. `"+progname.Name()+" doctor --site %s` again.", p.Site),
				}})
			continue
		}
		if strings.Contains(p.Out, "a password is required") {
			out = append(out, missing)
			continue
		}
		list, ok := admitrules.ReadSudoList(user, strings.Split(strings.TrimRight(p.Out, "\n"), "\n"))
		if !ok {
			more := append(lastLines(p.Out+"\n"+p.Err, 3),
				"recover:",
				fmt.Sprintf("  1. The host's owner runs `LC_ALL=C sudo -n -l` (`/usr/bin/sudo.ws` in place of `sudo` where sudo is sudo-rs) as %s on %s and reads the answer: a translated or unfamiliar sudo is the likely cause.", user, p.Site),
				fmt.Sprintf("  2. If it lists %s and nothing else, report that output as a doctor bug. If not, the host's owner checks the line site admit wrote in /etc/sudoers.d/%s with `visudo -c`, or runs `"+progname.Default+" site admit` for %s again.", admitrules.HelperRule(helper), d.Prefix(), p.Site),
				fmt.Sprintf("  3. `"+progname.Name()+" doctor --site %s` again.", p.Site),
			)
			out = append(out, Finding{Section: SectionSudo, Level: Warn, Line: fmt.Sprintf("%s: could not read what sudo lets %s run", p.Site, user), More: more})
			continue
		}
		beyond, defaults := list.BeyondHelper(helper)
		if extra := slices.Concat(beyond, defaults); len(extra) > 0 {
			shown := make([]string, len(extra))
			for i, e := range extra {
				shown[i] = ui.Visible(e)
			}
			out = append(out, Finding{Section: SectionSudo, Level: Fail,
				Line: fmt.Sprintf("%s: sudo lets %s reach past the WireGuard helper: %s", p.Site, user, strings.Join(shown, "; ")),
				More: []string{
					fmt.Sprintf("Every operator of this deployment can log in as %s, so whatever sudo allows it is theirs too, and root on the host for anything past the helper. site admit writes one line, %s, in /etc/sudoers.d/%s; the rest comes from elsewhere in the host's sudoers.", user, admitrules.HelperRule(helper), d.Prefix()),
					"recover:",
					fmt.Sprintf("  1. The host's owner lists every rule and Defaults entry for the user with `sudo -l -U %s` (`/usr/bin/sudo.ws -l -U` where sudo is sudo-rs) and removes all but site admit's line with `visudo`, looking in /etc/sudoers and /etc/sudoers.d.", user),
					fmt.Sprintf("  2. `"+progname.Name()+" doctor --site %s` again.", p.Site),
				}})
			continue
		}
		if slices.Contains(list.Rules, admitrules.HelperRule(helper)) {
			out = append(out, Finding{Section: SectionSudo, Level: OK, Line: fmt.Sprintf("%s: sudo lets %s run the WireGuard helper and nothing else", p.Site, user)})
			continue
		}
		out = append(out, missing)
	}
	return out
}

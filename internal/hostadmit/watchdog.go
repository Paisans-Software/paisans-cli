package hostadmit

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// udevTrigger applies the rules to the devices that exist now (spec,
// Observed R4).
const udevTrigger = "udevadm control --reload && udevadm trigger --subsystem-match=watchdog --subsystem-match=misc"

// giveBack makes each watchdog node root's again. udev sets an owner only
// where a rule says so and never resets one, so removing the rule and
// triggering leaves the device the user's until the module reloads.
const giveBack = `for d in /dev/watchdog /dev/watchdog[0-9]*; do if [ -e "$d" ]; then chown root:root "$d" && chmod 600 "$d"; fi; done`

// watchdogRule gives the device to the user, owner and group both: a
// device whose group is root's cannot be chowned inside the user
// namespace, and the infra compose chowns it for Patroni (Observed R4).
func watchdogRule(user, group string) string {
	return "# Written by paisans host admit: the watchdog is " + user + "'s, for its\n" +
		"# Patroni. One deployment per host holds it; paisans host evict removes this.\n" +
		`SUBSYSTEM=="watchdog|misc", KERNEL=="watchdog*", OWNER="` + user + `", GROUP="` + group + `", MODE="0600"` + "\n"
}

// planWatchdogGrant gives the watchdog with --watchdog: the module as host
// prepare loads it, then the device to the user. One deployment per host
// holds it, so another deployment's rule refuses. Without the grant, a
// rule an earlier admit wrote is removed, with the module's unit.
func planWatchdogGrant(p *planner) error {
	own := watchdogRulePath(p.d)
	unit := "/etc/systemd/system/" + p.d.Prefix() + "-watchdog.service"
	if !p.in.Flags.Watchdog {
		if p.f.Files[own].Present {
			p.step(Step{Title: "take the watchdog back", Describe: "watchdog: this site is no longer a data site; remove " + own + " and give the device back to root",
				Command: "rm -f " + own + " && " + udevTrigger + " && " + giveBack, Kinds: []string{hostprep.KindWatchdog}})
		}
		if p.f.Files[unit].Present {
			p.step(Step{Title: "stop loading the watchdog at boot", Describe: "watchdog: remove " + unit,
				Command: "systemctl disable " + p.d.Prefix() + "-watchdog.service; rm -f " + unit + " && systemctl daemon-reload", Kinds: []string{hostprep.KindWatchdog}})
		}
		return nil
	}
	for _, r := range p.f.WatchdogRules {
		if r != own {
			return fmt.Errorf("another deployment holds this host's watchdog (%s), and only one Patroni can open the device. Admit this data site on another host, or evict that deployment first. Nothing was changed", ui.Visible(r))
		}
	}
	s, err := hostprep.PlanWatchdog(p.t, p.d, p.profile)
	if err != nil {
		return errors.New(strings.TrimPrefix(shown(err).Error(), "watchdog: ") + ". Nothing was changed")
	}
	p.section(s)
	user := p.in.Request.User
	rule := watchdogRule(user, groupOf(p))
	f := p.f.Files[own]
	switch {
	case f.Link:
		return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", own)
	case f.Sum == sha(rule) && f.Mode == "644" && f.UID == 0:
		var root []string
		for _, dev := range sortedKeys(p.f.WatchdogDevs) {
			if p.f.WatchdogDevs[dev] != p.f.User.UID {
				root = append(root, ui.Visible(dev))
			}
		}
		if len(root) == 0 {
			p.present("watchdog: the device is " + user + "'s")
			break
		}
		p.step(Step{Title: "give the watchdog to " + user, Describe: "watchdog: " + own + " is in place and " + strings.Join(root, ", ") + " is not " + user + "'s yet; apply the rule again",
			Command: udevTrigger, Kinds: []string{hostprep.KindWatchdog}})
	default:
		p.step(Step{Title: "give the watchdog to " + user, Describe: "watchdog: " + own + " makes /dev/watchdog and /dev/watchdog0 " + user + "'s, owner and group, mode 600",
			File: &hostprep.File{Path: own, Content: rule, Mode: 0o644}, Command: udevTrigger, Kinds: []string{hostprep.KindWatchdog}, Provisional: !p.f.User.Exists})
	}
	p.warn("watchdog: "+user+"'s Patroni can reboot this machine", "Patroni arms the watchdog and reboots the whole host when it stops renewing it, which is what makes its failover safe.")
	return nil
}

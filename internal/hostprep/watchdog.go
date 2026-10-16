package hostprep

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// softdogIdentity is the identity softdog reports in sysfs. Read from the
// kernel source, drivers/watchdog/softdog.c (softdog_info.identity), not
// observed on a host.
const softdogIdentity = "Software Watchdog"

// softdogWarning is printed whenever auto falls back to softdog, with the
// command to run again, prepare or admit. It is a warning and not a
// refusal because softdog still fences the common failure, a Patroni that
// stopped renewing its lease.
const softdogWarning = "the watchdog will be softdog. It reboots the host when Patroni hangs, but not when the kernel does, and a hung kernel cannot fence itself any other way. A hardware or hypervisor watchdog is better: enable one in the firmware or the VM's settings, and %s again."

// watchdogDevice is one entry under /sys/class/watchdog.
type watchdogDevice struct {
	Name     string // watchdog0
	Identity string // iTCO_wdt, Software Watchdog, ...
}

func (d watchdogDevice) softdog() bool { return d.Identity == softdogIdentity }

// watchdogFacts is what the generic probe found. The probe reads kernel and
// systemd interfaces that are the same on every distribution this toolkit is
// likely to support, so it is not part of a profile.
type watchdogFacts struct {
	DeviceNode bool
	Devices    []watchdogDevice
	// RuntimeWatchdog is systemd's RuntimeWatchdogUSec, as `systemctl show`
	// prints it. Anything but zero means PID 1 holds the device.
	RuntimeWatchdog string
	Daemon          bool
	// Owner is /dev/watchdog's owner's uid. Host admit gives the device to
	// the deployment's user, and Patroni's container may hand it on to its
	// own user, which maps into the user's subordinate range.
	Owner string
	// Me is the user the probe ran as and its subordinate range.
	Me hostcheck.Self
}

// mine reports whether the deployment owns /dev/watchdog: its user does, or
// one of its containers' users.
func (f watchdogFacts) mine() bool {
	owner, err := strconv.Atoi(f.Owner)
	return err == nil && f.Me.Owns(owner)
}

// probeWatchdog reads the watchdog. asUser also reads who the probe ran as,
// which only the deployment's user's read needs: admit runs it as root.
func probeWatchdog(t Transport, asUser bool) (watchdogFacts, error) {
	script, err := snippet("snippets/watchdog-probe.sh", nil)
	if err != nil {
		return watchdogFacts{}, err
	}
	if asUser {
		script += "\n" + hostcheck.SelfProbe
	}
	out, err := t.Run(script)
	if err != nil {
		return watchdogFacts{}, fmt.Errorf("probing the watchdog on %s: %w", t.Describe(), err)
	}
	var facts watchdogFacts
	if asUser {
		if facts.Me, err = hostcheck.ParseSelf(out); err != nil {
			return watchdogFacts{}, fmt.Errorf("probing the watchdog on %s: %w", t.Describe(), err)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		key, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "device":
			facts.DeviceNode = rest == "present"
		case "identity":
			name, identity, _ := strings.Cut(rest, " ")
			facts.Devices = append(facts.Devices, watchdogDevice{Name: name, Identity: strings.TrimSpace(identity)})
		case "systemd":
			facts.RuntimeWatchdog = strings.TrimSpace(rest)
		case "daemon":
			facts.Daemon = rest == "running"
		case "owner":
			facts.Owner = strings.TrimSpace(rest)
		}
	}
	return facts, nil
}

// systemdHoldsWatchdog reads RuntimeWatchdogUSec. Zero, or nothing at all on a
// systemd too old to report it, means PID 1 has not opened the device.
func systemdHoldsWatchdog(value string) bool {
	switch value {
	case "", "0", "0s", "off":
		return false
	}
	return true
}

func (f watchdogFacts) describe() string {
	if len(f.Devices) == 0 {
		return "/dev/watchdog (driver not reported in /sys/class/watchdog)"
	}
	var parts []string
	for _, d := range f.Devices {
		parts = append(parts, fmt.Sprintf("%s is %s", d.Name, d.Identity))
	}
	return strings.Join(parts, ", ")
}

func (f watchdogFacts) hardware() []watchdogDevice {
	var out []watchdogDevice
	for _, d := range f.Devices {
		if !d.softdog() {
			out = append(out, d)
		}
	}
	return out
}

func (f watchdogFacts) softdogLoaded() bool {
	for _, d := range f.Devices {
		if d.softdog() {
			return true
		}
	}
	return false
}

// planWatchdog decides what a data site's watchdog needs, by the site's mode.
//
// Contention is checked first and refuses in every mode. Only one
// process can open /dev/watchdog, and if systemd or a watchdog daemon already
// has it, Patroni fails to open it at its first start, on a host that a plan
// had just called prepared. by is the command planning it, prepare or
// admit, which its words name.
func planWatchdog(t Transport, d deployment.Deployment, profile Profile, site config.Site, by string) (Section, error) {
	var out Section
	mode := site.WatchdogMode()
	if !site.Has(config.RoleData) {
		out.Present = append(out.Present, "watchdog: not needed, this site holds no data role")
		return out, nil
	}

	facts, err := probeWatchdog(t, false)
	if err != nil {
		return out, err
	}
	if systemdHoldsWatchdog(facts.RuntimeWatchdog) {
		return out, fmt.Errorf("watchdog: systemd's RuntimeWatchdogUSec is %s on %s, so PID 1 holds /dev/watchdog and Patroni cannot open it. Set RuntimeWatchdogSec=0 in /etc/systemd/system.conf (or remove the drop-in that sets it), run systemctl daemon-reexec, and %s again", facts.RuntimeWatchdog, t.Describe(), by)
	}
	if facts.Daemon {
		return out, fmt.Errorf("watchdog: a watchdog daemon is running on %s and holds /dev/watchdog, so Patroni cannot open it. Stop and disable it (systemctl disable --now watchdog) and %s again", t.Describe(), by)
	}

	hardware := facts.hardware()
	switch mode {
	case config.WatchdogRequired:
		if len(hardware) == 0 {
			found := "no watchdog device"
			if facts.softdogLoaded() {
				found = "only softdog"
			}
			return out, fmt.Errorf("watchdog: required, and %s has %s. required accepts a hardware or hypervisor watchdog and nothing else. Enable one in the firmware or the VM's settings, or declare watchdog: auto to accept softdog", t.Describe(), found)
		}
		out.Present = append(out.Present, "watchdog: "+facts.describe())
		return out, nil

	case config.WatchdogSoftdog:
		if len(hardware) > 0 {
			out.Warnings = append(out.Warnings, Warning{
				Hint:   "watchdog: softdog declared, but a hardware watchdog exists",
				Detail: fmt.Sprintf("watchdog: softdog was declared, but %s already provides /dev/watchdog and Patroni will open that one, not softdog. Declare auto to say so.", hardware[0].Identity)})
		}
		module, err := profile.WatchdogModule(t, d, "softdog", facts.softdogLoaded(), by == "admit")
		if err != nil {
			return out, err
		}
		out.add(module)
		return out, nil
	}

	// auto: whatever is there, else softdog with a warning.
	if len(hardware) > 0 || (facts.DeviceNode && len(facts.Devices) == 0) {
		out.Present = append(out.Present, "watchdog: "+facts.describe())
		return out, nil
	}
	if facts.softdogLoaded() {
		out.Present = append(out.Present, "watchdog: "+facts.describe())
	}
	module, err := profile.WatchdogModule(t, d, "softdog", facts.softdogLoaded(), by == "admit")
	if err != nil {
		return out, err
	}
	out.add(module)
	out.Warnings = append(out.Warnings, Warning{Hint: "watchdog: falling back to softdog", Detail: fmt.Sprintf(softdogWarning, by)})
	return out, nil
}

// admitWatchdog is what fixes the watchdog on site when host prepare finds
// it wrong: it is a root change, the host owner's.
func admitWatchdog(site string) string {
	return fmt.Sprintf("The host's owner runs sudo paisans host admit --request %[1]s.admit --watchdog again, from paisans host request --site %[1]s > %[1]s.admit.", site)
}

// checkWatchdog reads what a data site's watchdog is, as the deployment's
// user: host admit loaded it and gave /dev/watchdog to the user. It plans
// nothing; anything wrong refuses, naming what the owner runs for the site
// called name.
func checkWatchdog(t Transport, name string, site config.Site) (Section, error) {
	var out Section
	if !site.Has(config.RoleData) {
		out.Present = append(out.Present, "watchdog: not needed, this site holds no data role")
		return out, nil
	}
	facts, err := probeWatchdog(t, true)
	if err != nil {
		return out, err
	}
	switch {
	case systemdHoldsWatchdog(facts.RuntimeWatchdog):
		return out, fmt.Errorf("watchdog: systemd's RuntimeWatchdogUSec is %s on %s, so PID 1 holds /dev/watchdog and Patroni cannot open it. The host's owner sets RuntimeWatchdogSec=0 in /etc/systemd/system.conf and runs systemctl daemon-reexec", facts.RuntimeWatchdog, t.Describe())
	case facts.Daemon:
		return out, fmt.Errorf("watchdog: a watchdog daemon is running on %s and holds /dev/watchdog. The host's owner stops and disables it (systemctl disable --now watchdog)", t.Describe())
	case !facts.DeviceNode || !facts.mine():
		return out, fmt.Errorf("watchdog: /dev/watchdog on %s is not the deployment's user's, so Patroni cannot open it. %s", t.Describe(), admitWatchdog(name))
	}
	hardware := facts.hardware()
	switch site.WatchdogMode() {
	case config.WatchdogRequired:
		if len(hardware) == 0 {
			return out, fmt.Errorf("watchdog: required, and %s has only softdog. required accepts a hardware or hypervisor watchdog and nothing else: the host's owner enables one in the firmware or the VM's settings, or declare watchdog: auto to accept softdog", t.Describe())
		}
	case config.WatchdogSoftdog:
		if len(hardware) > 0 {
			out.Warnings = append(out.Warnings, Warning{Hint: "watchdog: softdog declared, but a hardware watchdog exists", Detail: fmt.Sprintf("%s provides /dev/watchdog and Patroni opens that one, not softdog. Declare auto to say so.", hardware[0].Identity)})
		}
	default:
		if len(hardware) == 0 {
			out.Warnings = append(out.Warnings, Warning{Hint: "watchdog: softdog", Detail: fmt.Sprintf(softdogWarning, "host admit")})
		}
	}
	out.Present = append(out.Present, "watchdog: "+facts.describe()+", the deployment's user's")
	return out, nil
}

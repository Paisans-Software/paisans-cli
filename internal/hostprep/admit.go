// What host admit reuses from host prepare, exported.
package hostprep

import (
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// PlanWatchdog is what host prepare plans for a data site's watchdog in
// mode auto: the device the host has, or softdog with its warning, after
// refusing a host where systemd or a watchdog daemon already holds it.
// Admit plans it whatever the site's mode, which a request does not carry;
// host prepare and doctor compare the mode with what admit found.
func PlanWatchdog(t Transport, d deployment.Deployment, profile Profile) (Section, error) {
	return planWatchdog(t, d, profile, config.Site{Roles: []config.Role{config.RoleData}, Watchdog: config.WatchdogAuto})
}

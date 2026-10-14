package doctor

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
)

// SettingsProbe is what one app's container on one site said when asked for
// its gate settings, or why it could not be asked.
type SettingsProbe struct {
	Site, App string
	Out       string
	Err       string
	// NotRunning is set when the app has no running container on the site,
	// which the containers section reports; this one only skips it.
	NotRunning bool
}

// NotRunningMarker is what SettingsCommand prints, and exits non zero with,
// when the app has no running container there, so the caller can tell that
// apart from a read that failed.
const NotRunningMarker = "paisans: no running container"

// SettingsCommand runs a kind's read command inside the running container of
// one of an app's compose services on a site. The container is found by its
// compose labels, never by name, as ContainersCommand finds this deployment's.
func SettingsCommand(d deployment.Deployment, app, service string, argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return fmt.Sprintf(`c=$(docker ps -q --filter %s --filter %s --filter %s | head -n 1); [ -n "$c" ] || { echo %s; exit 3; }; docker exec "$c" %s`,
		shellQuote("label=com.docker.compose.project="+d.Project(app)),
		shellQuote("label=com.docker.compose.service="+service),
		shellQuote(d.LabelFilter()),
		shellQuote(NotRunningMarker), strings.Join(quoted, " "))
}

// Settings checks, for every app using the gate, that each running copy
// enforces the settings the gate leaves to it, and that its file locks them.
// What each kind needs, and how to read it, is the kind's own record
// (blueprints.GateSettingsFor, blueprints.LiveSettingsFor); nothing here knows
// one kind from another. A wrong or unlocked setting is a file on the host that
// is wrong or an app not restarted since it changed, so the fix is apply, which
// renders the file and recreates the stack; nothing here writes an app's
// database. See docs/specs/2026-10-12-gate-settings.md.
func Settings(cfg *config.Config, probes []SettingsProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		app := cfg.Apps[p.App]
		wanted := blueprints.GateSettingsFor(app)
		live, ok := blueprints.LiveSettingsFor(app.Blueprint)
		if len(wanted) == 0 || !ok {
			continue
		}
		where := fmt.Sprintf("%s on %s", p.App, p.Site)
		if p.NotRunning {
			out = append(out, Finding{Section: SectionSettings, Level: Skip, Line: where + ": not running, so its gate settings could not be read"})
			continue
		}
		if p.Err != "" {
			out = append(out, Finding{Section: SectionSettings, Level: Skip, Line: where + ": its gate settings could not be read", More: []string{p.Err}})
			continue
		}
		names := make([]string, len(wanted))
		for i, s := range wanted {
			names[i] = s.Name
		}
		got, err := live.Parse(p.Out, names)
		if err != nil {
			out = append(out, Finding{Section: SectionSettings, Level: Skip, Line: where + ": its gate settings could not be read", More: []string{err.Error()}})
			continue
		}
		var wrong []string
		for _, s := range wanted {
			l, ok := got[s.Name]
			switch {
			case !ok:
				wrong = append(wrong, fmt.Sprintf("%s is not set, and must be %s: %s.", s.Name, s.Want, s.Why))
			case !strings.EqualFold(l.Value, s.Want):
				wrong = append(wrong, fmt.Sprintf("%s is %s, and must be %s: %s.", s.Name, l.Value, s.Want, s.Why))
			case !l.Locked:
				wrong = append(wrong, fmt.Sprintf("%s is %s but not locked, so the app's own admin page can change it.", s.Name, l.Value))
			}
		}
		if len(wrong) == 0 {
			out = append(out, Finding{Section: SectionSettings, Level: OK, Line: fmt.Sprintf("%s: %s enforced and locked", where, strings.Join(names, ", "))})
			continue
		}
		more := append(wrong,
			fmt.Sprintf("Each is written and locked by the file the toolkit renders for %s. `paisans apply --site %s` renders it and recreates the stack; if the setting is still unlocked afterwards, the image predates the lock and needs a newer one (apps.%s.images).", p.App, p.Site, p.App))
		out = append(out, Finding{Section: SectionSettings, Level: Fail, Line: fmt.Sprintf("%s: %d gate setting(s) not enforced", where, len(wrong)), More: more})
	}
	return out
}

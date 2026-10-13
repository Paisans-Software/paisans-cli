package validate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// The rules a blueprint brings, and the ones about what a blueprint leaves
// out (docs/specs/2026-10-12-blueprints-and-catalogs.md, "What reads a
// blueprint"). The built in blueprints declare no checks or settings of
// their own, since validate's code checks them, so these rules fire for
// blueprints from a catalog.
func (c *checker) blueprintRules() {
	backups := c.cfg.Backup != nil && c.cfg.Backup.On()
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		kind := app.Kind
		prefix := "apps." + name

		// Each of the blueprint's own checks, by its own id.
		for _, check := range kinds.ChecksFor(kind) {
			if check.Holds(c.cfg, name) {
				continue
			}
			key := prefix
			if check.Key != "" {
				key += "." + check.Key
			}
			// The rule is the blueprint's own id, so it is not a literal
			// here as every other rule's is.
			level := Warn
			if check.Refuse {
				level = Refuse
			}
			c.findings = append(c.findings, Finding{level, check.ID, key, "a check of the app's blueprint fails", fmt.Sprintf("%s (a check of the %s blueprint).", check.Message, kind)})
		}

		// A blueprint with no health route is still watched, less closely.
		if h := kinds.HealthOf(kind); h.Path == "" && !h.TCP {
			c.warn("blueprint-without-health", prefix+".blueprint",
				"the blueprint declares no health route",
				"is %s, which declares no health route, so the monitor checks only that %s answers: a TCP connect to its port and its front page below 500. An app up but broken, answering / while its database is down, goes unnoticed. Ask the blueprint's author for one.", kind, name)
		}

		// Its data must reach the backups the operator believes it does.
		if backups && app.BacksUp() && kinds.NeedsDump(kind) && kinds.BackupOf(kind) == nil {
			c.refuse("blueprint-without-backup", prefix+".blueprint",
				"the blueprint keeps a database the backups cannot read",
				"is %s, which keeps its data in %s and says nothing of how to dump it, so this deployment's backups would leave %s's database out while it believes they hold it. Use a release of the blueprint that declares backup, or set apps.%s.backup: false to say so.", kind, kinds.EngineOf(kind), name, name)
		}

		// What the gate leaves to the app, and nothing it enforces.
		if app.Gated {
			if open := kinds.GateFor(kind).LeavesOpen; len(open) > 0 && len(kinds.GateSettingsFor(app)) == 0 {
				c.warn("gated-leaves-open", prefix+".gated",
					"the gate leaves this app something its blueprint does not close",
					"is true, and the %s blueprint says the gate passes %s through to the app, with no setting it enforces to close it. Members' content is then only as private as the app keeps it on its own.", kind, strings.Join(open, " and "))
			}
		}

		// Settings the blueprint declares, checked by type; an unknown one
		// is most likely a typo nothing reads.
		if specs := kinds.SettingsOf(kind); specs != nil {
			keys := make([]string, 0, len(app.Settings))
			for k := range app.Settings {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				spec, ok := specs[k]
				if !ok {
					c.warn("blueprint-setting-unknown", fmt.Sprintf("%s.settings.%s", prefix, k),
						"the blueprint reads no such setting",
						"is not a setting the %s blueprint reads, so nothing reads it.", kind)
					continue
				}
				if problem := spec.Problem(app.Settings[k]); problem != "" {
					c.refuse("blueprint-setting-invalid", fmt.Sprintf("%s.settings.%s", prefix, k),
						"the setting is not of its type",
						"%s, and the %s blueprint reads it as %s.", problem, kind, spec.Type)
				}
			}
		}
	}
}

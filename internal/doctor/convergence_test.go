package doctor

import (
	"strings"
	"testing"
)

// A published version is not a deployed one: apply publishes before it
// changes a host, so a site that stopped part way holds the newest version
// and runs an older one. Each case is what doctor says of one site.
func TestConvergenceTellsPublishedFromDeployed(t *testing.T) {
	for _, c := range []struct {
		name  string
		probe ConvergenceProbe
		level Level
		line  string
	}{
		{"converged", ConvergenceProbe{Site: "home-a", Applied: 16, Newest: 16}, OK, "home-a: applied revision 16, the newest"},
		{"behind", ConvergenceProbe{Site: "home-a", Applied: 15, Newest: 16}, Warn, "home-a: applied revision 15; revision 16 is published but not applied here"},
		{"owes", ConvergenceProbe{Site: "home-a", Applied: 15, Newest: 16, Owed: "chat"}, Warn, "home-a: an apply stopped part way and still owes chat"},
		{"never", ConvergenceProbe{Site: "home-a", Newest: 16}, Warn, "home-a: no apply has finished here"},
		{"no newest known", ConvergenceProbe{Site: "home-a", Applied: 12}, OK, "home-a: applied revision 12"},
		{"unreadable", ConvergenceProbe{Site: "home-a", Err: "sudo: a password is required"}, Skip, "home-a: what was applied could not be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := Convergence([]ConvergenceProbe{c.probe})
			if len(fs) != 1 || fs[0].Level != c.level || fs[0].Line != c.line || fs[0].Section != SectionConvergence {
				t.Fatalf("got %+v, want one %v finding %q", fs, c.level, c.line)
			}
			if c.level == Warn && !strings.Contains(strings.Join(fs[0].More, "\n"), "deployment apply --execute") {
				t.Errorf("a site behind names no recovery: %+v", fs[0].More)
			}
		})
	}
}

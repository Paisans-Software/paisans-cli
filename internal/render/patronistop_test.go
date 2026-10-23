package render_test

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// A data site's Patroni has 60 s to stop. Docker kills a container that
// outlasts its stop timeout, 10 s by default, and a Patroni killed before
// its Postgres reaches the shutdown's safepoint leaves the host's watchdog
// armed, so the host reboots.
func TestPatroniHasTimeToStop(t *testing.T) {
	plan := build(t)
	content := map[string]string{}
	for _, f := range plan.Files {
		content[f.Path] = f.Content
	}
	for _, site := range []string{"home-a", "home-b"} {
		var compose struct {
			Services map[string]struct {
				StopGracePeriod string `yaml:"stop_grace_period"`
			}
		}
		if err := yaml.Unmarshal([]byte(content[site+"/srv/paisans/f2a9/infra/compose.yaml"]), &compose); err != nil {
			t.Fatalf("%s: %v", site, err)
		}
		patroni, ok := compose.Services["patroni"]
		if !ok {
			t.Fatalf("%s: no patroni service", site)
		}
		if patroni.StopGracePeriod != "60s" {
			t.Errorf("%s: patroni's stop_grace_period is %q, not 60s", site, patroni.StopGracePeriod)
		}
	}
}

package main

import (
	"io"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// site prune takes exactly one site, and deployment prune none.
func TestPruneArguments(t *testing.T) {
	for name, err := range map[string]error{
		"no site":    runSitePrune([]string{"--config", fixtureConfig()}, strings.NewReader(""), io.Discard),
		"two sites":  runSitePrune([]string{"home-a", "vm", "--config", fixtureConfig()}, strings.NewReader(""), io.Discard),
		"deployment": runDeploymentPrune([]string{"home-a", "--config", fixtureConfig()}, strings.NewReader(""), io.Discard),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	err := runSitePrune([]string{"nowhere", "--config", fixtureConfig()}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), `declares no site "nowhere"`) {
		t.Errorf("an undeclared site: %v", err)
	}
}

// The plan marks each service it removes as pending and warns about each it
// keeps, saying why.
func TestSitePruneShowsWhatItRemovesAndKeeps(t *testing.T) {
	rec := &ui.Recorder{Verbose_: true}
	showServicePrune(rec, []ownership.Service{
		{Stack: "infra", Project: "paisans-f2a9-infra", Name: "haproxy", Containers: []string{"paisans-f2a9-infra-haproxy-1"}, Running: 1},
		{Stack: "infra", Project: "paisans-f2a9-infra", Name: "patroni", Containers: []string{"paisans-f2a9-infra-patroni-1"}, Running: 1, Kept: "a member of the Patroni cluster"},
	})
	if !rec.Has("pending", "remove service haproxy of stack infra") ||
		!warned(rec, "keep service patroni of stack infra", "It is a member of the Patroni cluster.") ||
		rec.Has("step", "remove service patroni") {
		t.Errorf("plan:\n%s", rec.Lines())
	}
	rec = &ui.Recorder{Verbose_: true}
	showServicePrune(rec, nil)
	if !rec.Has("detail", "no left over services") {
		t.Errorf("an empty plan:\n%s", rec.Lines())
	}
}

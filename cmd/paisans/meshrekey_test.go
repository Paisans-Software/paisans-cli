package main

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// A command line that cannot be run reaches no host.
func TestMeshRekeyRefusesBeforeReachingAHost(t *testing.T) {
	saved := rekeyHost
	rekeyHost = func(string, config.Site, bool) apply.Transport {
		t.Fatal("a host was reached")
		return nil
	}
	t.Cleanup(func() { rekeyHost = saved })
	for args, want := range map[string]string{
		"":              "mesh rekey needs a site",
		"home-a --all":  "--all takes no site",
		"--all home-a":  "--all takes no site",
		"nowhere":       "declares no site nowhere",
		"home-a nobody": "declares no site nobody",
	} {
		err := runMeshRekey(append(strings.Fields(args), "--config", fixtureConfig()))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", args, err, want)
		}
	}
	if err := runMesh([]string{"spin"}); err == nil || !strings.Contains(err.Error(), "rekey") {
		t.Errorf("an unknown subcommand: %v", err)
	}
}

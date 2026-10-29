package main

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// appliedByNewer makes each site in newer read as last applied by that
// release of paisans, and every other site as applied by this one, and
// records each site read.
func appliedByNewer(t *testing.T, newer map[string]string) *[]string {
	t.Helper()
	var read []string
	saved := toolkitCheck
	toolkitCheck = func(site string, _ apply.Transport, _ deployment.Deployment) error {
		read = append(read, site)
		if v, ok := newer[site]; ok {
			return &apply.NewerToolkitError{Site: site, Build: version.Build{Version: v, Commit: strings.Repeat("a", 40)}}
		}
		return nil
	}
	t.Cleanup(func() { toolkitCheck = saved })
	return &read
}

// An apply finding sites a newer paisans last applied is refused before its
// lease, its review or any step, naming every such site and the newest
// release, which is the one to upgrade to. A dry run is refused the same
// way, since its plan would be this release's reading of those hosts.
func TestApplyIsRefusedWhenANewerPaisansLastAppliedASite(t *testing.T) {
	cfg := fixtureCfg(t)
	sites := cfg.SiteNames()
	if len(sites) < 2 {
		t.Fatalf("the fixture declares %v, and the test needs two sites", sites)
	}
	for _, extra := range [][]string{nil, {"--execute"}} {
		ran := fakeConverge(t, nil)
		takes := heldBy(t, "bob@desk")
		read := appliedByNewer(t, map[string]string{sites[0]: "99.0.0", sites[1]: "99.1.0"})
		err := converge(t, extra...)
		text := problemText(err)
		if err == nil || !strings.Contains(text, "a newer paisans last applied this deployment") {
			t.Fatalf("%v: err = %v", extra, err)
		}
		for _, want := range []string{sites[0] + ": paisans 99.0.0", sites[1] + ": paisans 99.1.0", "Upgrade paisans to 99.1.0 or later", "Nothing was changed"} {
			if !strings.Contains(text, want) {
				t.Errorf("%v: the refusal does not say %q:\n%s", extra, want, text)
			}
		}
		if len(*read) < 2 {
			t.Errorf("%v: read only %v, so the refusal cannot name every site", extra, *read)
		}
		if len(*ran) != 0 {
			t.Errorf("%v: a refused apply ran %v", extra, *ran)
		}
		if len(*takes) != 0 {
			t.Errorf("%v: a refused apply took the lease", extra)
		}
	}
}

// Sites this release or an older one last applied pass, and the run goes on.
func TestApplyGoesOnWhenNoNewerPaisansAppliedASite(t *testing.T) {
	ran := fakeConverge(t, nil)
	read := appliedByNewer(t, nil)
	if err := converge(t, "--execute"); err != nil {
		t.Fatal(problemText(err))
	}
	if len(*read) == 0 {
		t.Error("the run read no site's manifest")
	}
	if len(*ran) == 0 {
		t.Error("the run ran no step")
	}
}

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// toolkitCheck reads one site's manifest for refuseNewerToolkit. Tests
// replace it.
var toolkitCheck = apply.CheckToolkit

// refuseNewerToolkit reads each of sites' manifests before an apply reaches
// any of them for anything else, and refuses the apply when a newer release
// of paisans last applied one or more: that release may put things on a host
// this one does not know about, and this one would write over them. Every
// site is read, so the refusal names each such site and the release to
// upgrade to, and none is changed. Execute reads each manifest again before
// it writes, for an apply by a newer paisans in between.
func refuseNewerToolkit(r ui.Reporter, cfg *config.Config, sites []string, host func(name string) apply.Transport) error {
	line := r.Step("check which paisans last applied each site")
	var newer []*apply.NewerToolkitError
	for _, name := range sites {
		err := toolkitCheck(name, host(name), cfg.Deployment())
		var n *apply.NewerToolkitError
		switch {
		case errors.As(err, &n):
			newer = append(newer, n)
		case err != nil:
			line.Fail(err)
			return err
		}
	}
	if len(newer) == 0 {
		line.Done("none by a newer paisans than " + version.Version)
		return nil
	}
	var lines []string
	want := newer[0].Build.Version
	for _, n := range newer {
		lines = append(lines, fmt.Sprintf("- %s: paisans %s", n.Site, n.Build))
		if c, err := version.Compare(n.Build.Version, want); err == nil && c > 0 {
			want = n.Build.Version
		}
	}
	line.End(ui.Failed, fmt.Sprintf("%s last applied by a newer paisans", plural(len(newer), "site")))
	return &ui.Problem{
		Hint: "a newer paisans last applied this deployment",
		Explain: strings.Join(lines, "\n") + fmt.Sprintf("\nThis is paisans %s, which may not know everything a newer release put on those hosts, and applying would write over it. Nothing was changed. Upgrade paisans to %s or later, and run %s deployment apply again.",
			version.Version, want, progname.Cmd()),
	}
}

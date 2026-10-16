package hostadmit_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The grant a site's roles need is checked before anything reaches the
// host: each stop's text is pinned, and nothing about the host is needed to
// reach it. A grant the site does not need is refused the same way (ruling
// 4), as is a --data-dir that is not an absolute, clean path.
func TestEarlyStops(t *testing.T) {
	for _, c := range []struct {
		name  string
		site  string
		flags hostadmit.Flags
		stops bool
	}{
		{"data-without-watchdog", "home-b", hostadmit.Flags{}, true},
		{"gateway-without-public-ports", "home-a", hostadmit.Flags{Watchdog: true}, true},
		{"gateway-and-data-without-either", "home-a", hostadmit.Flags{}, true},
		{"caddy-monitor-without-public-ports", "watch", hostadmit.Flags{}, true},
		{"external-monitor-never-stops", "status", hostadmit.Flags{}, false},
		{"apps-only-never-stops", "store", hostadmit.Flags{}, false},
		{"witness-never-stops", "vm", hostadmit.Flags{}, false},
		{"watchdog-not-needed", "vm", hostadmit.Flags{Watchdog: true}, true},
		{"public-ports-not-needed", "status", hostadmit.Flags{PublicPorts: true}, true},
		{"data-dir-relative", "vm", hostadmit.Flags{DataDir: "srv/data"}, true},
		{"data-dir-unclean", "vm", hostadmit.Flags{DataDir: "/srv/../srv/data/"}, true},
		{"data-dir-root", "vm", hostadmit.Flags{DataDir: "/"}, true},
		{"data-dir-control-character", "vm", hostadmit.Flags{DataDir: "/srv/da\x1bta"}, true},
		{"data-dir-plain", "vm", hostadmit.Flags{DataDir: "/srv/data"}, false},
		{"both-grants-given", "home-a", hostadmit.Flags{Watchdog: true, PublicPorts: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := hostadmit.Early(input(t, c.site, c.flags))
			if (err != nil) != c.stops {
				t.Fatalf("Early = %v, want a stop: %v", err, c.stops)
			}
			if err == nil {
				return
			}
			var problem *ui.Problem
			if !errors.As(err, &problem) || len(problem.Hint) > 80 || strings.Contains(problem.Hint, "\n") {
				t.Errorf("not a Problem with a one-line hint of at most 80 characters: %q", err)
			}
			path := filepath.Join("testdata", "stops", c.name+".txt")
			if *update {
				os.MkdirAll(filepath.Dir(path), 0o755)
				os.WriteFile(path, []byte(err.Error()+"\n"), 0o644)
				return
			}
			want, rerr := os.ReadFile(path)
			if rerr != nil || string(want) != err.Error()+"\n" {
				t.Errorf("%s: got\n%s\nwant\n%s", path, err, want)
			}
		})
	}
}

// Build stops the same way, before its first read of the host, so a
// caller that skipped Early still changes and reads nothing.
func TestBuildStopsBeforeReadingTheHost(t *testing.T) {
	h := newBlank()
	if _, err := hostadmit.Build(h, input(t, "home-b", hostadmit.Flags{})); err == nil {
		t.Fatal("a data site without --watchdog was planned")
	}
	if len(h.ran) != 0 {
		t.Errorf("Build ran %q before stopping", h.ran)
	}
}

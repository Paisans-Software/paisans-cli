package blueprints_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// Mbin's tile starts the fork's OIDC flow; every other blueprint opens its
// front page, which is the bare host with no trailing slash.
func TestLaunchURLDefaultsPerBlueprint(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		want := "https://app.example.org"
		if blueprint == config.Blueprint("mbin") {
			want = "https://app.example.org/oauth/oidc/connect"
		}
		if got := blueprints.LaunchURL(blueprint, "app.example.org", ""); got != want {
			t.Errorf("%s: LaunchURL = %q, want %q", blueprint, got, want)
		}
	}
}

func TestADashboardLinkReplacesTheBlueprintsPath(t *testing.T) {
	cases := []struct {
		blueprint config.Blueprint
		link      string
		want      string
	}{
		{config.Blueprint("mbin"), "/magazines", "https://app.example.org/magazines"},
		{config.Blueprint("mbin"), "/", "https://app.example.org"},
		{config.Blueprint("wikijs"), "/home", "https://app.example.org/home"},
	}
	for _, tc := range cases {
		if got := blueprints.LaunchURL(tc.blueprint, "app.example.org", tc.link); got != tc.want {
			t.Errorf("%s %q: got %q, want %q", tc.blueprint, tc.link, got, tc.want)
		}
	}
}

// The bare host is in every blueprint's list, because it is what every client
// made before blueprints named a path was given.
func TestToolkitLaunchURLs(t *testing.T) {
	got := fmt.Sprint(blueprints.ToolkitLaunchURLs(config.Blueprint("mbin"), "app.example.org"))
	if got != "[https://app.example.org https://app.example.org/oauth/oidc/connect]" {
		t.Errorf("mbin: %s", got)
	}
	got = fmt.Sprint(blueprints.ToolkitLaunchURLs(config.Blueprint("wikijs"), "app.example.org"))
	if got != "[https://app.example.org]" {
		t.Errorf("wikijs: %s", got)
	}
}

func TestCheckDashboardLink(t *testing.T) {
	for _, ok := range []string{"/", "/oauth/oidc/connect", "/login?next=%2Fm"} {
		if err := blueprints.CheckDashboardLink(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	refused := map[any]string{
		"https://app.example.org/x": "the host is always the app's own hostname",
		"oauth/oidc/connect":        "not a path",
		"":                          "not a path",
		"//other.example.org/x":     "reads as a host",
		"/a b":                      "whitespace",
		42:                          "not a string",
	}
	for v, want := range refused {
		err := blueprints.CheckDashboardLink(v)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, want it refused saying %q", v, err, want)
		}
	}
}

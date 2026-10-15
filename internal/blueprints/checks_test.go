package blueprints

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/version"
)

func TestAListSettingTakesNonEmptyStrings(t *testing.T) {
	s := SettingSpec{Type: "list"}
	if p := s.Problem([]any{"admins", "members"}); p != "" {
		t.Errorf("a list of names: %s", p)
	}
	for _, bad := range []any{"admins", []any{"admins", ""}, []any{"admins", 3}} {
		if s.Problem(bad) == "" {
			t.Errorf("%#v passed as a list of strings", bad)
		}
	}
}

// requires_toolkit is met by a version at least the one it names, and by no
// other: a build counts as the version its commit holds, with no exemption
// for a build between releases.
func TestMeets(t *testing.T) {
	for _, c := range []struct {
		req, have string
		ok        bool
	}{
		{"", "0.0.0", true},
		{">= 0.1", "0.1.0", true},
		{">= 0.1", "0.2.3", true},
		{">=0.9.1", "1.0.0", true},
		{">= 0.1", "0.0.0", false},
		{">= 0.1.1", "0.1.0", false},
		{">= 1.0", "0.9.9", false},
	} {
		err := meets(c.req, c.have)
		if (err == nil) != c.ok {
			t.Errorf("%q against %s: got %v, want met %v", c.req, c.have, err, c.ok)
		}
	}
	if err := requirement(">= " + version.Version); err != nil {
		t.Errorf("this build does not meet its own version: %v", err)
	}
}

package blueprints

import "testing"

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

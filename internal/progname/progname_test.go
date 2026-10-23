package progname

import "testing"

func TestSetTakesTheBaseOfArgv0(t *testing.T) {
	t.Cleanup(func() { Set(Default) })
	for _, c := range []struct{ argv0, want string }{
		{"/usr/local/bin/paisans-test", "paisans-test"},
		{"./bin/paisans-dev", "paisans-dev"},
		{"paisans", "paisans"},
		{"", Default},
		{"/", Default},
	} {
		Set(c.argv0)
		if got := Name(); got != c.want {
			t.Errorf("Set(%q): Name() = %q, want %q", c.argv0, got, c.want)
		}
	}
}

func TestNameIsTheDefaultUntilSet(t *testing.T) {
	if got := Name(); got != Default {
		t.Errorf("Name() = %q before Set, want %q", got, Default)
	}
}

func TestCmdQuotesANameAShellWouldSplit(t *testing.T) {
	t.Cleanup(func() { Set(Default) })
	for _, c := range []struct{ argv0, want string }{
		{"/opt/bin/paisans-test", "paisans-test"},
		{"/opt/bin/paisans_1.2", "paisans_1.2"},
		{"/opt/my bin/paisans test", "'paisans test'"},
		{"/opt/bin/it's", `'it'\''s'`},
		{"/opt/bin/p%s", "'p%s'"},
	} {
		Set(c.argv0)
		if got := Cmd(); got != c.want {
			t.Errorf("Set(%q): Cmd() = %q, want %q", c.argv0, got, c.want)
		}
	}
}

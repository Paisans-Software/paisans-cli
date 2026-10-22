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

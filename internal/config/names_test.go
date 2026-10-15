package config_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Each exported check is the rule the loader holds its field to, so a
// request read without a configuration is held to the same.
func TestNameChecks(t *testing.T) {
	for _, c := range []struct {
		check func(string) bool
		name  string
		good  []string
		bad   []string
	}{
		{config.ValidUserName, "user", []string{"paisans-f2a9", "ubuntu", "_svc"}, []string{"", "Root", "1abc", "a b", "a;b", "abcdefghijklmnopqrstuvwxyzabcdefg"}},
		{config.ValidOperatorName, "operator", []string{"alice", "0day", "a.b_c-d"}, []string{"", "Alice", "-a", "a b", "a/b"}},
		{config.ValidAppName, "app", []string{"talk", "status-2"}, []string{"", "Talk", "a.b", "a_b", "-a"}},
		{config.ValidHostname, "hostname", []string{"status.example.org"}, []string{"", "a b.example.org", "a;b", "-a.example.org", "a..example.org", strings.Repeat("a", 64) + ".org"}},
	} {
		for _, s := range c.good {
			if !c.check(s) {
				t.Errorf("%s %q refused", c.name, s)
			}
		}
		for _, s := range c.bad {
			if c.check(s) {
				t.Errorf("%s %q accepted", c.name, s)
			}
		}
	}
}

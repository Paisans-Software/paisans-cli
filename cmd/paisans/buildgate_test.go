package main

import "testing"

// A command writes a record with --execute in any spelling the flag
// package takes, and render always does; everything else only reads.
func TestWritesRecords(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"deployment", "apply", "--execute"}, true},
		{[]string{"site", "admit", "-execute"}, true},
		{[]string{"config", "publish", "--execute=true"}, true},
		{[]string{"deployment", "apply", "--execute=false"}, false},
		{[]string{"render", "--out", "x"}, true},
		{[]string{"deployment", "apply"}, false},
		{[]string{"doctor"}, false},
		{[]string{"version"}, false},
		{[]string{"validate", "--config", "--execute"}, true},
		{[]string{"deployment", "apply", "--", "--execute"}, false},
	} {
		if got := writesRecords(c.args); got != c.want {
			t.Errorf("%v: %v, want %v", c.args, got, c.want)
		}
	}
	if err := buildGate([]string{"deployment", "apply", "--execute"}); err != nil {
		t.Errorf("a test build, which names its commit, was refused: %v", err)
	}
}

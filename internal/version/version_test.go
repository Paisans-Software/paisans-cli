package version

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"0.2.0", "0.3.0", -1},
		{"0.10.0", "0.9.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.2.1", "0.2.0", 1},
	} {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%s, %s) = %d, %v, want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "0.2", "v0.2.0", "0.2.0 (519299d5cb00)", "0.2.x", "0.-1.0", "0.02.0", "0.2.0.1", "+1.0.0", "1.2.3-rc1", "1.2.3 "} {
		if _, err := Compare(bad, "0.2.0"); err == nil {
			t.Errorf("Compare(%q) ordered it", bad)
		}
	}
}

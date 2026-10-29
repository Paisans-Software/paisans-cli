package blueprints

import (
	"strings"
	"testing"
)

// foundationWithoutBeszel is the test foundation as a core catalog released
// before the beszel tracks has it: neither key is named.
func foundationWithoutBeszel(t *testing.T) []byte {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(testFoundation(t), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, FoundationBeszel+":") && !strings.HasPrefix(trimmed, FoundationBeszelAgent+":") {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

// The beszel tracks are optional, as the engagement track is: a core catalog
// released before them still reads, so a deployment without the server
// monitor keeps following its newest release.
func TestFoundationBeszelOptional(t *testing.T) {
	raw := foundationWithoutBeszel(t)
	if strings.Contains(string(raw), FoundationBeszel+":") || strings.Contains(string(raw), FoundationBeszelAgent+":") {
		t.Fatal("the fixture still names a beszel image")
	}
	if _, err := ParseFoundation(raw); err != nil {
		t.Fatalf("a foundation without beszel is refused: %v", err)
	}
	if _, err := ParseFoundation([]byte(testFoundation(t))); err != nil {
		t.Fatalf("a foundation with beszel is refused: %v", err)
	}

	spec, err := ParseFoundation(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(forgetCore)
	setFoundation(spec)
	_, err = FoundationImage(FoundationBeszelAgent)
	want := "the core catalog's foundation.yaml has no beszel-agent image; it needs a catalog release that publishes one"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// Both beszel tracks stay on the 0.21 line the toolkit is written against: a
// hub and an agent move together, and Beszel is 0.x, so a minor may change
// what they say to each other.
func TestFoundationBeszelTags(t *testing.T) {
	for _, name := range []string{FoundationBeszel, FoundationBeszelAgent} {
		if err := CheckFoundationImage(name, "ghcr.io/henrygd/beszel/"+name+":0.21.*"); err != nil {
			t.Errorf("%s: 0.21.* refused: %v", name, err)
		}
		if err := CheckFoundationImage(name, "ghcr.io/henrygd/beszel/"+name+":0.22.*"); err == nil {
			t.Errorf("%s: 0.22.* accepted, want refused", name)
		}
	}
}

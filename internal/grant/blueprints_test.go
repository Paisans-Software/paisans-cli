package grant

import (
	"strings"
	"testing"
	"time"
)

// A catalog blueprint new to a host, moved to other files, or newly
// unchecked needs approval; one gone is shown; one unchanged is not in the
// review. Recording the grant makes the next review empty.
func TestCatalogBlueprintsAreReviewed(t *testing.T) {
	one := Blueprint{App: "notes", Blueprint: "ex/journal", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), SHA256: "s1"}
	g := Grant{Blueprints: []Blueprint{one}}
	r := Compare("vm", g, Record{}, ApplyScope())
	if len(r.Changes) != 1 || r.Changes[0].Op != Added || !strings.Contains(r.Changes[0].Text, "ex/journal at v1.0.0 (aaaaaaaaaaaa)") {
		t.Fatalf("first review %+v", r.Changes)
	}
	rec := Update(Record{}, g, ApplyScope(), time.Unix(1_800_000_000, 0), "an admin")
	if r := Compare("vm", g, rec, ApplyScope()); len(r.Changes) != 0 {
		t.Errorf("after recording: %+v", r.Changes)
	}

	moved := one
	moved.Tag, moved.Commit, moved.SHA256 = "v1.1.0", strings.Repeat("b", 40), "s2"
	r = Compare("vm", Grant{Blueprints: []Blueprint{moved}}, rec, ApplyScope())
	if len(r.Changes) != 1 || r.Changes[0].Op != Changed || !strings.Contains(r.Changes[0].Text, "→ ex/journal at v1.1.0") {
		t.Fatalf("a moved pin %+v", r.Changes)
	}
	if r.Pending() != 1 {
		t.Errorf("pending %d", r.Pending())
	}

	r = Compare("vm", Grant{}, rec, ApplyScope())
	if len(r.Changes) != 1 || r.Changes[0].Op != Removed || r.Pending() != 0 {
		t.Errorf("a removed app %+v", r.Changes)
	}
	// apply --only narrows the review to its stacks.
	if r := Compare("vm", Grant{Blueprints: []Blueprint{moved}}, rec, ApplyScope("talk")); len(r.Changes) != 0 {
		t.Errorf("--only talk reviewed notes: %+v", r.Changes)
	}
}

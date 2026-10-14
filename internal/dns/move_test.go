package dns

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const (
	oldGW  = "198.51.100.1"
	oldGW6 = "2001:db8::1"
)

// moved is declared() just after the gateway role moved from home-a, which
// held it with these public addresses, to vm.
func moved() *config.Config {
	cfg := declared()
	a := cfg.Sites["home-a"]
	a.PublicAddress, a.PublicAddress6 = oldGW, oldGW6
	cfg.Sites["home-a"] = a
	return cfg
}

func TestDesiredMarksTheGatewaysHostnames(t *testing.T) {
	wants, err := Desired(moved())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wants {
		if w.Type != "A" {
			continue
		}
		gateway := w.Name == "talk.example.org" || w.Name == "blog.example.org" || strings.HasSuffix(w.Name, "-media.example.org")
		if w.ViaGateway != gateway {
			t.Errorf("%s: ViaGateway %t", w.Name, w.ViaGateway)
		}
		if gateway && !slices.Equal(w.Former, []string{oldGW, oldGW6}) {
			t.Errorf("%s: Former %v", w.Name, w.Former)
		}
	}
}

// A hostname still pointing at the gateway before this one, through a record
// this deployment's dns init made, waits for the move: neither a conflict
// that stops apply nor a record dns init creates.
func TestInitLeavesAMovingNameToTheMove(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "old-talk", Type: "A", Name: "talk.example.org", Content: oldGW, Comment: recordComment},
		{ID: "old-talk6", Type: "AAAA", Name: "talk.example.org", Content: oldGW6, Comment: recordComment},
		{ID: "hand-blog", Type: "A", Name: "blog.example.org", Content: oldGW},
	}
	c := fake.serve(t)
	p := plan(t, c, moved())
	actions := map[string]Action{}
	for _, e := range addressEntries(p.Entries) {
		actions[e.Type+" "+e.Name] = e.Action
	}
	if actions["A talk.example.org"] != Moving {
		t.Errorf("talk: %s, want moving", actions["A talk.example.org"])
	}
	if actions["A blog.example.org"] != Conflict {
		t.Errorf("a record dns init did not make is still a conflict: %s", actions["A blog.example.org"])
	}
	rec := &ui.Recorder{}
	p.Show(rec)
	if !strings.Contains(rec.Lines(), "A talk.example.org waits for paisans gateway move") {
		t.Errorf("got:\n%s", rec.Lines())
	}
	for _, e := range addressEntries(p.Creates()) {
		if e.Name == "talk.example.org" {
			t.Errorf("dns init would create %s %s itself", e.Type, e.Name)
		}
	}
}

func TestTheMoveDeletesOnlyTheOldGatewaysOwnRecordsAtGatewayHostnames(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "old-talk", Type: "A", Name: "talk.example.org", Content: oldGW, Comment: recordComment, TTL: 1},
		// vm declares no IPv6, so home-a's AAAA goes too, or IPv6 clients
		// would go on reaching a gateway about to stop.
		{ID: "old-talk6", Type: "AAAA", Name: "talk.example.org", Content: oldGW6, Comment: recordComment, TTL: 600},
		{ID: "new-talk", Type: "A", Name: "talk.example.org", Content: "203.0.113.10", Comment: recordComment},
		{ID: "old-blog", Type: "A", Name: "blog.example.org", Content: oldGW, Comment: recordComment, TTL: 1},
		// Not a gateway hostname: a site endpoint, and a name the
		// configuration no longer has.
		{ID: "endpoint", Type: "A", Name: "vm.example.org", Content: oldGW, Comment: recordComment},
		{ID: "gone", Type: "A", Name: "gone.example.org", Content: oldGW, Comment: recordComment},
		// Not this deployment's, or not as dns init makes them.
		{ID: "proxied", Type: "A", Name: "blog.example.org", Content: oldGW, Comment: recordComment, Proxied: true},
		{ID: "other", Type: "A", Name: "blog.example.org", Content: oldGW, Comment: "paisans-0c1d: created by paisans dns init"},
	}
	c := fake.serve(t)
	ctx := context.Background()
	p, err := BuildMove(ctx, c, moved(), "home-a")
	if err != nil {
		t.Fatal(err)
	}
	ids := func(es []PruneEntry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.ID)
		}
		sort.Strings(out)
		return out
	}
	if got := ids(p.Deletes); !slices.Equal(got, []string{"old-blog", "old-talk", "old-talk6"}) {
		t.Errorf("deletes %v", got)
	}
	if got := ids(p.Kept); !slices.Equal(got, []string{"other", "proxied"}) {
		t.Errorf("kept %v", got)
	}
	if p.TTL() != 600 {
		t.Errorf("TTL %d, want the longest, 600", p.TTL())
	}
	if p.Ready() {
		t.Errorf("blog.example.org has no record of vm's, so the plan is not ready")
	}

	// Nothing is deleted while vm's records are missing or a protected
	// record points at home-a.
	if err := ExecuteMoveRemove(ctx, c, p, ui.Discard); err == nil || len(fake.deletes) != 0 {
		t.Fatalf("deleted %v before the new records were there: %v", fake.deletes, err)
	}
	if err := ExecuteMoveAdd(ctx, c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	if p, err = BuildMove(ctx, c, moved(), "home-a"); err != nil || !p.Ready() {
		t.Fatalf("not ready after the add: %v", err)
	}
	if err := ExecuteMoveRemove(ctx, c, p, ui.Discard); err == nil || len(fake.deletes) != 0 {
		t.Fatalf("deleted %v while a protected record points at home-a: %v", fake.deletes, err)
	}
	fake.records["zone-1"] = slices.DeleteFunc(fake.records["zone-1"], func(r cfRecord) bool { return r.ID == "proxied" || r.ID == "other" })
	if p, err = BuildMove(ctx, c, moved(), "home-a"); err != nil {
		t.Fatal(err)
	}
	if err := ExecuteMoveRemove(ctx, c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	sort.Strings(fake.deletes)
	if !slices.Equal(fake.deletes, []string{"old-blog", "old-talk", "old-talk6"}) {
		t.Errorf("deleted %v", fake.deletes)
	}
	if p, err = BuildMove(ctx, c, moved(), "home-a"); err != nil || !p.Clear() {
		t.Errorf("records still point at home-a: %v", err)
	}
}

func TestFormerGatewaysAreTheSitesTheRecordsStillPointAt(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "old-talk", Type: "A", Name: "talk.example.org", Content: oldGW, Comment: recordComment},
	}
	got, err := FormerGateways(context.Background(), fake.serve(t), moved())
	if err != nil || !slices.Equal(got, []string{"home-a"}) {
		t.Errorf("got %v, %v", got, err)
	}
}

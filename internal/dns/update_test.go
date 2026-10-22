package dns

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The gateway's public address before it changed.
const staleGW = "198.51.100.7"

// A record dns init made for this deployment, at a name it still wants, that
// points at an address the gateway no longer has is updated in place: the
// dry run shows the update and writes nothing, --execute writes it and reads
// it back.
func TestOwnRecordAtAnOldAddressIsUpdated(t *testing.T) {
	cfg := declared()
	vm := cfg.Sites["vm"]
	vm.PublicAddress6 = "2001:db8::10"
	cfg.Sites["vm"] = vm

	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "own-talk", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment, TTL: 1},
		{ID: "own-talk6", Type: "AAAA", Name: "talk.example.org", Content: "2001:db8::99", Comment: recordComment, TTL: 1},
	}
	c := fake.serve(t)
	p := plan(t, c, cfg)

	actions := map[string]Action{}
	for _, e := range addressEntries(p.Entries) {
		actions[e.Type+" "+e.Name] = e.Action
	}
	if actions["A talk.example.org"] != Update || actions["AAAA talk.example.org"] != Update {
		t.Fatalf("own records at an old address should be updated: %v", actions)
	}
	if len(p.Conflicts()) != 0 || len(p.Updates()) != 2 {
		t.Fatalf("want 2 updates and no conflict, got %+v", p.Entries)
	}

	shown := &ui.Recorder{}
	p.Show(shown)
	t.Logf("dry run:\n%s", shown.Lines())
	if !shown.Has("pending", "update A talk.example.org -> 203.0.113.10 (zone example.org)") {
		t.Errorf("the update is not shown as pending:\n%s", shown.Lines())
	}
	if !strings.Contains(shown.Lines(), staleGW) {
		t.Errorf("the dry run should name the address being replaced:\n%s", shown.Lines())
	}
	if len(fake.patches) != 0 || len(fake.posts) != 0 {
		t.Fatalf("planning wrote: %d PATCH, %d POST", len(fake.patches), len(fake.posts))
	}

	run := &ui.Recorder{}
	if err := Execute(context.Background(), c, p, run); err != nil {
		t.Fatal(err)
	}
	if !run.Has("done", "update A talk.example.org") {
		t.Errorf("steps:\n%s", run.Lines())
	}
	if len(fake.patches) != 2 {
		t.Fatalf("want 2 PATCHes, got %+v", fake.patches)
	}
	for _, rec := range fake.patches {
		want := map[string]string{"own-talk": "203.0.113.10", "own-talk6": "2001:db8::10"}[rec.ID]
		if want == "" || rec.Content != want || rec.Proxied || rec.Comment != recordComment || rec.Name != "talk.example.org" {
			t.Errorf("unexpected update %+v", rec)
		}
	}
	again := plan(t, c, cfg)
	for _, e := range addressEntries(again.Entries) {
		if e.Name == "talk.example.org" && e.Action != Present {
			t.Errorf("after the update %s %s is %s", e.Type, e.Name, e.Action)
		}
	}
}

// A record that is not exactly this deployment's, as dns init makes them,
// stays a conflict, and nothing at all is written.
func TestARecordNotThisDeploymentsStillConflicts(t *testing.T) {
	cases := map[string][]cfRecord{
		"no comment": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW},
		},
		"another deployment's comment": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: "paisans-0c1d: created by paisans dns init"},
		},
		"this comment with more after it": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment + " (edited)"},
		},
		"this comment cut short": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: strings.TrimSuffix(recordComment, " init")},
		},
		"proxied": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment, Proxied: true},
		},
		"two of its own": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment},
			{ID: "y", Type: "A", Name: "talk.example.org", Content: "198.51.100.8", Comment: recordComment},
		},
		"its own beside someone else's": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment},
			{ID: "y", Type: "A", Name: "talk.example.org", Content: "198.51.100.8"},
		},
		"its own beside a stray AAAA": {
			{ID: "x", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment},
			{ID: "y", Type: "AAAA", Name: "talk.example.org", Content: "2001:db8::99"},
		},
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFake()
			fake.zones["example.org"] = "zone-1"
			fake.records["zone-1"] = existing
			c := fake.serve(t)
			p := plan(t, c, declared())
			if len(p.Updates()) != 0 {
				t.Fatalf("nothing should be updated, got %+v", p.Updates())
			}
			var talk *Entry
			for i, e := range p.Entries {
				if e.Type == "A" && e.Name == "talk.example.org" {
					talk = &p.Entries[i]
				}
			}
			if talk == nil || talk.Action != Conflict {
				t.Fatalf("A talk.example.org should be a conflict: %+v", talk)
			}
			err := Execute(context.Background(), c, p, ui.Discard)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("execute should refuse, got %v", err)
			}
			if len(fake.patches) != 0 || len(fake.posts) != 0 || len(fake.deletes) != 0 {
				t.Fatalf("a conflict must stop every write: %d PATCH, %d POST, %d DELETE", len(fake.patches), len(fake.posts), len(fake.deletes))
			}
		})
	}
}

// A conflict anywhere stops the updates as well as the creates.
func TestAConflictElsewhereStopsTheUpdate(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "own-talk", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment},
		{ID: "hand-blog", Type: "A", Name: "blog.example.org", Content: staleGW},
	}
	c := fake.serve(t)
	p := plan(t, c, declared())
	if len(p.Updates()) != 1 || len(p.Conflicts()) != 1 {
		t.Fatalf("want 1 update and 1 conflict, got %+v", p.Entries)
	}
	if err := Execute(context.Background(), c, p, ui.Discard); !errors.Is(err, ErrConflict) {
		t.Fatalf("execute should refuse, got %v", err)
	}
	if len(fake.patches) != 0 || len(fake.posts) != 0 {
		t.Fatalf("wrote %d PATCH, %d POST past a conflict", len(fake.patches), len(fake.posts))
	}
}

// An update that does not land is caught by the read-back.
func TestReadBackCatchesAnUpdateThatDidNotLand(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "own-talk", Type: "A", Name: "talk.example.org", Content: staleGW, Comment: recordComment},
	}
	fake.dropWrites = true
	c := fake.serve(t)
	p := plan(t, c, declared())
	err := Execute(context.Background(), c, p, ui.Discard)
	if err == nil || !strings.Contains(err.Error(), "A talk.example.org was updated but does not read back") {
		t.Fatalf("want the update's read-back failure, got %v", err)
	}
	if len(fake.patches) != 1 || len(fake.posts) != 0 {
		t.Fatalf("execute should stop at the first failure: %d PATCH, %d POST", len(fake.patches), len(fake.posts))
	}
}

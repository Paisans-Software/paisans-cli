package grant_test

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/grant"
)

// A record written before the configuration part existed is read as it is,
// and one with the part is read whole; either is written back as the
// current version.
func TestParseReadsVersionOneAndTwo(t *testing.T) {
	v1 := `{"version": 1, "images": {"applied_at": "x", "entries": []}}`
	rec, err := grant.Parse(v1)
	if err != nil || rec.Images == nil || rec.Config != nil {
		t.Fatalf("version 1: %+v %v", rec, err)
	}
	if !strings.Contains(grant.Encode(rec), `"version": 3`) {
		t.Errorf("not written as version 3: %s", grant.Encode(rec))
	}
	v2 := `{"version": 2, "config": {"hash": "abc", "revision": 7, "applied_at": "2026-10-12T10:00:00Z"}}`
	rec, err = grant.Parse(v2)
	if err != nil || rec.Config == nil || rec.Config.Hash != "abc" || rec.Config.Revision != 7 || !rec.Config.AppliedAt.Equal(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("version 2: %+v %v", rec.Config, err)
	}
	for _, bad := range []string{`{"version": 4}`, `{"version": 0}`, `{"version": 2, "config": {"hash": "a", "extra": 1}}`} {
		if _, err := grant.Parse(bad); err == nil {
			t.Errorf("read %s", bad)
		}
	}
}

// The version applied is recorded with when; the same version applied again
// keeps the time it was first recorded, so the record is not rewritten.
func TestWithConfigRecordsTheVersionApplied(t *testing.T) {
	at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	rec := grant.Record{Version: 1}.WithConfig("abc", 3, at)
	if rec.Config == nil || rec.Config.Hash != "abc" || rec.Config.Revision != 3 || !rec.Config.AppliedAt.Equal(at) {
		t.Fatalf("%+v", rec.Config)
	}
	again := rec.WithConfig("abc", 3, at.Add(time.Hour))
	if grant.Encode(again) != grant.Encode(rec) {
		t.Errorf("the same version changed the record: %s", grant.Encode(again))
	}
	next := rec.WithConfig("def", 4, at.Add(time.Hour))
	if next.Config.Hash != "def" || next.Config.Revision != 4 || !next.Config.AppliedAt.Equal(at.Add(time.Hour)) {
		t.Errorf("%+v", next.Config)
	}
}

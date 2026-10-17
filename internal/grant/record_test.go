package grant_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// build is the build keys of a record, after a comma.
const build = `, "toolkit_version": "0.3.0", "toolkit_commit": "519299d5cb009da4904cdf53c2025f1ad609f102", "toolkit_modified": true`

// A record of the current layout is read whole, and one of any other
// layout is refused.
func TestParseReadsTheCurrentLayoutOnly(t *testing.T) {
	v3 := `{"version": 3, "config": {"hash": "abc", "revision": 7, "applied_at": "2026-10-12T10:00:00Z"}` + build + `}`
	rec, err := grant.Parse(v3)
	if err != nil || rec.Config == nil || rec.Config.Hash != "abc" || rec.Config.Revision != 7 || !rec.Config.AppliedAt.Equal(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("version 3: %+v %v", rec.Config, err)
	}
	for _, bad := range []string{
		`{"version": 1, "images": {"applied_at": "x", "entries": []}` + build + `}`,
		`{"version": 2, "config": {"hash": "abc", "revision": 7, "applied_at": "2026-10-12T10:00:00Z"}` + build + `}`,
		`{"version": 4}`, `{"version": 0}`,
		`{"version": 3, "config": {"hash": "a", "extra": 1}` + build + `}`,
	} {
		if _, err := grant.Parse(bad); err == nil {
			t.Errorf("read %s", bad)
		}
	}
}

// A record with a web_server part, which no version of the record holds, is
// refused rather than read with the part dropped: the web server is not
// something a grant reviews.
func TestParseRefusesAWebServerPart(t *testing.T) {
	for _, bad := range []string{
		`{"version": 3, "web_server": {"include_dir": "/etc/caddy/paisans.d"}}`,
		`{"version": 3, "images": {"applied_at": "x", "entries": []}, "web_server": {}}`,
	} {
		if rec, err := grant.Parse(bad); err == nil {
			t.Errorf("read %s as %+v", bad, rec)
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

// A record round-trips the build that wrote it.
func TestARecordRoundTripsItsBuild(t *testing.T) {
	want := version.Build{Version: "0.3.0-dev", Commit: "519299d5cb009da4904cdf53c2025f1ad609f102", Modified: true}
	rec, err := grant.Parse(grant.Encode(grant.Record{Build: want}))
	if err != nil || rec.Build != want {
		t.Errorf("got %+v, %v", rec.Build, err)
	}
}

// A record without every one of the build's keys is refused, and Read says
// to delete it and apply again.
func TestARecordWithNoBuildIsRefused(t *testing.T) {
	for _, bad := range []string{
		`{"version": 3, "images": {"applied_at": "x", "entries": []}, "toolkit_version": "", "toolkit_commit": "", "toolkit_modified": false}`,
		`{"version": 3, "images": {"applied_at": "x", "entries": []}, "toolkit_version": "0.3.0", "toolkit_commit": "abc", "toolkit_modified": false}`,
	} {
		if _, err := grant.Parse(bad); err == nil {
			t.Errorf("read %s", bad)
		}
	}
	content := `{"version": 3, "images": {"applied_at": "x", "entries": []}, "toolkit_version": "0.3.0"}`
	var noBuild *version.NoBuildError
	if _, err := grant.Parse(content); !errors.As(err, &noBuild) {
		t.Fatalf("got %v", err)
	}
	h := &fakeHost{content: content, found: true}
	_, _, _, err := grant.Read(h, testDeployment(t))
	if !errors.Is(err, grant.ErrMalformed) || !strings.Contains(err.Error(), "toolkit_commit, toolkit_modified missing") || !strings.Contains(err.Error(), "then apply again") {
		t.Errorf("got %v", err)
	}
}

package configstore

import (
	"testing"
	"time"
)

// A version's arrival is the first gather's time; a later gather keeps it,
// and a version that arrives later gets its own.
func TestRecordArrivalOnce(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	operators := map[string]string{"alice": alice.pub}
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", operators))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := Local{Dir: t.TempDir()}
	first, second := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return first }
	if _, err := gather(fresh, reach(sites, "home-a"), testID, Anchor{Root: v1}, GatherOptions{}, fakePull); err != nil {
		t.Fatal(err)
	}
	now = func() time.Time { return second }
	g, err := gather(fresh, reach(sites, "home-a"), testID, Anchor{Root: v1}, GatherOptions{}, fakePull)
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := fresh.ArrivedAt(v1); !ok || !at.Equal(first) {
		t.Fatalf("arrived %v %v, want %v", at, ok, first)
	}
	if err := fresh.RecordArrival(g, second); err != nil {
		t.Fatal(err)
	}
	if at, _ := fresh.ArrivedAt(v1); !at.Equal(first) {
		t.Fatalf("rewritten to %v", at)
	}
	if _, ok := fresh.ArrivedAt("0000000000000000000000000000000000000000"); ok {
		t.Fatal("an unknown version has an arrival")
	}
}

// A gather that asks no site to sync records arrivals too.
func TestDryRunRecordsArrival(t *testing.T) {
	needTools(t, "git", "ssh-keygen")
	alice := newOperator(t)
	sites := storeSites(t, "home-a")
	l := workstation(t, edit(testFiles(), "1", map[string]string{"alice": alice.pub}))
	v1, _, err := alice.publish(t, l, reach(sites, "home-a"), false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := Local{Dir: t.TempDir()}
	at := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return at }
	if _, err := gather(fresh, reach(sites, "home-a"), testID, Anchor{Root: v1}, GatherOptions{Sync: false}, fakePull); err != nil {
		t.Fatal(err)
	}
	if got, ok := fresh.ArrivedAt(v1); !ok || !got.Equal(at) {
		t.Fatalf("arrived %v %v", got, ok)
	}
}

// Two records of one version, as racing gathers leave, read as the first.
func TestRecordArrivalTwiceKeepsTheFirst(t *testing.T) {
	l := Local{Dir: t.TempDir()}
	sha := "1111111111111111111111111111111111111111"
	g := Gathered{Verdict: Verdict{Trusted: map[string]bool{sha: true}}}
	first, second := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if err := l.RecordArrival(g, first); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordArrival(g, second); err != nil {
		t.Fatal(err)
	}
	if at, ok := l.ArrivedAt(sha); !ok || !at.Equal(first) {
		t.Fatalf("got %v %v", at, ok)
	}
}

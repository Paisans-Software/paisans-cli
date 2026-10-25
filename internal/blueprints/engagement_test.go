package blueprints

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// The engagement blueprint is reserved: its port is its own among the
// clustered apps, its unsubscribe links answer without a sign in, it sends
// mail, and it generates its signing key and its reader's password.
func TestEngagementBlueprintParses(t *testing.T) {
	if _, ok := For(config.BlueprintEngagement); !ok {
		t.Fatal("engagement has no record")
	}
	if _, fromCatalog := Source(config.BlueprintEngagement); fromCatalog {
		t.Error("engagement is read from a catalog, and is a reserved blueprint")
	}
	if got := Port(config.BlueprintEngagement); got != 8083 {
		t.Errorf("port %d, want 8083", got)
	}
	if GateFor(config.BlueprintEngagement).Gateable {
		t.Error("engagement is gateable, and a member unsubscribing from an inbox may not be signed in")
	}
	if !SendsMail(config.BlueprintEngagement) {
		t.Error("engagement does not send mail")
	}
	if h := HealthOf(config.BlueprintEngagement); h.Path != "/healthz" {
		t.Errorf("health path %q, want /healthz", h.Path)
	}
	if !UsesPostgres(config.BlueprintEngagement) {
		t.Error("engagement keeps no database of its own")
	}
	var app Service
	var found bool
	for _, s := range Services(config.BlueprintEngagement) {
		if s.Name == "app" {
			app, found = s, true
		}
	}
	if !found {
		t.Fatal("engagement has no app service")
	}
	if app.Foundation != FoundationEngagement {
		t.Errorf("the app service's foundation is %q, want %q", app.Foundation, FoundationEngagement)
	}
	secrets := AppSecrets(config.BlueprintEngagement)
	for _, key := range []string{"digest_signing_key", "source_database_password"} {
		if !slices.Contains(secrets, key) {
			t.Errorf("%s is not one of its app secrets: %v", key, secrets)
		}
	}
	key, ok := SecretOf(config.BlueprintEngagement, "digest_signing_key")
	if !ok {
		t.Fatal("digest_signing_key has no spec")
	}
	if key.Format != FormatPassword || key.RotationCost != CostRestart || key.NeverRotate != "" {
		t.Errorf("digest_signing_key %+v, want a password rotated at the cost of a restart", key)
	}
	if !slices.Contains(SelfRotated(config.BlueprintEngagement), "digest_signing_key") {
		t.Error("digest_signing_key is not self rotated")
	}
	if !slices.ContainsFunc(RequiredSecrets(config.BlueprintEngagement), func(r RequiredSecret) bool { return r.Key == "digest_signing_key" }) {
		t.Error("digest_signing_key is not required, and the service refuses to start without it")
	}
}

// foundationWithoutEngagement is the test foundation as the newest published
// core catalog has it, before it publishes an engagement track.
func foundationWithoutEngagement(t *testing.T) []byte {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(testFoundation(t), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), FoundationEngagement+":") {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

// A core catalog released before the engagement track still reads, so every
// deployment without an engagement app keeps following its newest release.
func TestFoundationWithoutEngagementParses(t *testing.T) {
	raw := foundationWithoutEngagement(t)
	if strings.Contains(string(raw), FoundationEngagement+":") {
		t.Fatal("the fixture still names an engagement image")
	}
	if _, err := ParseFoundation(raw); err != nil {
		t.Fatalf("a foundation without engagement is refused: %v", err)
	}
	if _, err := ParseFoundation([]byte(testFoundation(t))); err != nil {
		t.Fatalf("a foundation with engagement is refused: %v", err)
	}
}

// An engagement app against a foundation that names no engagement image is
// refused when its image is resolved, saying which release it needs.
func TestEngagementWithoutAFoundationTrackIsRefused(t *testing.T) {
	spec, err := ParseFoundation(foundationWithoutEngagement(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(forgetCore)
	setFoundation(spec)
	_, err = FoundationImage(FoundationEngagement)
	want := "the core catalog's foundation.yaml has no engagement image; it needs a catalog release that publishes one"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

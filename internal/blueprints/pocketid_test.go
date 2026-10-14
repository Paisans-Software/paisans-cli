package blueprints_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// The Pocket ID image the foundation names is the repository the refusal
// text was read from.
func TestDefaultPocketIDImageHasItsStandbyMarker(t *testing.T) {
	image, ok := blueprints.DefaultImage(config.KindPocketID, "app")
	if !ok {
		t.Fatal("pocket-id ships no default app image")
	}
	if marker, known := blueprints.StandbyMarker(image); !known || marker == "" {
		t.Fatalf("pocket-id runs %s, marker %q, known %v", image, marker, known)
	}
}

// The wrapper quotes the marker in single quotes, and slog may quote the
// line it appears in, so a marker with a quote of either kind could never
// match, or would break the script.
func TestTheStandbyMarkerIsSafeToQuote(t *testing.T) {
	if strings.ContainsAny(blueprints.PocketIDStandbyMarker, `'"\`) {
		t.Errorf("marker %q contains a quote or a backslash", blueprints.PocketIDStandbyMarker)
	}
}

func TestAnUnknownImageGetsTheSameMarker(t *testing.T) {
	got, known := blueprints.StandbyMarker("registry.example.org/pocket-id:custom")
	if known || got != blueprints.PocketIDStandbyMarker {
		t.Errorf("StandbyMarker(custom) = %q, %v", got, known)
	}
}

// Each Pocket ID setting is one key and one variable, and no variable is
// rendered from two places, so a refusal can always name the one owner.
func TestPocketIDSettingsAreOneToOne(t *testing.T) {
	keys, envs := map[string]bool{}, map[string]bool{}
	for _, s := range blueprints.PocketIDSettings {
		if keys[s.Key] || envs[s.Env] {
			t.Errorf("%s / %s is declared twice", s.Key, s.Env)
		}
		keys[s.Key], envs[s.Env] = true, true
		if owner, ok := blueprints.PocketIDEnvOwner(s.Env); !ok || owner != "settings."+s.Key {
			t.Errorf("%s is owned by %q", s.Env, owner)
		}
		if s.Type == blueprints.SettingChoice && len(s.Choices) == 0 {
			t.Errorf("%s is a choice of nothing", s.Key)
		}
	}
	for _, env := range append([]string{blueprints.PocketIDSignupGroupsEnv, blueprints.PocketIDUIConfigDisabledEnv}, blueprints.PocketIDSMTPEnv...) {
		if envs[env] {
			t.Errorf("%s is in the settings table and rendered elsewhere too", env)
		}
	}
}

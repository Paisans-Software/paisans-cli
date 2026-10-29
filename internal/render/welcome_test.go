package render_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
)

func TestCommunityRoomName(t *testing.T) {
	for community, want := range map[string]string{
		"Riverside":           "Riverside Community",
		"Riverside Guild":     "Riverside Guild",
		"riverside community": "riverside community",
		"Riverside Clubhouse": "Riverside Clubhouse Community",
		" Riverside ":         "Riverside Community",
		"Fixture Community":   "Fixture Community",
		"The Commons":         "The Commons",
	} {
		if got := render.CommunityRoomName(community); got != want {
			t.Errorf("CommunityRoomName(%q) = %q, want %q", community, got, want)
		}
	}
}

func TestRoomAliasLocalpart(t *testing.T) {
	for name, want := range map[string]string{
		"Riverside Community": "riverside-community",
		"  Café & Co.  ":      "caf-co",
		"A--B":                "a-b",
		"!!!":                 "",
	} {
		if got := render.RoomAliasLocalpart(name); got != want {
			t.Errorf("RoomAliasLocalpart(%q) = %q, want %q", name, got, want)
		}
	}
}

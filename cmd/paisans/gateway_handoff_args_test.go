package main

import (
	"slices"
	"testing"
)

// The hand-off applies the old site with the move's own files, sudo,
// verbosity and image options, and its dry run is the same without
// --execute.
func TestTheHandOffAppliesWithTheMovesOwnFlags(t *testing.T) {
	got := handOffArgs("home-a", handOffFlags{config: "/d/paisans-stack.yaml", secrets: "/d/secrets.enc.yaml", sudo: true, verbose: true, keepImages: true, minFree: "5G"}, true)
	want := []string{"--site", "home-a", "--only", "infra", "--config", "/d/paisans-stack.yaml", "--secrets", "/d/secrets.enc.yaml", "--sudo=true", "-v", "--keep-images", "--min-free", "5G", "--execute"}
	if !slices.Equal(got, want) {
		t.Errorf("executed:\n got %q\nwant %q", got, want)
	}
	dry := handOffArgs("home-a", handOffFlags{config: "/d/paisans-stack.yaml", sudo: false}, false)
	if want := []string{"--site", "home-a", "--only", "infra", "--config", "/d/paisans-stack.yaml", "--sudo=false"}; !slices.Equal(dry, want) {
		t.Errorf("dry run:\n got %q\nwant %q", dry, want)
	}
}

// apply without --site passes its image options to the move step, whose
// hand-off applies a site.
func TestConvergePassesImageOptionsToTheMove(t *testing.T) {
	got := convergeFlags([]string{"gateway", "move"}, convergeOptions{Config: "c", KeepImages: true, MinFree: "5G"})
	if !slices.Contains(got, "--keep-images") || !slices.Contains(got, "5G") {
		t.Errorf("the move step lacks the image options: %q", got)
	}
}

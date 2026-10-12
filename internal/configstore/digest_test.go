package configstore

import (
	"errors"
	"testing"
)

func TestDigestIsStableAndOrderSensitive(t *testing.T) {
	a := Digest([]byte("x"), []byte("y"))
	if a != Digest([]byte("x"), []byte("y")) {
		t.Fatal("not stable")
	}
	if a == Digest([]byte("y"), []byte("x")) {
		t.Fatal("swapping the files must change the digest")
	}
	if want := "sha256:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881 sha256:a1fce4363854ff888cff4b8e7875d600c2682390412a8cf79b37d0b11148b0fa"; a != want {
		t.Fatalf("Digest = %s", a)
	}
}

func TestCheckDigest(t *testing.T) {
	f := fixtureVersion(t, "paisans: 1\n", "creation_rules: []\n")
	if err := CheckDigest(f, fixtureID); err != nil {
		t.Fatalf("matching: %v", err)
	}
	f.Paisans = []byte("paisans: 2\n")
	if err := CheckDigest(f, fixtureID); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("edited paisans.yaml: %v", err)
	}
	g := fixtureVersionWithoutDigest(t)
	if err := CheckDigest(g, fixtureID); !errors.Is(err, ErrNoDigest) {
		t.Fatalf("no digest: %v", err)
	}
}

package config

import (
	"strings"
	"testing"
)

func TestSetArchAddsOneLineAheadOfTheSitesFirstKey(t *testing.T) {
	in := "sites:\n  # the home server\n  home-a:\n    roles: [data, apps]\n    address: 10.44.0.1\n  vm:\n    roles:\n      - gateway\n    address: 10.44.0.2\n"
	out, err := setArch([]byte(in), map[string]string{"home-a": "amd64", "vm": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	want := "sites:\n  # the home server\n  home-a:\n    arch: amd64\n    roles: [data, apps]\n    address: 10.44.0.1\n  vm:\n    arch: arm64\n    roles:\n      - gateway\n    address: 10.44.0.2\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestSetArchWritesInsideAFlowMapping(t *testing.T) {
	in := "sites:\n  home-a: {roles: [data], address: 10.44.0.1}\n"
	out, err := setArch([]byte(in), map[string]string{"home-a": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "sites:\n  home-a: {arch: arm64, roles: [data], address: 10.44.0.1}\n"; string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestSetArchRefusesASiteThatRecordsOne(t *testing.T) {
	in := "sites:\n  home-a:\n    arch: amd64\n    address: 10.44.0.1\n"
	if _, err := setArch([]byte(in), map[string]string{"home-a": "arm64"}); err == nil || !strings.Contains(err.Error(), "already records") {
		t.Fatalf("got %v", err)
	}
}

func TestSetArchRefusesAnUndeclaredSite(t *testing.T) {
	in := "sites:\n  home-a:\n    address: 10.44.0.1\n"
	if _, err := setArch([]byte(in), map[string]string{"home-z": "arm64"}); err == nil || !strings.Contains(err.Error(), "home-z") {
		t.Fatalf("got %v", err)
	}
}

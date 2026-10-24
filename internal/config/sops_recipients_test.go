package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// sopsDir is a directory holding a .sops.yaml of body.
func sopsDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.SOPSConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// recipientsOf is the recipients the .sops.yaml data names, as Recipients
// reads them.
func recipientsOf(t *testing.T, data []byte) []string {
	t.Helper()
	got, err := config.Recipients(sopsDir(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAddSOPSRecipientOneRule(t *testing.T) {
	dir := sopsDir(t, "creation_rules:\n  - age: age1one\n")
	got, err := config.AddSOPSRecipient(dir, "age1two")
	if err != nil {
		t.Fatal(err)
	}
	if want := "creation_rules:\n  - age: age1one,age1two\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if on, _ := os.ReadFile(filepath.Join(dir, config.SOPSConfigName)); string(on) != "creation_rules:\n  - age: age1one\n" {
		t.Fatalf("the file on disk changed: %q", on)
	}
}

func TestAddSOPSRecipientTwoRules(t *testing.T) {
	body := "# who may decrypt\n" +
		"creation_rules:\n" +
		"  - path_regex: secrets\\.enc\\.yaml$\n" +
		"    age: age1one, age1three # alice, bob\n" +
		"  - age: age1one\n"
	got, err := config.AddSOPSRecipient(sopsDir(t, body), "age1two")
	if err != nil {
		t.Fatal(err)
	}
	if r := recipientsOf(t, got); !slices.Equal(r, []string{"age1one", "age1three", "age1two"}) {
		t.Fatalf("recipients %v\n%s", r, got)
	}
	for _, kept := range []string{"# who may decrypt", "path_regex: secrets\\.enc\\.yaml$", "# alice, bob"} {
		if !strings.Contains(string(got), kept) {
			t.Errorf("%q was lost:\n%s", kept, got)
		}
	}
	if strings.Count(string(got), "age1two") != 2 {
		t.Errorf("not added to both rules:\n%s", got)
	}
}

func TestAddSOPSRecipientAlreadyPresent(t *testing.T) {
	body := "creation_rules:\n    - age: \"age1one,age1two\"   # as written by hand\n    - age: age1two\n"
	got, err := config.AddSOPSRecipient(sopsDir(t, body), "age1two")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("got %q, want the file unchanged", got)
	}
}

func TestAddSOPSRecipientRefusesRulesItDoesNotWrite(t *testing.T) {
	for name, tc := range map[string]struct{ body, rule string }{
		"no age":     {"creation_rules:\n  - age: age1one\n  - path_regex: other$\n    pgp: 0123456789ABCDEF\n", "rule 2"},
		"key_groups": {"creation_rules:\n  - key_groups:\n      - age:\n          - age1one\n", "rule 1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.AddSOPSRecipient(sopsDir(t, tc.body), "age1two")
			if err == nil || !strings.Contains(err.Error(), "by hand") || !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestAddSOPSRecipientRefusesAMissingFile(t *testing.T) {
	if _, err := config.AddSOPSRecipient(t.TempDir(), "age1two"); err == nil {
		t.Fatal("a missing .sops.yaml was accepted")
	}
}

func TestAddSOPSRecipientRefusesNoRules(t *testing.T) {
	for _, body := range []string{"creation_rules: []\n", "{}\n", "creation_rules:\n  - age1one\n"} {
		if _, err := config.AddSOPSRecipient(sopsDir(t, body), "age1two"); err == nil {
			t.Errorf("%q was accepted", body)
		}
	}
}

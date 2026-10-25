package main

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// revision is one version of a test history: its number, paisans-stack.yaml
// and the keys of its secrets.enc.yaml.
type revision struct {
	n       int
	paisans string
	secrets string
}

// historyOf commits revs as a chain in a system copy's store, each signed by
// alice, and returns the gather that holds them and the last one's sha.
func historyOf(t *testing.T, revs ...revision) (configstore.Local, configstore.Gathered, string) {
	t.Helper()
	key, _ := testKey(t)
	sys := withSystemCopy(t, fixtureDeploymentID)
	cache, err := sys.Cache()
	if err != nil {
		t.Fatal(err)
	}
	g := configstore.Gathered{Versions: map[string]configstore.Version{}, Signers: map[string]string{}}
	var head string
	for _, r := range revs {
		var parents []string
		if head != "" {
			parents = []string{head}
		}
		f := configstore.Files{Paisans: []byte(r.paisans), Secrets: []byte(r.secrets), SOPS: []byte("creation_rules: []\n")}
		sha, err := cache.Commit(f, parents, r.n, key, time.Unix(1_800_000_000, 0))
		if err != nil {
			t.Fatal(err)
		}
		g.Versions[sha] = configstore.Version{SHA: sha, Parents: parents, Revision: r.n, SignerFP: "SHA256:fp"}
		g.Signers[sha] = "alice"
		head = sha
	}
	return sys, g, head
}

func draftOf(paisans, secrets string) configstore.Files {
	return configstore.Files{Paisans: []byte(paisans), Secrets: []byte(secrets), SOPS: []byte("creation_rules: []\n")}
}

const noSecrets = "sops:\n  version: 3\n"

func TestUndoFlagSiteAddedBack(t *testing.T) {
	both := "sites:\n  home-a:\n    host: a.example\n  home-b:\n    host: b.example\n"
	one := "sites:\n  home-a:\n    host: a.example\n"
	sys, g, head := historyOf(t,
		revision{14, both, noSecrets},
		revision{15, one, noSecrets},
		revision{16, one + "domain: x.example\n", noSecrets})
	flags, err := undoFlags(sys, g, head, draftOf(both+"domain: x.example\n", noSecrets))
	if err != nil {
		t.Fatal(err)
	}
	want := "sites.home-b: added back; revision 15 (signed by alice) removed it."
	if len(flags) != 1 || flags[0] != want {
		t.Errorf("flags = %q, want [%q]", flags, want)
	}
}

func TestUndoFlagValueChangedTwice(t *testing.T) {
	sys, g, head := historyOf(t,
		revision{1, "domain: one.example\n", noSecrets},
		revision{2, "domain: two.example\n", noSecrets},
		revision{3, "domain: three.example\n", noSecrets})
	// Back to revision 2's value: the latest change, revision 3, is undone.
	flags, err := undoFlags(sys, g, head, draftOf("domain: two.example\n", noSecrets))
	if err != nil {
		t.Fatal(err)
	}
	want := "domain: put back; revision 3 (signed by alice) changed it."
	if len(flags) != 1 || flags[0] != want {
		t.Errorf("flags = %q, want [%q]", flags, want)
	}
	// Back to revision 1's value is not what revision 3 changed from.
	flags, err = undoFlags(sys, g, head, draftOf("domain: one.example\n", noSecrets))
	if err != nil || len(flags) != 0 {
		t.Errorf("older value: %q, %v", flags, err)
	}
}

func TestUndoFlagPathRemovedAndReAdded(t *testing.T) {
	with := "domain: x.example\nname: n\n"
	without := "name: n\n"
	sys, g, head := historyOf(t,
		revision{1, with, noSecrets},
		revision{2, without, noSecrets},
		revision{3, with, noSecrets},
		revision{4, with + "extra: 1\n", noSecrets})
	// Revision 3 added domain back, and the draft drops it again: it equals
	// revision 2, the version before that change.
	flags, err := undoFlags(sys, g, head, draftOf("name: n\nextra: 1\n", noSecrets))
	if err != nil {
		t.Fatal(err)
	}
	want := "domain: removed; revision 3 (signed by alice) added it."
	if len(flags) != 1 || flags[0] != want {
		t.Errorf("flags = %q, want [%q]", flags, want)
	}
}

func TestUndoFlagSecretKeyByNameOnly(t *testing.T) {
	with := "token: ENC[AES256_GCM,data:SECRETVALUE1]\nsops:\n  version: 3\n"
	without := noSecrets
	sys, g, head := historyOf(t,
		revision{1, "domain: x\n", with},
		revision{2, "domain: x\n", without})
	flags, err := undoFlags(sys, g, head, draftOf("domain: x\n", "token: ENC[AES256_GCM,data:OTHERVALUE2]\nsops:\n  version: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := "secrets: token: added back; revision 2 (signed by alice) removed it."
	if len(flags) != 1 || flags[0] != want {
		t.Errorf("flags = %q, want [%q]", flags, want)
	}
	if strings.Contains(strings.Join(flags, "\n"), "VALUE") {
		t.Errorf("a secret value is in the flags: %q", flags)
	}
	// A changed value at the same key is not flagged.
	sys, g, head = historyOf(t,
		revision{1, "domain: x\n", with},
		revision{2, "domain: x\n", strings.Replace(with, "VALUE1", "VALUE2", 1)})
	flags, err = undoFlags(sys, g, head, draftOf("domain: x\n", with))
	if err != nil || len(flags) != 0 {
		t.Errorf("value change: %q, %v", flags, err)
	}
}

func TestNoUndoFlagForNewChange(t *testing.T) {
	sys, g, head := historyOf(t,
		revision{1, "domain: one.example\n", noSecrets},
		revision{2, "domain: two.example\n", noSecrets})
	flags, err := undoFlags(sys, g, head, draftOf("domain: new.example\nfresh: 1\n", noSecrets))
	if err != nil || len(flags) != 0 {
		t.Errorf("flags = %q, %v", flags, err)
	}
}

func TestUndoFlagNamesKeyWhenNoOperator(t *testing.T) {
	sys, g, head := historyOf(t,
		revision{1, "domain: one\n", noSecrets},
		revision{2, "domain: two\n", noSecrets})
	for sha := range g.Signers {
		g.Signers[sha] = ""
	}
	flags, err := undoFlags(sys, g, head, draftOf("domain: one\n", noSecrets))
	if err != nil {
		t.Fatal(err)
	}
	want := "domain: put back; revision 2 (signed by SHA256:fp) changed it."
	if len(flags) != 1 || flags[0] != want {
		t.Errorf("flags = %q, want [%q]", flags, want)
	}
}

// The review prints the flags under the difference, and prints none when
// the draft puts nothing back.
func TestShowChangePrintsUndoFlagsAfterTheDifference(t *testing.T) {
	sys, g, head := historyOf(t,
		revision{1, "domain: one.example\n", noSecrets},
		revision{2, "domain: two.example\n", noSecrets})
	rec := &ui.Recorder{}
	if _, err := showChange(rec, sys, g, []string{head}, draftOf("domain: one.example\n", noSecrets)); err != nil {
		t.Fatal(err)
	}
	_, lines, _ := differenceShown(t, rec)
	if n := len(lines); n < 2 || lines[n-1] != "domain: put back; revision 2 (signed by alice) changed it." {
		t.Errorf("the flag is not last under the difference:\n%s", strings.Join(lines, "\n"))
	}
	rec = &ui.Recorder{}
	if _, err := showChange(rec, sys, g, []string{head}, draftOf("domain: new.example\n", noSecrets)); err != nil {
		t.Fatal(err)
	}
	_, lines, _ = differenceShown(t, rec)
	if strings.Contains(strings.Join(lines, "\n"), "put back") {
		t.Errorf("flagged a new change:\n%s", strings.Join(lines, "\n"))
	}
}

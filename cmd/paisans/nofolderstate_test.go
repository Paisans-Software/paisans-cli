package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
)

// Every command run in one draft folder, beside a system copy and sites
// that answer, leaves the folder holding the draft and nothing a trust
// decision reads: no .paisans, whatever the command reads or writes.
func TestNoCommandWritesFolderState(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	pub := agentKeys()[0]
	cache, err := f.sys.Cache()
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.FetchBundle(mustBundle(t, f.answer)); err != nil {
		t.Fatal(err)
	}
	if err := f.sys.SetSigningKey(pub); err != nil {
		t.Fatal(err)
	}
	if err := must(openStore()).Record(localstore.Saved{ID: fixtureDeploymentID, Domain: "example.org", SHA: f.shas[0], Revision: 1, Status: localstore.Verified, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	signFromFile(t, f.key)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	deployment := func(args ...string) func() error {
		return func() error {
			return runDeployment(append(args, "--config", f.cfgPath), noTerminalStdin(), &out)
		}
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"checkout", deployment("checkout", "--execute")},
		{"validate", func() error { return runValidate([]string{"--config", f.cfgPath, "--offline"}) }},
		{"operator add", func() error {
			return runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "carol@example.org", "--key", createCarolKey, "--all-sites", "--config", f.cfgPath}, notATerminal(t), &out)
		}},
		{"secrets set", func() error {
			return runSecretsSet([]string{"external.acme_dns_token", "--config", f.cfgPath}, strings.NewReader("not-a-real-token-0001\n"))
		}},
		{"pull", func() error {
			theirs := f.files[0]
			theirs.Paisans = bytes.Replace(theirs.Paisans, []byte("from_name: Example Community"), []byte("from_name: Their Community"), 1)
			sha := f.sitesHold(t, theirs, f.shas[0], 2)
			if err := deployment("pull", "--execute")(); err != nil {
				return err
			}
			wantMerged(t, f.cfgPath, 2, sha, "from_name: Their Community", "carol")
			return nil
		}},
		{"apply", func() error { return f.applySite(t, noTerminalStdin()) }},
		// The fake hosts let a deploy go no further than the mesh, which is
		// past the publish.
		{"apply --execute", func() error {
			_ = f.applySite(t, noTerminalStdin(), "--execute")
			if !f.pushed() {
				t.Error("apply --execute published nothing")
			}
			return nil
		}},
		{"publish", deployment("publish")},
		{"log", deployment("log")},
		{"register", func() error { return runOperator([]string{"register", "--config", f.cfgPath}, noTerminalStdin(), &out) }},
		{"invite", func() error {
			return runOperatorInvite([]string{"dave", "--key", newPub(t), "--config", f.cfgPath}, io.Discard)
		}},
	}
	for _, s := range steps {
		var err error
		captureStdout(t, func() { err = s.run() })
		if err != nil {
			t.Errorf("%s: %s", s.name, problemText(err))
		}
		if _, err := os.Stat(f.cfgPath); err != nil {
			t.Fatalf("after %s the folder holds no draft: %v", s.name, err)
		}
		noFolderState(t, f.dir)
	}
}

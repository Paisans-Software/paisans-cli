package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// identityLines is each deployment section's lines, in order, as kind and
// result: what a log of the run names as its deployment and version. The
// core catalog's line is left out; TestApplyNamesTheCoreRelease reads it.
func identityLines(rec *ui.Recorder) [][]string {
	var out [][]string
	in := false
	for _, e := range rec.Events {
		switch {
		case e.Kind == "section":
			in = e.Text == "deployment"
			if in {
				out = append(out, nil)
			}
		case in && e.Kind != "step" && e.Text != "core catalog":
			out[len(out)-1] = append(out[len(out)-1], e.Kind+" "+e.Text+": "+e.Extra)
		}
	}
	return out
}

// built is the deployment section's first line: the build of paisans that
// runs it, its version and short commit.
var built = "done paisans: " + version.Current().String()

// Files that are the head the sites hold name it as the run opens, once the
// workstation holds that version, and nothing is pending, so the run does not
// name them again. The first run gathers the version into the workstation's
// store; the second names it from there.
func TestApplyNamesTheDeploymentAndVersionFirst(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	got := identityLines(rec)
	want := []string{built, "done id: " + f.cfgID(t), "done configuration: revision 1, " + f.shas[0][:12]}
	if rec.Index("section", "deployment") != 0 || len(got) != 1 {
		t.Fatalf("got %v\n%s", got, rec.Lines())
	}
	sameLines(t, got[0], want)
}

// The deployment section names the core catalog release the run reads.
func TestApplyNamesTheCoreRelease(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	release, _ := blueprints.CoreRelease()
	if release.Commit == "" || rec.Index("done", "core catalog") < 0 || !strings.Contains(rec.Lines(), release.Tag+", "+release.Commit[:12]) {
		t.Errorf("the run does not name the core release %+v:\n%s", release, rec.Lines())
	}
}

// Edited files are no version until the run publishes them: the version is
// pending as the run opens, and named once it ends, with what was published.
func TestApplyNamesAPublishedVersionAtTheEnd(t *testing.T) {
	f := newApplyStore(t, 1)
	f.based(t)
	edited := f.files[0]
	edited.Paisans = append(append([]byte{}, edited.Paisans...), "# next\n"...)
	if err := f.l.WriteFiles(edited); err != nil {
		t.Fatal(err)
	}
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, &grant.ConfigPart{Hash: f.shas[0], Revision: 1}))
	rec := recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.l.Head()
	got := identityLines(rec)
	if len(got) != 2 || head == f.shas[0] {
		t.Fatalf("head %s, got %v\n%s", head, got, rec.Lines())
	}
	sameLines(t, got[0], []string{built, "done id: " + f.cfgID(t), "pending configuration: pending: deployment apply --execute will publish it"})
	sameLines(t, got[1], []string{built, "done id: " + f.cfgID(t), "done configuration: revision 2, " + head[:12]})
	if end := rec.Index("section", "deployment"); rec.Index("done", "publish revision 2") < end {
		t.Errorf("named again before the publish:\n%s", rec.Lines())
	}
}

// A run that stops still names what it opened with pending, as it ends:
// files no version holds, refused before any host is reached, are named
// again after the refusal, the version still pending.
func TestApplyNamesWhatIsPendingWhenItStops(t *testing.T) {
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(data), "members: [", "members: [nowhere, ", 1)
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := recordConverge(t)
	captureStdout(t, func() {
		err = runApply([]string{"--config", path, "--secrets", fixtureSecretsPath()}, strings.NewReader(""), nil)
	})
	got := identityLines(rec)
	if err == nil || len(got) != 2 {
		t.Fatalf("err %v, got %v\n%s", err, got, rec.Lines())
	}
	pending := []string{built, "done id: " + declaredID(path), "pending configuration: pending: deployment apply --execute will publish it"}
	sameLines(t, got[0], pending)
	sameLines(t, got[1], pending)
	last := 0
	for i, e := range rec.Events {
		if e.Kind == "section" && e.Text == "deployment" {
			last = i
		}
	}
	if refused := rec.Index("refuse", ""); refused < 0 || refused > last {
		t.Errorf("not named again after the refusal:\n%s", rec.Lines())
	}
}

// cfgID is the id the fixture's files declare.
func (f *applyStoreFixture) cfgID(t *testing.T) string {
	t.Helper()
	id := declaredID(f.cfgPath)
	if id == "" {
		t.Fatal("the fixture declares no id")
	}
	return id
}

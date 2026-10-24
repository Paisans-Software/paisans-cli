package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// ownStore points the saved deployments at a directory of the test's own.
func ownStore(t *testing.T) localstore.Store {
	t.Helper()
	t.Setenv(localstore.DirVariable, t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// registryHolding is a registry naming each id, as root's file on a host.
func registryHolding(t *testing.T, ids ...string) string {
	t.Helper()
	r := registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{}}
	for _, id := range ids {
		r.Deployments[id] = registry.Entry{Token: id[:4], Root: "/srv/paisans/" + id[:4], Domain: "example.org", Site: "home-a", ClaimedAt: "2026-10-17T00:00:00Z", Build: version.Current()}
	}
	b, err := registry.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fetchFrom makes --ssh reach, as its owner, a host whose registry is reg
// and whose store answers answer.
func fetchFrom(t *testing.T, reg, answer string) {
	t.Helper()
	saved := reachOwner
	t.Cleanup(func() { reachOwner = saved })
	reachOwner = func(dest config.Destination, sudo bool) apply.Transport {
		if !sudo {
			t.Error("fetch reached the host without sudo")
		}
		files := map[string]string{}
		if reg != "" {
			files[registry.Path] = reg
		}
		return &storeHost{name: dest.String(), answer: answer, files: files}
	}
	var b bytes.Buffer
	savedR := reporterOverride
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = savedR })
}

// deployment apply --execute saves the version it published, with the key that signed
// it as the saved copy's anchor, so a later fetch can judge the sites from
// it wherever the deployment's own files went.
func TestApplySavesThePublishedVersion(t *testing.T) {
	st := ownStore(t)
	f := newApplyStore(t, 0)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	_ = f.applySite(t, noTerminalStdin(), "--execute")
	head, _ := f.l.Head()
	if head == "" {
		t.Fatal("nothing was published")
	}
	rec, ok, err := st.Load(fixtureID(t))
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	if rec.SHA != head || rec.Revision != 1 || rec.Status != localstore.Verified || rec.Domain != "example.org" || !strings.HasPrefix(rec.Source, "applied from ") {
		t.Errorf("record %+v", rec)
	}
	l, _ := st.Local(fixtureID(t))
	want, _ := f.l.Files()
	got, err := l.Files()
	if err != nil || !bytes.Equal(got.Paisans, want.Paisans) || !bytes.Equal(got.Secrets, want.Secrets) {
		t.Fatalf("saved files differ: %v", err)
	}
	if key, _ := l.SigningKey(); key == "" {
		t.Error("no signing key recorded as the anchor")
	}
	if h, _ := l.Head(); h != head {
		t.Errorf("saved head %q", h)
	}
}

// A dry run saves nothing.
func TestApplyDryRunSavesNothing(t *testing.T) {
	st := ownStore(t)
	f := newApplyStore(t, 0)
	loggingSSH(t, "")
	fakeGrants(t, grantedWith(t, f, nil))
	recordConverge(t)
	_ = f.applySite(t, noTerminalStdin())
	if listed, _ := st.List(); len(listed) != 0 {
		t.Fatalf("saved %+v", listed)
	}
}

// A recovery saves what it recovered.
func TestRecoverSavesTheVersion(t *testing.T) {
	st := ownStore(t)
	sha, answer, _ := storedVersion(t)
	recoverHosts(t, answer)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDeployment([]string{"recover", "--from", "ubuntu@203.0.113.9", "--root", sha, "--config", filepath.Join(dir, "paisans-stack.yaml"), "--execute"}, noTerminalStdin(), &out); err != nil {
		t.Fatal(err)
	}
	rec, ok, _ := st.Load(fixtureID(t))
	if !ok || rec.SHA != sha || rec.Status != localstore.Verified || rec.Source != "recovered into "+dir {
		t.Fatalf("record %+v", rec)
	}
	l, _ := st.Local(fixtureID(t))
	if r, _ := l.Root(); r != sha {
		t.Errorf("the recovery's pinned root was not carried: %q", r)
	}
}

// A deployment saved before is fetched as pull would take it: the host's
// newer version, judged from the saved anchor, becomes the saved copy.
func TestFetchUpdatesASavedDeployment(t *testing.T) {
	st := ownStore(t)
	shas, answer, files := storedHistory(t, 2, 0)
	// What an earlier save left: the first version, its root pinned.
	l, _ := st.Local(fixtureID(t))
	cache, _ := l.Cache()
	g := mustBundle(t, answer)
	if err := cache.FetchBundle(g); err != nil {
		t.Fatal(err)
	}
	if err := l.Checkout(shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := l.SetRoot(shas[0]); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(localstore.Saved{ID: fixtureID(t), SHA: shas[0], Revision: 1, Status: localstore.Verified}); err != nil {
		t.Fatal(err)
	}

	fetchFrom(t, registryHolding(t, fixtureID(t)), answer)
	if err := runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	rec, _, _ := st.Load(fixtureID(t))
	if rec.SHA != shas[1] || rec.Revision != 2 || rec.Status != localstore.Verified || rec.Source != "fetched from admin@203.0.113.9:22" || rec.Domain != "example.org" {
		t.Errorf("record %+v", rec)
	}
	got, _ := l.Files()
	if !got.Equal(files[1]) {
		t.Error("the saved files are not the newest version")
	}
}

// A deployment this workstation never saved has nothing to trust what the
// host serves from: its newest version is set aside under incoming, never
// written as the copy's own files, and listed as unverified.
func TestFetchSetsAsideAnUnknownDeployment(t *testing.T) {
	st := ownStore(t)
	shas, answer, _ := storedHistory(t, 2, 0)
	fetchFrom(t, registryHolding(t, fixtureID(t)), answer)
	if err := runDeployment([]string{"fetch", "--ssh", "admin@203.0.113.9"}, noTerminalStdin(), &bytes.Buffer{}); err != nil {
		t.Fatal(problemText(err))
	}
	l, _ := st.Local(fixtureID(t))
	if l.HasFiles() {
		t.Fatal("unverified files were written as the saved copy's own")
	}
	if _, err := os.Stat(filepath.Join(l.IncomingFor(shas[1]), "paisans-stack.yaml")); err != nil {
		t.Fatalf("the newest version is not set aside: %v", err)
	}
	rec, _, _ := st.Load(fixtureID(t))
	if rec.Status != localstore.Unverified || rec.SHA != shas[1] {
		t.Errorf("record %+v", rec)
	}
	var out bytes.Buffer
	if err := runDeploymentList(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fixtureID(t)) || !strings.Contains(out.String(), "unverified") {
		t.Errorf("list:\n%s", out.String())
	}
}

// A registry that cannot be read fetches nothing; a fetch needs --ssh; an
// id a host's registry names that is not one is never made a path.
func TestFetchRefusals(t *testing.T) {
	st := ownStore(t)
	if err := runDeploymentFetch(nil); err == nil || !strings.Contains(problemText(err), "--ssh") {
		t.Fatalf("no --ssh: %v", err)
	}
	fetchFrom(t, "not json", "")
	if err := runDeploymentFetch([]string{"--ssh", "admin@203.0.113.9"}); err == nil || !strings.Contains(problemText(err), "registry") {
		t.Fatalf("bad registry: %v", err)
	}
	if _, err := st.Local("../../etc"); err == nil {
		t.Fatal("a path was taken for an id")
	}
}

// With nothing saved, list says how deployments come to be saved.
func TestListWithNothingSaved(t *testing.T) {
	ownStore(t)
	var out bytes.Buffer
	if err := runDeploymentList(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No deployment is saved") {
		t.Errorf("list:\n%s", out.String())
	}
}

// mustBundle is a store answer's bundle.
func mustBundle(t *testing.T, answer string) []byte {
	t.Helper()
	b, _, err := configstore.Pull(&storeHost{name: "x", answer: answer}, deployment.Deployment{ID: fixtureID(t)}, false)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

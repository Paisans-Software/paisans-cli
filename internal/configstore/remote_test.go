package configstore

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

type fakeSite struct {
	files    map[string]string
	commands []string
	// answer is what the gather command prints.
	answer string
	// failSync makes the sync unit's start fail.
	failSync bool
	// bare is a site host prepare has not reached: no store, and no sync
	// unit, so starting the unit fails unless the command checks first.
	bare bool
}

func (f *fakeSite) Run(c string) (string, error) {
	f.commands = append(f.commands, c)
	switch {
	case strings.Contains(c, "systemctl start") && f.bare && strings.Contains(c, "[ -d '"+hostprep.ConfigRepo(dep)+"' ]"):
		return "", nil
	case strings.Contains(c, "systemctl start") && f.bare:
		return "Failed to start unit: Unit not found.", errors.New("exit status 5")
	case strings.Contains(c, "systemctl start") && f.failSync:
		return "boom", errors.New("exit 1")
	case strings.Contains(c, "bundle create"):
		return f.answer, nil
	}
	return "", nil
}
func (f *fakeSite) RunInput(c, _ string) (string, error) { return f.Run(c) }
func (f *fakeSite) ReadFile(p string) (string, bool, error) {
	s, ok := f.files[p]
	return s, ok, nil
}
func (f *fakeSite) WriteFile(p, content string, _ uint32) error {
	f.files[p] = content
	f.commands = append(f.commands, "write "+p)
	return nil
}
func (f *fakeSite) Describe() string { return "fake" }

var dep = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

func TestPushWritesThenVerifiesAndReceives(t *testing.T) {
	h := &fakeSite{files: map[string]string{}}
	if err := Push(h, dep, []byte("BUNDLE")); err != nil {
		t.Fatal(err)
	}
	tmp := "/var/lib/paisans/config/" + dep.Token() + ".incoming.bundle"
	if h.files[tmp] != "BUNDLE" {
		t.Fatalf("bundle not written at %s: %v", tmp, h.files)
	}
	if len(h.commands) != 3 || h.commands[1] != "write "+tmp {
		t.Fatalf("commands = %q", h.commands)
	}
	c := h.commands[2]
	for _, want := range []string{"bundle verify", "paisans_receive '" + hostprep.ConfigRepo(dep) + "' '" + tmp + "'", "rm -f '" + tmp + "'"} {
		if !strings.Contains(c, want) {
			t.Errorf("command lacks %q: %s", want, c)
		}
	}
	if !strings.Contains(c, hostprep.ConfigReceiveShell) {
		t.Errorf("command does not define the receive rule: %s", c)
	}
}

func TestPullOfAnEmptyRepositoryIsNilAndNoError(t *testing.T) {
	h := &fakeSite{files: map[string]string{}, answer: "__PAISANS_EMPTY__\n"}
	b, r, err := Pull(h, dep, false)
	if err != nil || b != nil || r != nil {
		t.Fatalf("got %v %v %v", b, r, err)
	}
}

func TestPullDecodesTheBundleAndTrailingNewline(t *testing.T) {
	h := &fakeSite{files: map[string]string{}, answer: base64.StdEncoding.EncodeToString([]byte("PACK\x00\x01")) + "\n"}
	b, _, err := Pull(h, dep, false)
	if err != nil || string(b) != "PACK\x00\x01" {
		t.Fatalf("got %q %v", b, err)
	}
	for _, c := range h.commands {
		if strings.HasPrefix(c, "systemctl") {
			t.Fatalf("sync started without syncFirst: %q", h.commands)
		}
	}
}

func TestPullSyncsFirstAndTreatsFailureAsReported(t *testing.T) {
	h := &fakeSite{files: map[string]string{}, answer: "__PAISANS_EMPTY__"}
	if _, _, err := Pull(h, dep, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.commands[0], "systemctl start '"+dep.Prefix()+"-config-sync.service'") {
		t.Fatalf("commands = %q", h.commands)
	}
	h = &fakeSite{files: map[string]string{}, answer: "__PAISANS_EMPTY__", failSync: true}
	_, r, err := Pull(h, dep, true)
	if err != nil || r == nil || r.SyncErr == "" {
		t.Fatalf("got %+v %v", r, err)
	}
}

// A site host prepare has not reached has no store and no sync unit, so it
// has nothing to sync: apply gathers before its host prepare steps, and a
// bare site is the start of every deployment, not a failed sync.
func TestPullSyncsNothingOnASiteWithNoStore(t *testing.T) {
	h := &fakeSite{files: map[string]string{}, answer: "__PAISANS_EMPTY__", bare: true}
	b, r, err := Pull(h, dep, true)
	if err != nil || b != nil || r != nil {
		t.Fatalf("got %v %+v %v", b, r, err)
	}
}

func TestPullParsesTheSyncLog(t *testing.T) {
	h := &fakeSite{files: map[string]string{
		"/var/lib/paisans/config/" + dep.Token() + ".sync.json": `{"at":"2026-10-12T10:00:00Z","peers":[{"peer":"10.9.0.2","ok":true},{"peer":"10.9.0.3","ok":false}]}`,
	}, answer: "__PAISANS_EMPTY__"}
	_, r, err := Pull(h, dep, false)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if r.At.Year() != 2026 || len(r.Peers) != 2 || r.Peers[0].Peer != "10.9.0.2" || !r.Peers[0].OK || r.Peers[1].OK {
		t.Fatalf("report = %+v", r)
	}
}

func TestPullIgnoresNoiseBeforeTheAnswer(t *testing.T) {
	h := &fakeSite{files: map[string]string{}, answer: "Warning: Permanently added host\nsudo: notice\n__PAISANS_EMPTY__\n"}
	if b, _, err := Pull(h, dep, false); err != nil || b != nil {
		t.Fatalf("marker: %v %v", b, err)
	}
	h.answer = "Warning: noise\n" + base64.StdEncoding.EncodeToString([]byte("PACK")) + "\n"
	if b, _, err := Pull(h, dep, false); err != nil || string(b) != "PACK" {
		t.Fatalf("base64: %q %v", b, err)
	}
}

func TestPullRefusesAnEmptyAnswer(t *testing.T) {
	for _, ans := range []string{"", "\n  \n"} {
		h := &fakeSite{files: map[string]string{}, answer: ans}
		if b, _, err := Pull(h, dep, false); err == nil || b != nil {
			t.Fatalf("%q: got %v %v", ans, b, err)
		}
	}
}

func TestPullKeepsTheBundleWhenTheSyncLogIsMalformed(t *testing.T) {
	h := &fakeSite{files: map[string]string{
		"/var/lib/paisans/config/" + dep.Token() + ".sync.json": "{not json",
	}, answer: base64.StdEncoding.EncodeToString([]byte("PACK"))}
	b, r, err := Pull(h, dep, false)
	if err != nil || string(b) != "PACK" || r == nil || r.SyncErr == "" {
		t.Fatalf("got %q %+v %v", b, r, err)
	}
}

// A site whose store host prepare has not made yet is told apart from any
// other failure, before anything is written there, so the refusal can name
// the command that makes it.
func TestPushToASiteWithNoStore(t *testing.T) {
	h := &noStoreSite{fakeSite{files: map[string]string{}}}
	err := Push(h, dep, []byte("B"))
	if !errors.Is(err, ErrNoStore) || len(h.files) != 0 {
		t.Fatalf("err = %v, files %v", err, h.files)
	}
}

type noStoreSite struct{ fakeSite }

func (f *noStoreSite) Run(c string) (string, error) {
	f.commands = append(f.commands, c)
	return "Last login: today\n" + noStoreMark + "\n", nil
}

type failingRun struct{ fakeSite }

func (f *failingRun) Run(c string) (string, error) { return "bad bundle", errors.New("exit 1") }

func TestPushSurfacesAFailure(t *testing.T) {
	h := &failingRun{fakeSite{files: map[string]string{}}}
	if err := Push(h, dep, []byte("B")); err == nil || !strings.Contains(err.Error(), "bad bundle") {
		t.Fatalf("err = %v", err)
	}
}

// An id that is not a deployment id never reaches a command line: a host's
// registry is where a recovery reads it, and a host can write anything there.
func TestPullAndPushRefuseAnIdThatIsNotOne(t *testing.T) {
	bad := deployment.Deployment{ID: "f2a9;touch /pwned;#-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
	h := &fakeSite{files: map[string]string{}, answer: "__PAISANS_EMPTY__"}
	if _, _, err := Pull(h, bad, true); err == nil {
		t.Fatal("pull ran with a bad id")
	}
	if err := Push(h, bad, []byte("B")); err == nil {
		t.Fatal("push ran with a bad id")
	}
	if len(h.commands) != 0 {
		t.Fatalf("commands reached the host: %q", h.commands)
	}
}

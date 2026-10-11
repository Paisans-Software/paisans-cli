package deployrecord_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/deployrecord"
	"github.com/paisans-software/paisans-stack/internal/registry"
)

// The record holds names only, so a write leaves it readable by the ssh user
// in a directory others may enter but not list.
func TestAWriteLeavesTheRecordReadableWithoutSudo(t *testing.T) {
	h := &fakeHost{files: map[string]string{}}
	var ran []string
	rec := recording{h, &ran}
	if _, err := deployrecord.Update(map[string]registry.Runner{"vm": rec}, dep, deployrecord.Adding(sites("vm")), now); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 {
		t.Fatalf("ran %d commands", len(ran))
	}
	command := ran[0]
	for _, want := range []string{"chmod 711 " + registry.Dir + ";", `chmod 644 "$tmp";`} {
		if !strings.Contains(command, want) {
			t.Errorf("the write has no %q:\n%s", want, command)
		}
	}
	for _, not := range []string{"chmod 700 ", "chmod 600 "} {
		if strings.Contains(command, not) {
			t.Errorf("the write still has %q:\n%s", not, command)
		}
	}
}

// recording is a fakeHost that keeps every command it runs.
type recording struct {
	*fakeHost
	ran *[]string
}

func (r recording) Run(command string) (string, error) {
	*r.ran = append(*r.ran, command)
	return r.fakeHost.Run(command)
}

// userHost answers ReadAsUser's command as the ssh user would see the file:
// readable, absent, or denied. It refuses everything else.
type userHost struct {
	dir     string
	content string
	state   string
	reads   int
}

func (u *userHost) Describe() string { return "ubuntu@vm.example.org" }
func (u *userHost) ReadFile(string) (string, bool, error) {
	panic("the ssh user's read goes through Run")
}
func (u *userHost) Run(command string) (string, error) {
	u.reads++
	out, err := exec.Command("sh", "-c", strings.ReplaceAll(command, deployrecord.Path(dep), u.path())).CombinedOutput()
	return string(out), err
}

// path is a file standing in for the record, in the state asked for.
func (u *userHost) path() string {
	p := filepath.Join(u.dir, "deployed.json")
	_ = os.Remove(p)
	switch u.state {
	case "readable":
		_ = os.WriteFile(p, []byte(u.content), 0o644)
	case "denied":
		_ = os.WriteFile(p, []byte(u.content), 0o000)
	}
	return p
}

// elevatedHost is the sudo read, counting how often it is asked.
type elevatedHost struct {
	fakeHost
	reads int
}

func (e *elevatedHost) ReadFile(p string) (string, bool, error) {
	e.reads++
	return e.fakeHost.ReadFile(p)
}

func TestTheRecordIsReadWithoutSudo(t *testing.T) {
	content := deployrecord.Encode(sites("vm", "watch"))
	user := &userHost{dir: t.TempDir(), content: content, state: "readable"}
	elevated := &elevatedHost{fakeHost: fakeHost{files: map[string]string{deployrecord.Path(dep): content}}}
	r, found, missing := deployrecord.Gather(map[string]registry.Runner{"vm": deployrecord.ReadAsUser(user, func() registry.Runner { return elevated })}, dep)
	if found != 1 || len(missing) != 0 || !r.Lists("sites", "watch") {
		t.Fatalf("%+v %d %v", r, found, missing)
	}
	if user.reads != 1 || elevated.reads != 0 {
		t.Errorf("user read %d times, sudo %d", user.reads, elevated.reads)
	}
}

func TestAnAbsentRecordIsNotReadThroughSudo(t *testing.T) {
	user := &userHost{dir: t.TempDir(), state: "absent"}
	elevated := &elevatedHost{fakeHost: fakeHost{files: map[string]string{}}}
	_, found, missing := deployrecord.Gather(map[string]registry.Runner{"vm": deployrecord.ReadAsUser(user, func() registry.Runner { return elevated })}, dep)
	if found != 0 || missing["vm"] != deployrecord.ErrNoRecord || elevated.reads != 0 {
		t.Errorf("found %d, missing %v, sudo reads %d", found, missing, elevated.reads)
	}
}

// A record written with modes the ssh user may not read is read through
// sudo instead.
func TestADeniedRecordIsReadThroughSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	content := deployrecord.Encode(sites("vm", "watch"))
	user := &userHost{dir: t.TempDir(), content: content, state: "denied"}
	elevated := &elevatedHost{fakeHost: fakeHost{files: map[string]string{deployrecord.Path(dep): content}}}
	r, found, missing := deployrecord.Gather(map[string]registry.Runner{"vm": deployrecord.ReadAsUser(user, func() registry.Runner { return elevated })}, dep)
	if found != 1 || len(missing) != 0 || !r.Lists("sites", "watch") || elevated.reads != 1 {
		t.Errorf("%+v %d %v, sudo reads %d", r, found, missing, elevated.reads)
	}
}

// A directory the ssh user may not enter hides the record from it entirely,
// which is a denial, not an absent record.
func TestADirectoryTheUserCannotEnterIsADenial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root enters a 0700 directory of anyone's")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(locked, "deployed.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	out, err := exec.Command("sh", "-c", deployrecord.UserReadCommand(p)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != deployrecord.DeniedMarker {
		t.Errorf("got %q", out)
	}
}

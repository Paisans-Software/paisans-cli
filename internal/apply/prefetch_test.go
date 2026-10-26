package apply_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// localSSH runs each ssh command's remote command with sh on this machine,
// as the host would, and counts the round trips.
func localSSH(t *testing.T) *int {
	t.Helper()
	trips := 0
	t.Cleanup(apply.SetRunSSH(func(c *exec.Cmd) error {
		trips++
		sh := exec.Command("sh", "-c", c.Args[len(c.Args)-1])
		sh.Stdin, sh.Stdout, sh.Stderr = c.Stdin, c.Stdout, c.Stderr
		return sh.Run()
	}))
	return &trips
}

func TestPrefetchAnswersInOneRoundTrip(t *testing.T) {
	trips := localSSH(t)
	dir := t.TempDir()
	file := dir + "/present"
	if out, err := exec.Command("sh", "-c", "printf 'a\\n\\n' > "+file).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	host := apply.SSHTransport{Destination: "ubuntu@host.example.org"}
	commands := []string{
		"echo one",
		"echo out; echo err >&2; exit 3",
		"command -v nothing-here >/dev/null 2>&1 || { echo absent; exit 0; }; echo unreached",
		"printf '__PAISANS_BATCH__ 0 0 x\\n'",
	}
	p := apply.Prefetch(host, commands, []string{file, dir + "/missing"})
	if *trips != 1 {
		t.Fatalf("prefetching took %d round trips, want 1", *trips)
	}

	if out, err := p.Run("echo one"); err != nil || out != "one\n" {
		t.Errorf("Run(echo one) = %q, %v", out, err)
	}
	out, err := p.Run(commands[1])
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("a command exiting 3 gave error %v", err)
	}
	if out != "out\nerr\n" {
		t.Errorf("a failing command's output = %q, want its stdout and stderr", out)
	}
	if out, err := p.Run(commands[2]); err != nil || out != "absent\n" {
		t.Errorf("an exit in one command = %q, %v; want it to end that command alone", out, err)
	}
	if out, err := p.Run(commands[3]); err != nil || out != "__PAISANS_BATCH__ 0 0 x\n" {
		t.Errorf("output that looks like the batch's mark = %q, %v", out, err)
	}
	if content, found, err := p.ReadFile(file); err != nil || !found || content != "a\n\n" {
		t.Errorf("ReadFile(present) = %q, %v, %v; want its trailing newlines kept", content, found, err)
	}
	if _, found, err := p.ReadFile(dir + "/missing"); err != nil || found {
		t.Errorf("ReadFile(missing) found %v, %v", found, err)
	}
	if *trips != 1 {
		t.Errorf("answering the prefetched took %d round trips in all, want 1", *trips)
	}

	// An answer is used once: asked again, the host answers.
	if out, err := p.Run("echo one"); err != nil || out != "one\n" {
		t.Errorf("Run(echo one) again = %q, %v", out, err)
	}
	if *trips != 2 {
		t.Errorf("asking again took %d round trips in all, want 2", *trips)
	}
}

func TestPrefetchDropsAnswersAfterAWrite(t *testing.T) {
	trips := localSSH(t)
	dir := t.TempDir()
	file := dir + "/f"
	host := apply.SSHTransport{Destination: "ubuntu@host.example.org"}
	p := apply.Prefetch(host, nil, []string{file})
	if err := p.WriteFile(file, "written\n", 0o600); err != nil {
		t.Fatal(err)
	}
	content, found, err := p.ReadFile(file)
	if err != nil || !found || content != "written\n" {
		t.Errorf("ReadFile after WriteFile = %q, %v, %v; want what was written", content, found, err)
	}
	if *trips != 3 {
		t.Errorf("took %d round trips, want 3: the prefetch, the write and the read", *trips)
	}
}

// A transport that is not ssh, such as a test's fake, is asked each command
// when the caller asks it.
func TestPrefetchLeavesOtherTransports(t *testing.T) {
	var fake apply.Transport = &countingTransport{}
	if got := apply.Prefetch(fake, []string{"echo one"}, nil); got != fake {
		t.Errorf("Prefetch wrapped a transport that is not ssh")
	}
}

type countingTransport struct{ apply.Transport }

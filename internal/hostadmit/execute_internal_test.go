package hostadmit

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// testHost answers admit's probe from facts a test changes as it runs, and
// every other command with nothing.
type testHost struct {
	facts [][2]string
	ran   []string
	// onRun sees every command, so a test can make the host change as a
	// real one would.
	onRun func(command string)
}

const testOSRelease = "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\n"

func newTestHost() *testHost {
	return &testHost{facts: [][2]string{{"passwd", "absent"}, {"engine", "absent"}}}
}

// set replaces every line key had with one line of value.
func (h *testHost) set(key, value string) {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv[0] != key {
			kept = append(kept, kv)
		}
	}
	h.facts = append(kept, [2]string{key, value})
}

func (h *testHost) Describe() string { return "test" }

func (h *testHost) Run(command string) (string, error) { return h.run(command) }

func (h *testHost) RunInput(command, _ string) (string, error) { return h.run(command) }

func (h *testHost) run(command string) (string, error) {
	h.ran = append(h.ran, command)
	if h.onRun != nil {
		h.onRun(command)
	}
	if strings.Contains(command, "paisans host admit: read only") {
		var b strings.Builder
		for _, kv := range h.facts {
			fmt.Fprintf(&b, "%s %s\n", kv[0], kv[1])
		}
		return b.String() + "end\n", nil
	}
	return "", nil
}

func (h *testHost) ReadFile(path string) (string, bool, error) {
	if path == "/etc/os-release" {
		return testOSRelease, true, nil
	}
	return "", false, nil
}

func (h *testHost) WriteFile(string, string, uint32) error { return nil }

// ranMatching is every command run that contains s.
func (h *testHost) ranMatching(s string) []string {
	var out []string
	for _, c := range h.ran {
		if strings.Contains(c, s) {
			out = append(out, c)
		}
	}
	return out
}

// testInput is the witness's request from the admission package's fixture,
// which needs no grant.
func testInput(t *testing.T) Input {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "admission", "testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	built, err := admission.Build(cfg, "vm")
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Parse(admission.Encode(built))
	if err != nil {
		t.Fatal(err)
	}
	return Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
}

func discard() ui.Reporter { return ui.NewPlain(io.Discard, false) }

// withParts runs the test with ps as admit's parts.
func withParts(t *testing.T, ps []part) {
	saved := parts
	parts = ps
	t.Cleanup(func() { parts = saved })
}

// userPart creates the user when it is absent.
var userPart = part{"user", func(p *planner) error {
	if !p.f.User.Exists {
		p.step(Step{Title: "create user paisans-f2a9", Command: "useradd paisans-f2a9"})
	}
	return nil
}}

// A step that needs the user's UID is provisional until the user exists:
// Execute runs what comes before it, reads the host again, and runs the
// rest with the real UID.
func TestExecuteRebuildsOnceTheUserExists(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Steps[1].Provisional || !strings.Contains(plan.Text(), "nft uid <uid>") {
		t.Fatalf("the egress step is not provisional with <uid>:\n%s", plan.Text())
	}
	if err := Execute(h, in, plan, discard()); err != nil {
		t.Fatal(err)
	}
	if got := h.ranMatching("nft uid"); len(got) != 1 || got[0] != "nft uid 1001" {
		t.Errorf("the egress step ran as %q, want once with uid 1001", got)
	}
	if got := h.ranMatching("useradd"); len(got) != 1 {
		t.Errorf("useradd ran %d times", len(got))
	}
}

// The plan Execute rebuilds must be the approved one from the provisional
// step on, in everything but the values it could not know: a host that
// changed while admit ran is not acted on.
func TestExecuteStopsWhenTheRebuiltPlanDiffers(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("surprise", "yes")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			if p.f.raw["surprise"] != nil {
				p.step(Step{Title: "something new", Command: "rm -rf /"})
			}
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("rm -rf")) != 0 || len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran past the change: %q", h.ran)
	}
}

// A rebuilt step that is still provisional means the user the earlier
// steps were to create does not exist: Execute stops rather than run a
// command with <uid> in it.
func TestExecuteStopsWhenTheUserStillDoesNotExist(t *testing.T) {
	h := newTestHost()
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil || !strings.Contains(err.Error(), "still needs the user") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran a provisional step: %q", h.ran)
	}
}

// A failed step stops Execute, names the step, and quotes the host's output
// only as visible text, so an escape sequence in it cannot redraw the
// screen.
func TestExecuteStopsAtAFailureAndShowsItsOutputVisibly(t *testing.T) {
	failing := &failHost{testHost: newTestHost(), out: "boom\x1b[2J\nsecond line"}
	withParts(t, []part{{"two", func(p *planner) error {
		p.step(Step{Title: "first", Command: "fail here"})
		p.step(Step{Title: "second", Command: "never"})
		return nil
	}}})
	in := testInput(t)
	plan, err := Build(failing, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(failing, in, plan, discard())
	if err == nil || !strings.HasPrefix(err.Error(), "first: ") {
		t.Fatalf("Execute = %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), `boom\x1b[2J`) || !strings.Contains(err.Error(), "second line") {
		t.Errorf("the host's output is not shown visibly: %q", err.Error())
	}
	if slices.Contains(failing.ran, "never") {
		t.Error("Execute ran past a failed step")
	}
}

type failHost struct {
	*testHost
	out string
}

func (h *failHost) Run(command string) (string, error) {
	if command == "fail here" {
		h.ran = append(h.ran, command)
		return h.out, fmt.Errorf("exit status 1")
	}
	return h.testHost.Run(command)
}

// A step's stdin goes to the command, never onto its command line.
func TestExecuteSendsInputOnStdin(t *testing.T) {
	h := &stdinHost{testHost: newTestHost()}
	withParts(t, []part{{"keys", func(p *planner) error {
		p.step(Step{Title: "add keys", Command: "cat >> keys", Input: "ssh-ed25519 AAAA\n"})
		return nil
	}}})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(h, in, plan, discard()); err != nil {
		t.Fatal(err)
	}
	if h.stdin["cat >> keys"] != "ssh-ed25519 AAAA\n" {
		t.Errorf("stdin %q", h.stdin)
	}
}

type stdinHost struct {
	*testHost
	stdin map[string]string
}

func (h *stdinHost) RunInput(command, stdin string) (string, error) {
	if h.stdin == nil {
		h.stdin = map[string]string{}
	}
	h.stdin[command] = stdin
	return h.testHost.RunInput(command, stdin)
}

// The rebuilt plan must be the approved one in every step's command, file
// and stdin once the placeholders are filled, not in its titles alone: a
// step whose command changed under the same title is not run.
func TestExecuteStopsWhenOnlyACommandChanged(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("surprise", "yes")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			command := "nft uid " + uidOf(p)
			if p.f.raw["surprise"] != nil {
				command += " && rm -rf /"
			}
			p.step(Step{Title: "load the egress table", Command: command, Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") || !strings.Contains(err.Error(), "command") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran the changed step: %q", h.ran)
	}
}

// The subordinate range is a placeholder too: filled from the range the
// new user was given, a step that uses it is the approved step.
func TestExecuteFillsTheSubordinateRange(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("subuid", "paisans-f2a9:165536:65536")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			r, err := rangeOf(p)
			if err != nil {
				return err
			}
			p.step(Step{Title: "load the egress table", Command: "nft load", Input: "meta skuid { " + uidOf(p) + ", " + r + " }\n", Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[1].Input != "meta skuid { <uid>, <subordinate range> }\n" {
		t.Fatalf("stdin %q", plan.Steps[1].Input)
	}
	if err := Execute(h, in, plan, discard()); err != nil {
		t.Fatal(err)
	}
	if got := h.ranMatching("nft load"); len(got) != 1 {
		t.Errorf("the egress step ran %d times", len(got))
	}
}

// Steps are compared one by one, after their counts: a title holding a
// newline is never read as two steps, and a rebuilt plan longer or shorter
// than the approved one is refused, not indexed past its end.
func TestSameStepsComparesCountsFirst(t *testing.T) {
	fill := strings.NewReplacer()
	one := []Step{{Title: "a\nb", Command: "true"}}
	two := []Step{{Title: "a", Command: "true"}, {Title: "b", Command: "rm -rf /"}}
	if err := sameSteps(one, two, fill); err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Errorf("one approved, two rebuilt: %v", err)
	}
	if err := sameSteps(two, one, fill); err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Errorf("two approved, one rebuilt: %v", err)
	}
	if err := sameSteps(two, two, fill); err != nil {
		t.Errorf("the same steps: %v", err)
	}
}

// A user that exists with no subordinate range, or with more than one, has
// no range a step can name: rangeOf refuses, naming /etc/subuid, and Build
// stops rather than plan a step with the placeholder in it.
func TestRangeOfRefusesAnExistingUserWithoutOneRange(t *testing.T) {
	for name, ranges := range map[string][]string{"none": nil, "two": {"paisans-f2a9:165536:65536", "paisans-f2a9:300000:65536"}} {
		t.Run(name, func(t *testing.T) {
			h := newTestHost()
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			for _, r := range ranges {
				h.facts = append(h.facts, [2]string{"subuid", r})
			}
			withParts(t, []part{{"egress", func(p *planner) error {
				r, err := rangeOf(p)
				if err != nil {
					return err
				}
				p.step(Step{Title: "load the egress table", Command: "nft " + r})
				return nil
			}}})
			_, err := Build(h, testInput(t))
			if err == nil || !strings.Contains(err.Error(), "/etc/subuid") {
				t.Fatalf("Build = %v", err)
			}
		})
	}
}

// A probed path below a link or another's directory stops admit before
// any part plans, naming the path and why, so no part can forget to.
func TestBuildRefusesAnUnsafePath(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"dir", "/srv/paisans/f2a9 link-above 0 0", "/srv/paisans/f2a9"},
		{"file", "/etc/sudoers.d/paisans-f2a9 unsafe-above", "/etc/sudoers.d/paisans-f2a9"},
		{"data-above", "unsafe-above", "where Docker's data would go"},
	} {
		h := newTestHost()
		h.facts = append(h.facts, [2]string{c.key, c.value})
		planned := false
		withParts(t, []part{{"any", func(p *planner) error { planned = true; return nil }}})
		_, err := Build(h, testInput(t))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%s %s: Build = %v", c.key, c.value, err)
		}
		if planned {
			t.Errorf("%s %s: a part planned on an unsafe host", c.key, c.value)
		}
	}
}

package hostprep_test

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// ubuntuBashrc is the head of the ~/.bashrc Ubuntu gives a new user: it
// returns at once for a shell that is not interactive.
const ubuntuBashrc = "# ~/.bashrc: executed by bash(1) for non-login shells.\n\ncase $- in\n    *i*) ;;\n      *) return;;\nesac\n\nHISTCONTROL=ignoreboth\n"

// shellSteps builds home-a's plan on host and returns its shell steps.
func shellSteps(t *testing.T, host *fakeHost) []hostprep.Step {
	t.Helper()
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	var out []hostprep.Step
	for _, s := range plan.Steps {
		if strings.Join(s.Kinds, ",") == hostprep.KindShell {
			out = append(out, s)
		}
	}
	return out
}

// written is the content a write step's command puts in place, decoded
// from the base64 it carries.
func written(t *testing.T, s hostprep.Step) string {
	t.Helper()
	_, rest, ok := strings.Cut(s.Command, "printf %s '")
	if !ok {
		t.Fatalf("no content in %q", s.Command)
	}
	b64, _, _ := strings.Cut(rest, "'")
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The block goes above Ubuntu's early return, so `ssh host docker ps`
// reads it as well as an interactive shell, and the rest of the file is
// kept as it was.
func TestTheDockerHostBlockGoesFirst(t *testing.T) {
	host := preparedHost(false)
	host.files[bashrcPath] = ubuntuBashrc
	steps := shellSteps(t, host)
	if len(steps) != 1 || steps[0].Title != "set DOCKER_HOST in .bashrc" {
		t.Fatalf("steps %+v, want the .bashrc write alone", steps)
	}
	got := written(t, steps[0])
	if !strings.HasPrefix(got, "# >>> paisans-f2a9 >>>\n") || !strings.HasSuffix(got, "# <<< paisans-f2a9 <<<\n"+ubuntuBashrc) {
		t.Errorf("bashrc:\n%s", got)
	}
	if !strings.Contains(got, `export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/docker.sock"`) {
		t.Errorf("no DOCKER_HOST in:\n%s", got)
	}
}

// A block that differs is replaced where it stands, and nothing around it
// moves; a block as written plans nothing.
func TestAnOldBlockIsReplacedInPlace(t *testing.T) {
	host := preparedHost(false)
	old := "# >>> paisans-f2a9 >>>\nexport DOCKER_HOST=stale\n# <<< paisans-f2a9 <<<\n"
	host.files[bashrcPath] = "alias ll='ls -l'\n" + old + ubuntuBashrc
	steps := shellSteps(t, host)
	if len(steps) != 1 {
		t.Fatalf("steps %+v", steps)
	}
	want := hostprep.WithBashrcBlock(fixture(t).Deployment(), "")
	if got := written(t, steps[0]); got != "alias ll='ls -l'\n"+want+ubuntuBashrc {
		t.Errorf("bashrc:\n%s", got)
	}
	host.files[bashrcPath] = "alias ll='ls -l'\n" + want + ubuntuBashrc
	if steps := shellSteps(t, host); len(steps) != 0 {
		t.Errorf("a kept block planned %+v", steps)
	}
}

// The block runs under sh and bash alike: it sets XDG_RUNTIME_DIR when the
// session has none, keeps one it has, and points DOCKER_HOST under it.
func TestTheBlockSetsDockerHost(t *testing.T) {
	block := hostprep.WithBashrcBlock(fixture(t).Deployment(), "")
	for _, c := range []struct{ env, want string }{
		{"", "unix:///run/user/"},
		{"XDG_RUNTIME_DIR=/tmp/xdg", "unix:///tmp/xdg/docker.sock"},
	} {
		cmd := exec.Command("sh", "-c", block+`printf %s "$DOCKER_HOST"`)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		if c.env != "" {
			cmd.Env = append(cmd.Env, c.env)
		}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), c.want) {
			t.Errorf("%q: DOCKER_HOST %q (%v), want it to start %q", c.env, out, err, c.want)
		}
	}
}

// ~/deployment is made when absent, pointed back when it links elsewhere,
// and a file or directory of that name is left, with a warning.
func TestTheDeploymentLink(t *testing.T) {
	for _, c := range []struct {
		name, probe string
		step        bool
		warn        bool
	}{
		{"absent", "", true, false},
		{"elsewhere", "link /srv/paisans/0000\n", true, false},
		{"kept", "link /srv/paisans/f2a9\n", false, false},
		{"a directory", "other\n", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			host := preparedHost(false)
			host.responses[probeLink] = c.probe
			plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
			if err != nil {
				t.Fatal(err)
			}
			var link *hostprep.Step
			for i, s := range plan.Steps {
				if s.Title == "link ~/deployment" {
					link = &plan.Steps[i]
				}
			}
			if (link != nil) != c.step {
				t.Fatalf("link step %+v, want one: %v", link, c.step)
			}
			if link != nil && !strings.Contains(link.Command, "ln -sfn '/srv/paisans/f2a9' '/home/ubuntu/deployment'") {
				t.Errorf("command %q", link.Command)
			}
			warned := false
			for _, w := range plan.Warnings {
				warned = warned || strings.Contains(w.Hint, "/home/ubuntu/deployment")
			}
			if warned != c.warn {
				t.Errorf("warned %v, want %v: %+v", warned, c.warn, plan.Warnings)
			}
		})
	}
}

// The link step makes the link, replaces a link, and leaves a directory
// that appeared after the probe alone rather than linking into it.
func TestTheLinkCommandRunsUnderSh(t *testing.T) {
	plan, err := hostprep.Build("home-a", fixture(t), func() *fakeHost { h := preparedHost(false); h.responses[probeLink] = ""; return h }(), admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	var command string
	for _, s := range plan.Steps {
		if s.Title == "link ~/deployment" {
			command = s.Command
		}
	}
	dir := t.TempDir()
	command = strings.ReplaceAll(command, "/home/ubuntu", dir)
	command = strings.ReplaceAll(command, "'/srv/paisans/f2a9'", "'"+dir+"/target'")
	for _, step := range []string{"make", "replace"} {
		if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", step, err, out)
		}
		if got, _ := exec.Command("readlink", dir+"/deployment").Output(); strings.TrimSpace(string(got)) != dir+"/target" {
			t.Fatalf("%s: link is %q", step, got)
		}
	}
	if err := exec.Command("sh", "-c", "rm "+dir+"/deployment && mkdir "+dir+"/deployment").Run(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err == nil || !strings.Contains(string(out), "is not a link") {
		t.Errorf("over a directory: %v: %s", err, out)
	}
	if entries, _ := exec.Command("ls", "-A", dir+"/deployment").Output(); len(entries) != 0 {
		t.Errorf("linked into the directory: %s", entries)
	}
}

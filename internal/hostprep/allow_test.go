package hostprep_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostprep"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const allowUbuntu = "/etc/paisans/ssh-allow.ubuntu"

// allowing is a list holding the keys of lines.
func allowing(lines ...string) string {
	out := "# keys host prepare may add\n"
	for _, l := range lines {
		out += fingerprint(l) + " " + strings.Fields(l)[2] + "\n"
	}
	return out
}

// With no list every key is added, as before there was one.
func TestNoAllowlistAllowsEveryKey(t *testing.T) {
	host := preparedHost(false)
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(stepsLabelled(plan, "add")) != 1 || hostprep.Refusal(plan) != nil {
		t.Errorf("bob's key is not added:\n%s", printed(plan))
	}
}

// A list without the key refuses the append, in the plan, naming the key, its
// admin and the command that allows it; Execute then runs nothing.
func TestAnAllowlistWithoutTheKeyRefusesTheAppend(t *testing.T) {
	host := preparedHost(false)
	host.files[allowUbuntu] = allowing(keyAlice)
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(stepsLabelled(plan, "add")) != 0 {
		t.Errorf("bob's key is planned:\n%s", printed(plan))
	}
	refusal := hostprep.Refusal(plan)
	if refusal == nil {
		t.Fatalf("not refused:\n%s", printed(plan))
	}
	hint, explain := ui.Describe(refusal)
	if hint != "a key not on ubuntu's allowlist on home-a" || !strings.Contains(explain, fingerprint(keyBob)+" (bob)") || !strings.Contains(explain, "paisans host allow home-a bob") {
		t.Errorf("refusal %q: %s", hint, explain)
	}
	if !strings.Contains(printed(plan), "refuse: "+hint) {
		t.Errorf("the plan does not show the refusal:\n%s", printed(plan))
	}
	ran := len(host.ran)
	if err := hostprep.Execute(plan, host, ui.Discard); err == nil || len(host.ran) != ran {
		t.Errorf("Execute ran %d commands and returned %v", len(host.ran)-ran, err)
	}
}

// Replacing the key under a listed admin's name is a key the list does not
// hold: the permission belongs to the fingerprint, not the name.
func TestASwappedKeyUnderAListedAdminIsRefused(t *testing.T) {
	host := preparedHost(false)
	host.files[allowUbuntu] = allowing(keyAlice)
	cfg := fixture(t)
	s := cfg.Sites["home-a"]
	s.SSH.Keys = []config.NamedKey{{Name: "alice", Line: keyAlice}, {Name: "alice", Line: keyCarol}}
	cfg.Sites["home-a"] = s
	plan, err := hostprep.Build("home-a", cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Refused) != 1 || plan.Refused[0].Fingerprint != fingerprint(keyCarol) || plan.Refused[0].Admin != "alice" {
		t.Errorf("refused %+v", plan.Refused)
	}
}

// What the list never refuses: a key already in authorized_keys, shared,
// renamed, released, forgotten or removed. An empty list allows no addition
// and still lets all of those through.
func TestAnAllowlistLeavesEverythingButAnAppend(t *testing.T) {
	for name, c := range map[string]struct {
		keys        []string
		authorized  string
		own, theirs string
		label       string
	}{
		"share":   {keys: []string{keyAlice, keyBob}, authorized: keyAlice + "\n" + keyBob + "\n", theirs: "added " + fingerprint(keyBob) + " bob\n", label: "share"},
		"rename":  {keys: []string{keyAlice}, authorized: keyAlice + "\n", own: "added " + fingerprint(keyAlice) + " alice-old\n", label: "rename"},
		"release": {keys: []string{keyAlice}, authorized: keyAlice + "\n" + keyCarol + "\n", own: "added " + fingerprint(keyAlice) + " alice\nshared " + fingerprint(keyCarol) + " carol\n", label: "release"},
		"remove":  {keys: []string{keyAlice}, authorized: keyAlice + "\n" + keyCarol + "\n", own: "added " + fingerprint(keyAlice) + " alice\nadded " + fingerprint(keyCarol) + " carol\n", label: "remove"},
		"forget":  {keys: []string{keyAlice}, authorized: keyAlice + "\n", own: "added " + fingerprint(keyAlice) + " alice\nadded " + fingerprint(keyCarol) + " carol\n", label: ""},
		"present": {keys: []string{keyAlice, keyCloud}, authorized: keyAlice + "\n" + keyCloud + "\n", label: "none"},
	} {
		t.Run(name, func(t *testing.T) {
			host := preparedHost(false)
			host.files[allowUbuntu] = "# empty\n"
			host.files[authorizedKeys] = c.authorized
			if c.own != "" {
				host.files[ownedKeys] = c.own
			}
			if c.theirs != "" {
				host.files[theirKeys] = c.theirs
			}
			cfg := fixture(t)
			s := cfg.Sites["home-a"]
			s.SSH.Keys = nil
			for _, k := range c.keys {
				who, _, _ := strings.Cut(strings.Fields(k)[2], "@")
				s.SSH.Keys = append(s.SSH.Keys, config.NamedKey{Name: who, Line: k})
			}
			cfg.Sites["home-a"] = s
			plan, err := hostprep.Build("home-a", cfg, host)
			if err != nil {
				t.Fatal(err)
			}
			if err := hostprep.Refusal(plan); err != nil {
				t.Fatalf("refused: %v\n%s", err, printed(plan))
			}
			if c.label != "none" && len(stepsLabelled(plan, c.label)) != 1 {
				t.Errorf("no %s step:\n%s", name, printed(plan))
			}
		})
	}
}

// The append checks the list again under the lock, run against real files:
// a list made between the plan and the run still refuses the key, and one
// holding it lets it in.
func TestTheAppendChecksTheListWhenItRuns(t *testing.T) {
	host := preparedHost(false)
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host)
	if err != nil {
		t.Fatal(err)
	}
	add := stepsLabelled(plan, "add")
	if len(add) != 1 {
		t.Fatalf("want bob's add:\n%s", printed(plan))
	}
	for name, list := range map[string]string{"listed": allowing(keyBob), "unlisted": allowing(keyAlice), "no list": ""} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"/ssh/authorized_keys": keyAlice + "\n", "/etc/authorized_keys.ubuntu.paisans-f2a9.owned": host.files[ownedKeys]}
			if list != "" {
				files["/etc/ssh-allow.ubuntu"] = list
			}
			if name == "unlisted" {
				defer func() {
					got, _ := os.ReadFile(dir + "/ssh/authorized_keys")
					if strings.Contains(string(got), keyBob) {
						t.Errorf("an unlisted key was appended:\n%s", got)
					}
				}()
				runFails(t, dir, add[0].Command, files, fingerprint(keyBob)+" is not in /etc/paisans/ssh-allow.ubuntu")
				return
			}
			run(t, dir, add[0].Command, files)
			got, _ := os.ReadFile(dir + "/ssh/authorized_keys")
			if !strings.Contains(string(got), keyBob) {
				t.Errorf("bob's key was not appended:\n%s", got)
			}
		})
	}
}

func TestParseAllow(t *testing.T) {
	fp := fingerprint(keyAlice)
	lines := hostprep.ParseAllow("# a comment\n\n" + fp + " alice\nSHA256:short bob\nnot-a-fingerprint\n  " + fingerprint(keyBob) + "\n")
	if len(lines) != 2 || lines[0] != (hostprep.AllowLine{Fingerprint: fp, Comment: "alice"}) || lines[1].Fingerprint != fingerprint(keyBob) {
		t.Errorf("%+v", lines)
	}
	if got := hostprep.ParseAllow(hostprep.EncodeAllow("ubuntu", lines)); len(got) != 2 || got[0] != lines[0] {
		t.Errorf("round trip %+v", got)
	}
}

// runFails is run for a command that must fail, saying want.
func runFails(t *testing.T, dir, command string, files map[string]string, want string) {
	t.Helper()
	script := strings.NewReplacer("/etc/paisans", dir+"/etc", "/home/ubuntu/.ssh", dir+"/ssh").Replace(unlocked(t, command))
	script = regexp.MustCompile(`ch(own|mod) --reference=\S+ \S+;`).ReplaceAllString(script, "")
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(dir+path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir+path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err == nil || !strings.Contains(string(out), strings.ReplaceAll(want, "/etc/paisans", dir+"/etc")) {
		t.Errorf("want a failure saying %q, got %v: %s", want, err, out)
	}
}

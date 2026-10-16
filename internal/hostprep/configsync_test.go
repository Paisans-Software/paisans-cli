package hostprep_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir with the user's configuration shut out.
func gitIn(t *testing.T, dir string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@invalid")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func bareRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "", "init", "--bare", "--quiet")
	return dir
}

// versionRefs is every ref under refs/ as "name sha" lines.
func allRefs(t *testing.T, dir string) string {
	t.Helper()
	return gitIn(t, dir, "", "for-each-ref", "--format=%(refname) %(objectname)", "refs/")
}

// The sync script runs for real against local repositories, with the host's
// configuration directory moved into a temporary one and each peer's git://
// address replaced by its directory. A peer that moved refs/versions/x to
// x's child and planted a quoted name gives this site only the child, under
// its own name; x stays where it is; and an honest peer afterwards still
// syncs.
func TestTheSyncScriptNeverMovesAVersion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("timeout"); err != nil {
		// A stand-in for coreutils' timeout where it is missing: it runs the
		// command without a limit.
		if err := os.WriteFile(filepath.Join(bin, "timeout"), []byte("#!/bin/sh\nshift\nexec \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(tmp, "config")
	site := bareRepo(t, filepath.Join(conf, "f2a9.git"))
	hostile := bareRepo(t, filepath.Join(tmp, "hostile", "f2a9.git"))
	honest := bareRepo(t, filepath.Join(tmp, "honest", "f2a9.git"))

	tree := gitIn(t, hostile, "", "mktree")
	x := gitIn(t, hostile, "", "commit-tree", tree, "-m", "x")
	y := gitIn(t, hostile, "", "commit-tree", tree, "-p", x, "-m", "y")
	for _, ref := range []string{"refs/versions/" + x, "refs/versions/" + y, "refs/versions/a'b", `refs/versions/c"d`} {
		gitIn(t, hostile, "", "update-ref", ref, y)
	}
	gitIn(t, honest, "", "fetch", "--quiet", hostile, y+":refs/versions/"+y)
	gitIn(t, honest, "", "update-ref", "refs/versions/"+x, x)
	gitIn(t, site, "", "fetch", "--quiet", honest, x+":refs/versions/"+x)

	script := ""
	for _, s := range storePlan(t, freshHost(), nil).Steps {
		if s.File != nil && s.File.Path == storeScript {
			script = s.File.Content
		}
	}
	if script == "" {
		t.Fatal("no sync script planned")
	}
	script = strings.ReplaceAll(script, "/srv/paisans/f2a9/.paisans/config", conf)
	script = strings.ReplaceAll(script, "git://", "")
	path := filepath.Join(tmp, "config-sync.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(peers ...string) string {
		t.Helper()
		var lines string
		for _, p := range peers {
			lines += filepath.Dir(p) + "\n"
		}
		if err := os.WriteFile(filepath.Join(conf, "f2a9.peers"), []byte(lines), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", path)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sync: %v: %s", err, out)
		}
		log, err := os.ReadFile(filepath.Join(conf, "f2a9.sync.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(log)
	}
	want := "refs/versions/" + x + " " + x + "\nrefs/versions/" + y + " " + y
	if x > y {
		want = "refs/versions/" + y + " " + y + "\nrefs/versions/" + x + " " + x
	}
	if log := run(hostile); !strings.Contains(log, `"ok":true`) {
		t.Fatalf("hostile peer sync log %s", log)
	}
	if got := allRefs(t, site); got != want {
		t.Fatalf("after the hostile peer:\n%s\nwant\n%s", got, want)
	}
	if log := run(honest); !strings.Contains(log, `"ok":true`) {
		t.Fatalf("an honest peer after a hostile one did not sync: %s", log)
	}
	if got := allRefs(t, site); got != want {
		t.Fatalf("after the honest peer:\n%s\nwant\n%s", got, want)
	}
}

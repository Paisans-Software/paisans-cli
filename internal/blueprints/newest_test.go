package blueprints

import (
	"os/exec"
	"strings"
	"testing"
)

// tagged is a repository whose one commit carries tags.
func tagged(t *testing.T, tags ...string) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "a")
	for _, tag := range tags {
		git("tag", tag)
	}
	return dir
}

// The newest release is the highest MAJOR.MINOR.PATCH, compared as numbers.
// Git's own version sort would pick v2.0.0-beta here, and v1.10.0-rc1 over
// v1.10.0; a pre-release is never chosen, and a tag that is not a version is
// ignored.
func TestNewestIsTheHighestRelease(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"v1.2.0", "v1.10.0", "v1.10.0-rc1", "v2.0.0-beta", "latest", "1.9.0"}, "v1.10.0"},
		{[]string{"v1.9.9", "v1.10.0-rc1"}, "v1.9.9"},
		{[]string{"1.2.0", "v1.3.0+build", "v01.4.0"}, "1.2.0"},
		{[]string{"v0.9.0", "v0.10.0", "v0.10.1"}, "v0.10.1"},
	}
	for _, c := range cases {
		tag, commit, err := Newest(tagged(t, c.tags...))
		if err != nil || tag != c.want || len(commit) != 40 {
			t.Errorf("%v: got %s %s %v, want %s", c.tags, tag, commit, err, c.want)
		}
	}
}

// A catalog with no release is refused, naming the tags it has, and so is
// one with no tags at all.
func TestNewestNeedsARelease(t *testing.T) {
	_, _, err := Newest(tagged(t, "v2.0.0-rc1", "latest"))
	if err == nil || !strings.Contains(err.Error(), "no release tag, only latest, v2.0.0-rc1") {
		t.Errorf("only pre-releases: %v", err)
	}
	_, _, err = Newest(tagged(t))
	if err == nil || !strings.Contains(err.Error(), "has no tags") {
		t.Errorf("no tags: %v", err)
	}
	// A pre-release is still pinned when named.
	if commit, err := TagCommit(tagged(t, "v2.0.0-rc1"), "v2.0.0-rc1"); err != nil || len(commit) != 40 {
		t.Errorf("a named pre-release: %s %v", commit, err)
	}
}

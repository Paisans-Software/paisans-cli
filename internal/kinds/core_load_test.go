package kinds

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
)

// The core catalog is read at its newest release, from the repository
// PAISANS_CORE_CATALOG names, with its foundation.
func TestTheCoreCatalogIsReadAtItsNewestRelease(t *testing.T) {
	coretest.Cache(t)
	t.Cleanup(forgetCore)
	forgetCore()
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	release, skipped := CoreRelease()
	want, _ := os.ReadFile(filepath.Join("..", "blueprints", "testdata", "core.release"))
	if release.Tag != strings.TrimSpace(string(want)) || len(release.Commit) != 40 || len(skipped) > 0 {
		t.Errorf("read %+v, skipped %v", release, skipped)
	}
	if _, ok := For("mbin"); !ok {
		t.Error("mbin is not known")
	}
	if image, err := FoundationImage(FoundationPocketID); err != nil || !strings.Contains(image, "pocket-id") {
		t.Errorf("the foundation's pocket-id is %q, %v", image, err)
	}
}

// coreRepo is a core catalog repository whose releases are the given tags,
// oldest first, each a commit of the test copy changed by its edit.
func coreRepo(t *testing.T, releases []string, edit map[string]func(dir string)) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "paisans-blueprints")
	copyTree(t, filepath.Join("..", "blueprints", "testdata", "core"), dir)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, tag := range releases {
		if e := edit[tag]; e != nil {
			e(dir)
		}
		run("add", "-A")
		run("commit", "-q", "--allow-empty", "-m", tag)
		run("tag", tag)
	}
	return "file://" + dir
}

func copyTree(t *testing.T, src, dest string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A newer release this binary cannot read is skipped, with why, and the
// newest one it can read is used: an older toolkit keeps working on the
// releases it understands.
func TestACoreReleaseThisBinaryCannotReadIsSkipped(t *testing.T) {
	coretest.Cache(t)
	url := coreRepo(t, []string{"v1.0.0", "v1.1.0", "v2.0.0"}, map[string]func(string){
		"v1.1.0": func(dir string) {
			// A track that leaves the Pocket ID API this toolkit speaks.
			p := filepath.Join(dir, FoundationFile)
			raw, _ := os.ReadFile(p)
			os.WriteFile(p, []byte(strings.Replace(string(raw), "pocket-id:v2.*.*", "pocket-id:v*.*.*", 1)), 0o644)
		},
		"v2.0.0": func(dir string) {
			// The foundation as it was, and a blueprint format this
			// toolkit does not read.
			f := filepath.Join(dir, FoundationFile)
			raw, _ := os.ReadFile(f)
			os.WriteFile(f, []byte(strings.Replace(string(raw), "pocket-id:v*.*.*", "pocket-id:v2.*.*", 1)), 0o644)
			p := filepath.Join(dir, "blueprints", "mbin", "blueprint.yaml")
			raw, _ = os.ReadFile(p)
			os.WriteFile(p, []byte(strings.Replace(string(raw), "paisans.blueprint/v1", "paisans.blueprint/v2", 1)), 0o644)
		},
	})
	t.Setenv("PAISANS_CORE_CATALOG", url)
	t.Cleanup(forgetCore)
	forgetCore()
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	release, skipped := CoreRelease()
	if release.Tag != "v1.0.0" || len(skipped) != 2 {
		t.Fatalf("read %+v, skipped %v", release, skipped)
	}
	if !strings.HasPrefix(skipped[0], "v2.0.0: ") || !strings.Contains(skipped[0], "apiVersion") {
		t.Errorf("v2.0.0 skipped as %q", skipped[0])
	}
	if !strings.HasPrefix(skipped[1], "v1.1.0: ") || !strings.Contains(skipped[1], "v2.") {
		t.Errorf("v1.1.0 skipped as %q", skipped[1])
	}
}

// A forge that cannot be reached falls back on the release this
// workstation last read, and says so.
func TestAnUnreachableCoreCatalogUsesTheLastRelease(t *testing.T) {
	coretest.Cache(t)
	url := coreRepo(t, []string{"v1.0.0"}, nil)
	t.Setenv("PAISANS_CORE_CATALOG", url)
	t.Cleanup(forgetCore)
	forgetCore()
	if err := LoadCore(""); err != nil {
		t.Fatal(err)
	}
	first, _ := CoreRelease()
	if err := os.RemoveAll(strings.TrimPrefix(url, "file://")); err != nil {
		t.Fatal(err)
	}
	forgetCore()
	if err := LoadCore(""); err != nil {
		t.Fatalf("with the forge gone: %v", err)
	}
	release, skipped := CoreRelease()
	if release != first || len(skipped) != 1 || !strings.Contains(skipped[0], "last read here") {
		t.Errorf("read %+v, skipped %v", release, skipped)
	}
	if _, ok := For("mbin"); !ok {
		t.Error("mbin is not known")
	}
}

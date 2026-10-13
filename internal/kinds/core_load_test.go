package kinds

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/blueprints/coretest"
)

// The core blueprints are read from the catalog cache at the binary's pin,
// and one whose files are not the ones the pin names is refused rather than
// rendered.
func TestACoreBlueprintIsCheckedAgainstThePin(t *testing.T) {
	cache := coretest.Cache(t)
	pin, err := blueprints.CorePin()
	if err != nil {
		t.Fatal(err)
	}
	// Replace the link to the shared copy with a copy of this test's own.
	shared, err := os.Readlink(filepath.Join(cache, pin.Commit))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cache, pin.Commit)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(shared, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(shared, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(forgetCore)

	forgetCore()
	if err := LoadCore(""); err != nil {
		t.Fatalf("the pinned copy: %v", err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if src, ok := Source("mbin"); !ok || !strings.HasPrefix(src, real) {
		t.Errorf("mbin read from %q, %v", src, ok)
	}

	f, err := os.OpenFile(filepath.Join(dir, "blueprints", "mbin", "blueprint.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("# edited\n")
	f.Close()
	forgetCore()
	if err := LoadCore(""); err == nil || !strings.Contains(err.Error(), "mbin") || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("an edited core blueprint: %v", err)
	}
	if _, ok := For("element"); ok {
		t.Error("a core catalog with an edited blueprint has blueprints known")
	}
}

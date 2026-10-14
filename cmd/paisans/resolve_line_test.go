package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/imagedigest"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// unpublished is a registry that does not have the mbin blueprint's default
// image: every other tag names one build.
type unpublished struct{}

func (unpublished) Digest(ref string) (string, error) {
	if def, _ := blueprints.DefaultImage(config.Blueprint("mbin"), "app"); ref != def {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	return "", fmt.Errorf("ghcr.io answered that %s does not exist: %w", ref, imagedigest.ErrNotFound)
}

// unresolvedPlan is the refusal apply plans freshSite's home-a with, against
// a registry that does not have its Mbin image.
func unresolvedPlan(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(freshSite), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	_, err = apply.Build("home-a", rendered, acme.Module(cfg.ACME.Provider), emptyHost{}, apply.ResolveImages(unpublished{}))
	if err == nil {
		t.Fatal("planned images no registry has")
	}
	return err
}

// The pull line is named for its site, and the refusal it ends with does
// not name the site again.
func TestAnUnresolvedImageNamesTheSiteOnce(t *testing.T) {
	refusal := unresolvedPlan(t)
	fakeConverge(t, nil)
	rec := recordConverge(t)
	fakePulls(t, nil, nil)
	convergePullPlan = func(_ *config.Config, _ *config.Secrets, site string, _ apply.Transport, _ ...apply.Option) (*apply.Plan, error) {
		if site == "home-a" {
			return nil, refusal
		}
		return &apply.Plan{Site: site}, nil
	}
	_ = converge(t)
	lines := ended(rec, "pull")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "fail home-a: ") })
	if i < 0 {
		t.Fatalf("home-a's pull line did not fail:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[i], "home-a: home-a") || strings.Count(lines[i], "home-a") != 1 {
		t.Errorf("the line names the site more than once: %s", lines[i])
	}
	if !strings.Contains(lines[i], "does not exist") {
		t.Errorf("the line does not say the image does not exist: %s", lines[i])
	}
}

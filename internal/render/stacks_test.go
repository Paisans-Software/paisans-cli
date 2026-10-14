package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// Two files the render writes at one path would leave one of them unwritten
// and the other reviewed in its place, so the render refuses rather than
// choose. An app named for the infrastructure stack is the way to get there
// past a configuration that was never loaded.
func TestTwoFilesAtOnePathAreRefused(t *testing.T) {
	cfg := fixture(t)
	// web is pinned to home-b, which runs the infrastructure stack too, and
	// keeps no secrets of its own.
	app := cfg.Apps["web"]
	cfg.Apps["infra"] = app
	_, err := render.Build(cfg, fixtureSecrets(t))
	if err == nil || !strings.Contains(err.Error(), "renders two files at") {
		t.Fatalf("an app named infra rendered: %v", err)
	}
}

// Only the infrastructure template's own services are marked as the
// foundation's in the infrastructure stack: a service it does not write,
// running a foundation image, is not.
func TestOnlyTheInfraTemplatesServicesAreMarked(t *testing.T) {
	cfg := fixture(t)
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	etcd, err := blueprints.FoundationImage(blueprints.FoundationEtcd)
	if err != nil {
		t.Fatal(err)
	}
	var files []render.File
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "/infra/compose.yaml") && strings.HasPrefix(f.Path, "vm/") {
			f.Content = strings.Replace(f.Content, "\nservices:\n", "\nservices:\n  backdoor:\n    image: "+etcd+"\n", 1)
			files = append(files, f)
		}
	}
	if len(files) != 1 || !strings.Contains(files[0].Content, "backdoor:") {
		t.Fatalf("no vm infra compose file to change: %d", len(files))
	}
	images, err := render.ListImages(cfg, fixtureSecrets(t), files)
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range images["vm"] {
		for _, s := range image.FoundationServices {
			if s == "backdoor" {
				t.Errorf("a service the infrastructure template does not write is marked: %+v", image)
			}
		}
	}
}

// infraServices names exactly the services the infrastructure template
// writes, so a service added to the template is not left unmarked, nor a
// name left over that the template no longer writes.
func TestInfraServicesAreTheTemplates(t *testing.T) {
	plan, err := render.Build(fixture(t), fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range plan.Files {
		if !strings.HasSuffix(f.Path, "/infra/compose.yaml") {
			continue
		}
		services, err := blueprints.ComposeMounts(f.Content)
		if err != nil {
			t.Fatal(err)
		}
		for name := range services {
			seen[name] = true
			if !render.InfraServices[name] {
				t.Errorf("the infrastructure template writes %s, which infraServices does not name", name)
			}
		}
	}
	for name := range render.InfraServices {
		if !seen[name] {
			t.Errorf("infraServices names %s, which no site's infrastructure stack runs", name)
		}
	}
}

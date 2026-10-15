package admission_test

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden requests")

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func build(t *testing.T, site string) admission.Request {
	t.Helper()
	r, err := admission.Build(fixture(t), site)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

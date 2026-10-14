package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/validate"
)

func TestDataSitesMustShareAnArchitecture(t *testing.T) {
	cfg := load(t, "valid")
	data := cfg.Cluster.Sites
	if len(data) < 2 {
		t.Skip("the valid fixture needs two data sites for this test")
	}
	set := func(name, arch string) {
		s := cfg.Sites[name]
		s.Arch = arch
		cfg.Sites[name] = s
	}

	set(data[0], "amd64")
	if validate.Check(cfg).Has("data-sites-differ-in-arch") {
		t.Fatal("one recorded and one unrecorded is not a difference")
	}

	set(data[1], "arm64")
	result := validate.Check(cfg)
	if !result.Has("data-sites-differ-in-arch") {
		t.Fatalf("did not fire: %v", result.Findings)
	}
	for _, f := range result.Findings {
		if f.Rule == "data-sites-differ-in-arch" && (!strings.Contains(f.Message, data[0]+" is amd64") || !strings.Contains(f.Message, data[1]+" is arm64")) {
			t.Errorf("message names neither site with its value: %s", f.Message)
		}
		if f.Rule == "data-sites-differ-in-arch" && !strings.Contains(f.Message, "delete that site's arch line and run again") {
			t.Errorf("message gives no next step: %s", f.Message)
		}
	}

	set(data[1], "amd64")
	if validate.Check(cfg).Has("data-sites-differ-in-arch") {
		t.Fatal("the same architecture fired")
	}
}

// Sites without the data role copy no database between them, so they mix.
func TestOtherSitesMayDifferInArchitecture(t *testing.T) {
	cfg := load(t, "valid")
	gw := cfg.GatewaySites()[0]
	s := cfg.Sites[gw]
	s.Arch = "arm64"
	cfg.Sites[gw] = s
	for _, name := range cfg.Cluster.Sites {
		if name == gw {
			t.Skip("the gateway holds the data role in this fixture")
		}
		d := cfg.Sites[name]
		d.Arch = "amd64"
		cfg.Sites[name] = d
	}
	if validate.Check(cfg).Has("data-sites-differ-in-arch") {
		t.Fatal("a gateway of another architecture was refused")
	}
}

package blueprints_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// A reference pins when two runs of apply would fetch the same bytes. The
// cases that matter are the ones that look pinned and are not.
func TestPinning(t *testing.T) {
	cases := []struct {
		ref    string
		pinned bool
	}{
		{"ghcr.io/mbinorg/mbin:v1.10.1", true},
		{"ghcr.io/requarks/wiki:2.5.316", true},
		{"ghcr.io/mbinorg/mbin@sha256:" + hex64, true},
		{"registry.example.org:5000/org/app:v1.2.3", true},
		{"ghcr.io/mbinorg/mbin:latest", false},
		{"ghcr.io/mbinorg/mbin", false},
		// A registry port is not a tag, so this names no build.
		{"registry.example.org:5000/org/app", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			ref := blueprints.ParseReference(tc.ref)
			if got := ref.Pinned(); got != tc.pinned {
				t.Fatalf("ParseReference(%q).Pinned() = %v, want %v (parsed as %+v)", tc.ref, got, tc.pinned, ref)
			}
			if tc.pinned && ref.Floating() != "" {
				t.Fatalf("a pinned reference explained itself as floating: %q", ref.Floating())
			}
			if !tc.pinned && ref.Floating() == "" {
				t.Fatal("a floating reference gave no reason, so the refusal would say nothing")
			}
		})
	}
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Every kind ships a pinned default for every service it declares, other than
// postgres, whose default is computed from cluster.postgres_version rather
// than fixed. An adopter who never opens the images stanza has to get a
// tested set, and none of those defaults may float, or the toolkit would
// refuse configuration it produces itself.
//
// It walks Builtin() and each kind's own blueprints.Services() rather than a
// list of service names written out here, so a kind given a record,
// or a kind whose services are not all named "app" (Eg: mbin's messenger),
// still gets checked: there is no second list this one can fall out of step
// with.
func TestDefaultsArePinned(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		services := blueprints.Services(blueprint)
		if len(services) == 0 {
			t.Errorf("%s ships no services", blueprint)
			continue
		}
		var sawFixedDefault bool
		for _, service := range services {
			if service.Name == blueprints.PostgresService {
				continue
			}
			ref, ok := blueprints.DefaultImage(blueprint, service.Name)
			if !ok {
				t.Errorf("%s ships no default image for its %s service", blueprint, service.Name)
				continue
			}
			if !blueprints.ParseReference(ref).Pinned() {
				t.Errorf("%s's %s service defaults to %q, which does not name one build", blueprint, service.Name, ref)
				continue
			}
			sawFixedDefault = true
		}
		if !sawFixedDefault {
			t.Errorf("%s ships no service with a fixed, pinned default image", blueprint)
		}
	}
}

// The database default is computed from cluster.postgres_version rather than
// fixed, so every database in a deployment is one major version.
func TestPostgresHasNoFixedDefault(t *testing.T) {
	if _, ok := blueprints.DefaultImage(config.Blueprint("mbin"), blueprints.PostgresService); ok {
		t.Fatal("postgres ships a fixed default, which would let two apps disagree about the major version")
	}
	if !blueprints.Has(config.Blueprint("mbin"), blueprints.PostgresService) {
		t.Fatal("postgres is not a declarable service, so a pinned app could not choose its database image")
	}
}

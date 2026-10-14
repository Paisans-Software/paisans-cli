package blueprints_test

import (
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
)

func TestComposeMountsReadsEverySyntax(t *testing.T) {
	compose := `
services:
  app:
    image: example/app:1
    tmpfs:
      - /app/var:size=256m,mode=0755
    volumes:
      - /srv/x/log:/app/var/log
      - /srv/x/oauth:/app/config/oauth2:ro
      - /anonymous
      - type: bind
        source: /srv/x/data
        target: /data
      - type: tmpfs
        target: /scratch
      - type: volume
        target: /also-anonymous
  sidecar:
    image: example/sidecar:1
    tmpfs: /run
`
	got, err := blueprints.ComposeMounts(compose)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]blueprints.ServiceMounts{
		"app":     {Image: "example/app:1", Targets: []string{"/app/var/log", "/app/config/oauth2", "/data", "/scratch", "/app/var"}},
		"sidecar": {Image: "example/sidecar:1", Targets: []string{"/run"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Only a mount at exactly the declared path covers it. A bind at a parent was
// observed to leave Docker's anonymous volume in place beneath it, and one at
// a child obviously does too.
func TestUncoveredIsExactMatchOnly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		declared  []string
		targets   []string
		uncovered []string
	}{
		{"exact, trailing slash ignored", []string{"/app/var/"}, []string{"/app/var"}, nil},
		{"a child does not cover", []string{"/app/var/"}, []string{"/app/var/log"}, []string{"/app/var"}},
		{"a parent does not cover", []string{"/var/lib/postgresql"}, []string{"/var/lib"}, []string{"/var/lib/postgresql"}},
		{"postgres 18 with the old target", []string{"/var/lib/postgresql"}, []string{"/var/lib/postgresql/data"}, []string{"/var/lib/postgresql"}},
		{"nothing declared", nil, []string{"/data"}, nil},
		{"several, sorted", []string{"/z", "/a", "/m"}, []string{"/m"}, []string{"/a", "/z"}},
	} {
		if got := blueprints.Uncovered(tc.declared, tc.targets); !reflect.DeepEqual(got, tc.uncovered) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.uncovered)
		}
	}
}

// The official image moved its declared volume, and its PGDATA, in 18. The
// image's own tag decides when it is the official image, since an app may pin
// an older one than the cluster runs.
func TestPostgresDataMountFollowsTheImage(t *testing.T) {
	for _, tc := range []struct{ image, cluster, want string }{
		{"postgres:16-trixie", "18", "/var/lib/postgresql/data"},
		{"postgres:17-trixie", "18", "/var/lib/postgresql/data"},
		{"postgres:18-trixie", "17", "/var/lib/postgresql"},
		{"postgres:16-alpine", "18", "/var/lib/postgresql/data"},
		{"postgres:17-alpine", "18", "/var/lib/postgresql/data"},
		{"docker.io/library/postgres:17.6", "18", "/var/lib/postgresql/data"},
		{"postgres:18-alpine", "18", "/var/lib/postgresql"},
		{"postgres:18-alpine", "17", "/var/lib/postgresql"},
		{"postgres:latest", "17", "/var/lib/postgresql/data"},
		{"ghcr.io/example-org/postgres:16", "18", "/var/lib/postgresql"},
		// postgres-walg is the official image plus WAL-G, tagged by major.
		{"ghcr.io/paisans-software/postgres-walg:17", "18", "/var/lib/postgresql/data"},
		{"ghcr.io/paisans-software/postgres-walg:18", "17", "/var/lib/postgresql"},
		{"", "17", "/var/lib/postgresql/data"},
	} {
		got := blueprints.PostgresDataMount(tc.image, tc.cluster)
		if got != tc.want {
			t.Errorf("%s at cluster %s: got %s, want %s", tc.image, tc.cluster, got, tc.want)
		}
	}
}

func TestRepositoryDropsTagAndDigest(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/paisans-software/caddy:2":            "ghcr.io/paisans-software/caddy",
		"ghcr.io/paisans-software/caddy:2@sha256:abc": "ghcr.io/paisans-software/caddy",
		"ghcr.io/paisans-software/caddy@sha256:abc":   "ghcr.io/paisans-software/caddy",
		"registry.example.org:5000/team/caddy:2":      "registry.example.org:5000/team/caddy",
		"registry.example.org:5000/team/caddy":        "registry.example.org:5000/team/caddy",
		"caddy":                                       "caddy",
	} {
		if got := blueprints.ParseReference(ref).Repository(); got != want {
			t.Errorf("%s: got %s, want %s", ref, got, want)
		}
	}
}

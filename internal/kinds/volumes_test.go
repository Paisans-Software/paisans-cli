package kinds_test

import (
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every image a kind ships by default has its declared volumes recorded. A
// bump changes the reference, the new one has no entry, and this fails until
// somebody inspects the image and records what it declares.
func TestEveryDefaultImageHasItsVolumesRecorded(t *testing.T) {
	for _, kind := range []config.Kind{config.KindMbin, config.KindOutline, config.KindPocketID, config.KindSynapse, config.KindElement, config.KindWriteFreely, config.KindOAuth2Proxy, config.KindUptime, config.KindInvite} {
		for _, service := range kinds.Services(kind) {
			if service.Image == "" {
				continue
			}
			if _, ok := kinds.DeclaredVolumes(service.Image); !ok {
				t.Errorf("%s %s runs %s, whose declared volumes are not in kinds.ImageVolumes. Record what it declares, then run `go test -tags registry_integration -run TestRecordedVolumesMatchEveryPlatform ./internal/kinds/`, which reads every platform from the registry and fails until the entry is right", kind, service.Name, service.Image)
			}
		}
	}
}

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
	got, err := kinds.ComposeMounts(compose)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]kinds.ServiceMounts{
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
		if got := kinds.Uncovered(tc.declared, tc.targets); !reflect.DeepEqual(got, tc.uncovered) {
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
		got := kinds.PostgresDataMount(tc.image, tc.cluster)
		if got != tc.want {
			t.Errorf("%s at cluster %s: got %s, want %s", tc.image, tc.cluster, got, tc.want)
		}
		if declared, ok := kinds.DeclaredVolumes(tc.image); ok {
			if u := kinds.Uncovered(declared, []string{got}); len(u) > 0 {
				t.Errorf("%s declares %v, and %s leaves %v uncovered", tc.image, declared, got, u)
			}
		}
	}
}

// The gateway's Caddy is recorded by repository, since apply renders
// whichever build its tag names: the tag as configured, a digest pinned
// beside it, and a bare digest all find the same entry. Another repository
// does not, nor does a reference that merely starts with the same name.
func TestDeclaredVolumesMatchesCaddyByRepository(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/paisans-software/caddy:2",
		"ghcr.io/paisans-software/caddy:2.11",
		"ghcr.io/paisans-software/caddy:2@sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186",
		"ghcr.io/paisans-software/caddy@sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186",
	} {
		if _, ok := kinds.DeclaredVolumes(ref); !ok {
			t.Errorf("%s is the toolkit's Caddy and was not found", ref)
		}
	}
	for _, ref := range []string{
		"ghcr.io/example-org/caddy-route53:2.11.4",
		"ghcr.io/paisans-software/caddy-extra:2",
		"caddy:2-alpine",
	} {
		if _, ok := kinds.DeclaredVolumes(ref); ok {
			t.Errorf("%s is not the toolkit's Caddy and was found", ref)
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
		if got := kinds.ParseReference(ref).Repository(); got != want {
			t.Errorf("%s: got %s, want %s", ref, got, want)
		}
	}
}

// The forks' channel tags move by design, so they are recorded by repository
// too, and the build apply pins beside a channel tag finds the same entry.
func TestDeclaredVolumesMatchesTheForkChannelsByRepository(t *testing.T) {
	for ref, want := range map[string][]string{
		"ghcr.io/paisans-software/mbin:stack-1": {"/app/var/"},
		"ghcr.io/paisans-software/mbin:stack-1@sha256:bb53cba4aaab5ca6ed973940830e985f6c149022936ceb2c5eca640205386d57": {"/app/var/"},
		"ghcr.io/josephquigley/writefreely-wisp:stack-1":                                                                nil,
	} {
		got, ok := kinds.DeclaredVolumes(ref)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: declared volumes %v (recorded %v), want %v", ref, got, ok, want)
		}
	}
}

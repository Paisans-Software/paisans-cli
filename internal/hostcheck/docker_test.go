package hostcheck_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// Where Docker came from does not matter, except a snap; how old it is
// does. Each case is the docker version probe's answer and dpkg's or snap's.
func TestDockerIsJudgedByVersionNotSource(t *testing.T) {
	for _, c := range []struct {
		name, docker, packages string
		refused                []string
	}{
		{"docker.io, current", "27.5.1 27.5.1\ncompose 2.33.1\n", "docker.io install ok installed\n", nil},
		{"docker.io, old", "24.0.7 24.0.7\ncompose 2.20.2\n", "docker.io install ok installed\n",
			[]string{"Docker Engine 24.0.7, and at least " + hostcheck.MinEngine, "Docker Compose 2.20.2, and at least " + hostcheck.MinCompose, "the host's owner run site admit again, which installs Docker"}},
		{"docker-ce, current", "28.5.1 28.5.1\ncompose 2.40.0\n", "docker-ce install ok installed\n", nil},
		{"docker-ce, old compose only", "27.5.1 27.5.1\ncompose v2.21.0\n", "docker-ce install ok installed\n",
			[]string{"Docker Compose 2.21.0, and at least " + hostcheck.MinCompose}},
		{"an old CLI", "27.5.1 20.10.24\ncompose 2.33.1\n", "docker-ce install ok installed\n",
			[]string{"the docker CLI 20.10.24, and at least " + hostcheck.MinEngine}},
		{"snap, however new", "28.4.0 28.4.0\ncompose 2.39.1\n", "Name    Version  Rev   Tracking       Publisher   Notes\ndocker  28.4.0   3265  latest/stable  canonical✓  -\n",
			[]string{"runs Docker from a snap", "snap remove docker", "the host's owner run site admit again, which installs Docker"}},
		// Compose missing is the host owner's to install, so the host
		// check lets it through and says so.
		{"compose missing", "27.5.1 27.5.1\ncompose absent\n", "docker.io install ok installed\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := cleanHost()
			h.answers["docker version"] = c.docker
			h.answers["dpkg-query"] = c.packages
			r := check(t, "home-a", h)
			err := r.Refusal()
			if c.refused == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("not refused")
			}
			for _, want := range c.refused {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal lacks %q:\n%s", want, err)
				}
			}
		})
	}
}

// --verbose shows the compose plugin beside the engine, or that it does not
// work.
func TestShowNamesTheComposeVersion(t *testing.T) {
	h := cleanHost()
	h.answers["docker version"] = "27.5.1 27.5.1\ncompose 2.33.1\n"
	wantLine(t, check(t, "home-a", h), "done: docker 27.5.1, compose 2.33.1 (docker-ce)")
	h.answers["docker version"] = "27.5.1 27.5.1\ncompose absent\n"
	wantLine(t, check(t, "home-a", h), "done: docker 27.5.1, `docker compose` does not work (docker-ce)")
}

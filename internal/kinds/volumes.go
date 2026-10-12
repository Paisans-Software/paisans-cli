package kinds

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/acme"
)

// ImageVolumes records the VOLUME paths each image this toolkit pins declares,
// as `docker image inspect --format '{{json .Config.Volumes}}'` printed them
// for linux/amd64 on 2026-10-07, after a `docker pull` of each exact
// reference. An empty list means the image declares none.
//
// A declared volume that the compose file does not mount at exactly that path
// gets an anonymous volume from Docker, a new one for every container, and
// Compose leaves the old one behind whenever it replaces a container. A real
// apps site collected 18 that way under Mbin's /app/var/. Every path here is
// therefore mounted by the kind's template, and a render test checks it.
//
// The table is keyed by the whole reference on purpose. Bumping an image
// changes its reference, the new one has no entry, and a test fails until
// somebody inspects it and records what it declares here, which is the
// moment a new VOLUME has to be noticed. The one image whose reference moves
// by design is recorded in RepositoryVolumes instead. `paisans apply` makes
// the same check against the host's copy of every image, which is what covers
// an operator's own `images` override; this table covers what the toolkit
// ships.
//
// Inspect with --platform linux/amd64 on a machine of another architecture:
// Docker's containerd image store otherwise answers for the native platform,
// and printed null for images that do declare volumes.
var ImageVolumes = map[string][]string{
	// Applications, the defaults in each kind's record.
	"docker.io/cloudamqp/amqproxy:3.2.0":                      nil,
	"docker.io/library/rabbitmq:3.13.7-management-alpine":     {"/var/lib/rabbitmq"},
	"docker.io/valkey/valkey:9.1.2-trixie":                    nil,
	"outlinewiki/outline:1.10.0":                              {"/var/lib/outline/data"},
	"ghcr.io/pocket-id/pocket-id:v2.14.0":                     nil,
	"ghcr.io/element-hq/synapse:v1.160.0":                     nil,
	"ghcr.io/element-hq/matrix-authentication-service:1.24.0": nil,
	"ghcr.io/element-hq/element-web:v1.12.27":                 nil,
	"quay.io/oauth2-proxy/oauth2-proxy:v7.15.4":               nil,
	// /data at 1.1.0-oidc.3, inspected as above; 1.1.0-oidc.4 is built from
	// the same Dockerfile, whose only VOLUME is /data, and is to be inspected
	// once published.
	"ghcr.io/paisans-software/uptime:1.1.0-oidc.4": {"/data"},
	// Distroless, declares none: `docker image inspect` printed null for this
	// exact tag on 2026-10-12.
	"ghcr.io/paisans-software/paisans-invite:0.1.0": nil,
	// Declares none: cmd/admin-reconciler/Dockerfile has no VOLUME, checked with
	// `docker image inspect` on a local build on 2026-10-08.
	"ghcr.io/paisans-software/admin-reconciler:0.1.0@sha256:008ca36e1637062bc1bde592b3a3997d9e0642e9fabed6264ce473be6ed6574d": nil,
	// A pinned app's own database, one per postgres_version the toolkit
	// knows. The path moved in 18; see PostgresDataMount. The Debian trixie
	// tags are the default (render.appImages); their VOLUME and PGDATA were
	// read with `docker image inspect` on 2026-10-11 and match the Alpine
	// tags', which stay for an app that pins one.
	"postgres:16-trixie": {"/var/lib/postgresql/data"},
	"postgres:17-trixie": {"/var/lib/postgresql/data"},
	"postgres:18-trixie": {"/var/lib/postgresql"},
	"postgres:16-alpine": {"/var/lib/postgresql/data"},
	"postgres:17-alpine": {"/var/lib/postgresql/data"},
	"postgres:18-alpine": {"/var/lib/postgresql"},
	// The infrastructure stack.
	"ghcr.io/zalando/spilo-16:3.3-p3":      nil,
	"ghcr.io/zalando/spilo-17:4.0-p3":      nil,
	"ghcr.io/zalando/spilo-18:4.1-p2":      nil,
	"gcr.io/etcd-development/etcd:v3.5.16": nil,
	"haproxy:3.0-alpine":                   nil,
	"dxflrs/garage:v1.0.1":                 nil,
	// The per site heartbeat (internal/render/heartbeat.go). curl's image
	// declares no VOLUME in its Dockerfile (curl/curl-container, alpine
	// variant); recorded from the Dockerfile rather than an inspect, which
	// is still to be run against the exact reference.
	"docker.io/curlimages/curl:8.11.1": nil,
}

// RepositoryVolumes is ImageVolumes for the images whose reference moves by
// design, keyed by repository rather than by reference, so any tag or digest
// of one answers.
//
// The gateway's Caddy is one such image: the toolkit names
// ghcr.io/paisans-software/caddy:2 and apply renders whichever build that tag
// names when it plans (see acme.Published), so no reference to it stays fixed
// for long enough to be a key. Every build comes from the same Dockerfile in
// Paisans-Software/caddy-dns, and the build the tag named on 2026-10-11
// (sha256:b401d1cb...) declared none, inspected as above. `paisans apply`
// inspects the exact build it deploys against the host's copy all the same,
// so a later build that declared a path is still refused before it runs.
//
// The two forks' channel tags are the others: the mbin and writefreely kinds
// name stack-1, which a maintainer moves to each promoted release
// (docs/specs/2026-10-11-stack-channels.md). Each fork builds every release
// from one Dockerfile. Mbin's declares /app/var/ (docker/Dockerfile:9 at
// Paisans-Software/mbin v1.15.0+paisans), and 1.15.0-paisans, its first
// promotion, declared exactly that when inspected as above on 2026-10-11.
// WriteFreely's declares none (Dockerfile at writefreely-wisp develop
// f95d840), and 0.21.0-wisp printed null inspected the same way.
var RepositoryVolumes = map[string][]string{
	acme.Repository:                          nil,
	"ghcr.io/paisans-software/mbin":          {"/app/var/"},
	"ghcr.io/josephquigley/writefreely-wisp": nil,
}

// DeclaredVolumes is what an image declares, as far as the toolkit has
// recorded it: by its whole reference in ImageVolumes, or else by its
// repository in RepositoryVolumes. ok is false for an image neither records,
// which is an operator's own and is checked against the host's copy at apply
// time instead.
func DeclaredVolumes(ref string) (paths []string, ok bool) {
	if paths, ok := ImageVolumes[ref]; ok {
		return paths, true
	}
	paths, ok = RepositoryVolumes[ParseReference(ref).Repository()]
	return paths, ok
}

// ServiceMounts is what one compose service mounts, and from which image.
type ServiceMounts struct {
	Image string
	// Targets are the container paths something persistent or deliberate is
	// mounted at: a bind, a named volume or a tmpfs. An anonymous volume the
	// file itself asks for (a bare `- /path`) is not one, since it leaks
	// exactly like the image's own.
	Targets []string
}

// ComposeMounts reads every service of a rendered compose file: its image,
// and the paths its volumes and tmpfs entries mount. Both compose syntaxes
// are read, the short `source:target[:mode]` string and the long mapping.
func ComposeMounts(content string) (map[string]ServiceMounts, error) {
	var doc struct {
		Services map[string]struct {
			Image   string      `yaml:"image"`
			Volumes []yaml.Node `yaml:"volumes"`
			Tmpfs   yaml.Node   `yaml:"tmpfs"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, err
	}
	out := map[string]ServiceMounts{}
	for name, service := range doc.Services {
		m := ServiceMounts{Image: service.Image}
		for _, v := range service.Volumes {
			target, ok, err := volumeTarget(v)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", name, err)
			}
			if ok {
				m.Targets = append(m.Targets, target)
			}
		}
		var tmpfs []string
		switch service.Tmpfs.Kind {
		case 0:
		case yaml.ScalarNode:
			tmpfs = []string{service.Tmpfs.Value}
		default:
			if err := service.Tmpfs.Decode(&tmpfs); err != nil {
				return nil, fmt.Errorf("service %s: tmpfs: %w", name, err)
			}
		}
		for _, entry := range tmpfs {
			target, _, _ := strings.Cut(entry, ":")
			m.Targets = append(m.Targets, target)
		}
		out[name] = m
	}
	return out, nil
}

// volumeTarget is the container path of one volumes entry, false for an
// anonymous volume.
func volumeTarget(v yaml.Node) (string, bool, error) {
	if v.Kind == yaml.ScalarNode {
		parts := strings.Split(v.Value, ":")
		if len(parts) == 1 {
			return "", false, nil
		}
		return parts[1], true, nil
	}
	var long struct {
		Type   string `yaml:"type"`
		Source string `yaml:"source"`
		Target string `yaml:"target"`
	}
	if err := v.Decode(&long); err != nil {
		return "", false, fmt.Errorf("volumes: %w", err)
	}
	if long.Type != "tmpfs" && long.Source == "" {
		return "", false, nil
	}
	return long.Target, true, nil
}

// Uncovered returns the declared volume paths no target mounts, cleaned and
// sorted.
//
// Only a mount at exactly the declared path counts. Docker skips creating an
// anonymous volume for a declared path only when the container already has a
// mount there; a bind at a parent leaves one at the declared path anyway,
// beneath it, which was seen on Docker 29.7.2 with a bind at /app and a
// declared /app/var/. A trailing slash is not a difference: Docker cleans
// the declared path, so /app/var/ is covered by a mount at /app/var.
func Uncovered(declared, targets []string) []string {
	have := map[string]bool{}
	for _, t := range targets {
		have[path.Clean(t)] = true
	}
	var out []string
	for _, d := range declared {
		if d = path.Clean(d); !have[d] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

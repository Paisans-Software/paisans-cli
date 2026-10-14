package blueprints

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

// The foundation. The core catalog's foundation.yaml names the image of
// every service whose template is the toolkit's own: the infrastructure
// stack's, and the reserved blueprints'. The binary names none, so a new
// release of any of them reaches a deployment without a toolkit release.
// docs/specs/2026-10-13-images-follow-tracks.md, "The foundation file".

// FoundationFile is the file's name at the core catalog's root.
const FoundationFile = "foundation.yaml"

// FoundationAPIVersion is the foundation format this toolkit reads.
const FoundationAPIVersion = "paisans.foundation/v1"

// The foundation's images, by the name foundation.yaml gives them.
const (
	FoundationEtcd               = "etcd"
	FoundationHAProxy            = "haproxy"
	FoundationGarage             = "garage"
	FoundationCaddy              = "caddy"
	FoundationHeartbeat          = "heartbeat"
	FoundationPocketID           = "pocket-id"
	FoundationOperatorReconciler = "operator-reconciler"
	FoundationOAuth2Proxy        = "oauth2-proxy"
	FoundationUptime             = "uptime"
	FoundationAccount            = "account"
)

// foundationNames are the images every foundation names.
var foundationNames = map[string]bool{
	FoundationEtcd: true, FoundationHAProxy: true, FoundationGarage: true, FoundationCaddy: true,
	FoundationHeartbeat: true, FoundationPocketID: true, FoundationOperatorReconciler: true,
	FoundationOAuth2Proxy: true, FoundationUptime: true, FoundationAccount: true,
}

// supportedTags is, for each image the toolkit's code is written against, the
// tags that code speaks to: a track must stay inside one of these prefixes,
// so a catalog cannot move a deployment onto an API this binary has never
// spoken to. An image absent here is one the toolkit only runs.
//
//   - pocket-id: internal/pocketid's REST client, written against v2.
//   - etcd: `etcdctl` v3, with the v3 API's flags.
//   - garage: its admin API and `garage` CLI, v1.
//   - haproxy: the haproxy.cfg template's syntax, 3.x.
//   - caddy: the Caddyfile the gateway is rendered with, Caddy 2.
//   - oauth2-proxy: its flags and environment, v7.
//   - spilo: Spilo's environment and Patroni's, the 3.x and 4.x lines.
var supportedTags = map[string][]string{
	FoundationPocketID:    {"v2."},
	FoundationEtcd:        {"v3."},
	FoundationGarage:      {"v1."},
	FoundationHAProxy:     {"3."},
	FoundationCaddy:       {"2."},
	FoundationOAuth2Proxy: {"v7."},
	"spilo":               {"3.", "4."},
}

// postgresMajors are the Postgres major versions the toolkit knows where to
// mount (PostgresDataMount) and how to upgrade between.
var postgresMajors = map[string]bool{"16": true, "17": true, "18": true}

// foundationSpec is foundation.yaml as written.
type foundationSpec struct {
	APIVersion string                   `yaml:"apiVersion"`
	Images     map[string]string        `yaml:"images"`
	Postgres   map[string]postgresImage `yaml:"postgres"`
}

// postgresImage is one Postgres major's images: Spilo for the cluster, and
// the image a pinned app's own database runs.
type postgresImage struct {
	Spilo string `yaml:"spilo"`
	App   string `yaml:"app"`
}

// foundation is the foundation this process read with the core catalog.
var foundation struct {
	sync.RWMutex
	spec *foundationSpec
}

// ParseFoundation reads a foundation.yaml and checks every image in it. A
// key this toolkit does not know is refused, as a blueprint's is.
func ParseFoundation(raw []byte) (*foundationSpec, error) {
	var s foundationSpec
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", FoundationFile, err)
	}
	if s.APIVersion != FoundationAPIVersion {
		return nil, fmt.Errorf("%s: apiVersion: is %q, and this toolkit reads %s", FoundationFile, s.APIVersion, FoundationAPIVersion)
	}
	for _, name := range sortedKeys(foundationNames) {
		if s.Images[name] == "" {
			return nil, fmt.Errorf("%s: images.%s: required", FoundationFile, name)
		}
	}
	for _, name := range sortedKeys(s.Images) {
		if !foundationNames[name] {
			return nil, fmt.Errorf("%s: images.%s: not an image this toolkit runs; it runs %s", FoundationFile, name, strings.Join(sortedKeys(foundationNames), ", "))
		}
		if err := checkImage(name, s.Images[name]); err != nil {
			return nil, fmt.Errorf("%s: images.%s: %w", FoundationFile, name, err)
		}
	}
	if len(s.Postgres) == 0 {
		return nil, fmt.Errorf("%s: postgres: names no major version", FoundationFile)
	}
	for _, major := range sortedKeys(s.Postgres) {
		if !postgresMajors[major] {
			return nil, fmt.Errorf("%s: postgres.%s: this toolkit runs Postgres %s", FoundationFile, major, strings.Join(sortedKeys(postgresMajors), ", "))
		}
		images := s.Postgres[major]
		if images.Spilo == "" || images.App == "" {
			return nil, fmt.Errorf("%s: postgres.%s: needs both spilo and app", FoundationFile, major)
		}
		if err := checkImage("spilo", images.Spilo); err != nil {
			return nil, fmt.Errorf("%s: postgres.%s.spilo: %w", FoundationFile, major, err)
		}
		if err := checkPostgresImage(major, images.App); err != nil {
			return nil, fmt.Errorf("%s: postgres.%s.app: %w", FoundationFile, major, err)
		}
	}
	return &s, nil
}

// checkImage checks one image of the foundation: a track whose pattern
// compiles, or a reference pinned to one build, and inside the tags the
// toolkit supports for it.
func checkImage(name, ref string) error {
	tag, err := imageTag(ref)
	if err != nil {
		return err
	}
	prefixes := supportedTags[name]
	if len(prefixes) == 0 {
		return nil
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(tag, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%s can move to tags outside %s, the versions this toolkit is written against. Fix the first part of the tag", ref, strings.Join(prefixes, " or "))
}

// checkPostgresImage checks a major's own database image: its tag must stay
// on that major, since a major is data and only postgres_version moves it.
func checkPostgresImage(major, ref string) error {
	tag, err := imageTag(ref)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(tag, major+".") && !strings.HasPrefix(tag, major+"-") && tag != major {
		return fmt.Errorf("%s can move off Postgres %s. Begin the tag with %s", ref, major, major)
	}
	return nil
}

// imageTag is the tag a foundation reference names, or its pattern when it
// is a track, which must compile. A reference with no tag is refused, since
// no tag means latest.
func imageTag(ref string) (string, error) {
	r := ParseReference(ref)
	if r.Digest != "" {
		r = ParseReference(r.Name)
	}
	if r.Tag == "" || r.Tag == "latest" {
		return "", fmt.Errorf("%s names no version: write a track (Eg: v2.*.*) or a tag", ref)
	}
	if imagedigest.IsTrack(ref) {
		if _, err := imagedigest.TrackPattern(ref); err != nil {
			return "", err
		}
	}
	return r.Tag, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readFoundation reads and checks the foundation of the core catalog
// checked out at dir.
func readFoundation(dir string) (*foundationSpec, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FoundationFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("it has no %s, which names the toolkit's own images", FoundationFile)
		}
		return nil, err
	}
	return ParseFoundation(raw)
}

// setFoundation records the foundation the core catalog was read with.
func setFoundation(s *foundationSpec) {
	foundation.Lock()
	defer foundation.Unlock()
	foundation.spec = s
}

// current is the foundation read with the core catalog, reading the core
// catalog first if it has not been.
func current() (*foundationSpec, error) {
	foundation.RLock()
	s := foundation.spec
	foundation.RUnlock()
	if s != nil {
		return s, nil
	}
	if err := LoadCore(""); err != nil {
		return nil, err
	}
	foundation.RLock()
	defer foundation.RUnlock()
	if foundation.spec == nil {
		return nil, fmt.Errorf("the core catalog was read with no %s", FoundationFile)
	}
	return foundation.spec, nil
}

// FoundationImage is the image the foundation names for one of the
// toolkit's own services (FoundationEtcd and the rest): a track, or a
// reference to one build.
func FoundationImage(name string) (string, error) {
	s, err := current()
	if err != nil {
		return "", err
	}
	image, ok := s.Images[name]
	if !ok {
		return "", fmt.Errorf("the core catalog's %s names no image %s", FoundationFile, name)
	}
	return image, nil
}

// SpiloImage is the image the infrastructure stack's Postgres runs for a
// major version.
func SpiloImage(major string) (string, error) {
	images, err := postgresImages(major)
	return images.Spilo, err
}

// PostgresImage is the image a pinned app's own database runs for a major
// version.
func PostgresImage(major string) (string, error) {
	images, err := postgresImages(major)
	return images.App, err
}

func postgresImages(major string) (postgresImage, error) {
	s, err := current()
	if err != nil {
		return postgresImage{}, err
	}
	images, ok := s.Postgres[major]
	if !ok {
		return postgresImage{}, fmt.Errorf("the core catalog's %s has no images for Postgres %s; it has %s", FoundationFile, major, strings.Join(sortedKeys(s.Postgres), ", "))
	}
	return images, nil
}

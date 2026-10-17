// Package applytest gives tests of the commands built on apply a registry
// without the network. It is imported only from test files, in a
// core_test.go beside coretest's:
//
//	import _ "github.com/paisans-software/paisans-cli/internal/apply/applytest"
//
// At init it sets apply.FreshImages to Registry, as `paisans` sets it to the
// real registry, so a command that starts a stack where none ran puts each
// track on a build.
package applytest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
	"gopkg.in/yaml.v3"
)

// Digest is the build Registry names for every tag.
const Digest = "sha256:" + "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0"

// Registry names one build for every tag, and puts a track on its first
// tag: the pattern with each `*` read as 0.
type Registry struct{}

func (Registry) Digest(string) (string, error) { return Digest, nil }

func (Registry) Track(ref string) (string, error) { return strings.ReplaceAll(ref, "*", "0"), nil }

func init() {
	apply.FreshImages = func() apply.Resolver { return Registry{} }
}

// Pinned is a rendered file as an apply through Registry leaves it on a
// host: each image line naming a track names its first tag at Digest
// instead. A test that seeds a host with rendered files seeds it with these,
// since no host holds a track.
func Pinned(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for i, line := range lines {
		body := strings.TrimRight(line, "\n")
		trimmed := strings.TrimLeft(body, " ")
		ref, quoted, ok := render.ImageLine(trimmed)
		if !ok || !strings.Contains(ref, "*") {
			continue
		}
		tagged, _ := Registry{}.Track(ref)
		to := tagged + "@" + Digest
		if quoted {
			to = `"` + to + `"`
		}
		lines[i] = body[:len(body)-len(trimmed)] + "image: " + to + line[len(body):]
	}
	return strings.Join(lines, "")
}

// Running answers apply.ContainersRead for a host holding files: one running
// container for every service of every stack's compose file under
// deployment.Base, as a host left by a finished apply runs them.
func Running(files map[string]string) string {
	var lines []string
	for path, content := range files {
		rest, ok := strings.CutPrefix(path, deployment.Base+"/")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || parts[2] != "compose.yaml" {
			continue
		}
		var doc struct {
			Services map[string]yaml.Node `yaml:"services"`
		}
		if yaml.Unmarshal([]byte(content), &doc) != nil {
			continue
		}
		for service := range doc.Services {
			lines = append(lines, fmt.Sprintf("paisans-%s-%s %s running\n", parts[0], parts[1], service))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "")
}

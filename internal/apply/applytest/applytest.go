// Package applytest gives tests of the commands built on apply a registry
// without the network. It is imported only from test files, in a
// core_test.go beside coretest's:
//
//	import _ "github.com/paisans-software/paisans-stack/internal/apply/applytest"
//
// At init it sets apply.FreshImages to Registry, as `paisans` sets it to the
// real registry, so a command that starts a stack where none ran puts each
// track on a build.
package applytest

import (
	"github.com/paisans-software/paisans-stack/internal/render"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/apply"
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

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/draftmerge"
)

// undoFlags names each change the draft makes against head that puts back a
// value an older version had, before a later revision changed it. For each
// path the draft changes it walks the trusted history along first parents
// from head to the latest revision that changed the path; when the draft
// holds the value that revision replaced (absent counts as a value), the
// path is flagged with the revision and the operator who signed it. Secrets
// are compared by key name only: a key added back or removed is flagged, a
// changed value never is, and no value is read into a flag.
func undoFlags(sys configstore.Local, g configstore.Gathered, head string, draft configstore.Files) ([]string, error) {
	cache, err := sys.Cache()
	if err != nil {
		return nil, err
	}
	var chain []string
	for sha, seen := head, map[string]bool{}; sha != "" && !seen[sha]; {
		seen[sha] = true
		v, ok := g.Versions[sha]
		if !ok {
			break
		}
		chain = append(chain, sha)
		sha = ""
		if len(v.Parents) > 0 {
			sha = v.Parents[0]
		}
	}
	// Each version's files are read once.
	read := map[string]pathValues{}
	valuesAt := func(sha string) (pathValues, error) {
		if v, ok := read[sha]; ok {
			return v, nil
		}
		f, err := cache.Files(sha)
		if err != nil {
			return pathValues{}, err
		}
		v, err := valuesOf(f)
		if err != nil {
			return pathValues{}, err
		}
		read[sha] = v
		return v, nil
	}
	if len(chain) == 0 {
		return nil, nil
	}
	mine, err := valuesOf(draft)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, domain := range []struct {
		label string
		of    func(pathValues) map[string]string
		mine  map[string]string
	}{
		{"", func(v pathValues) map[string]string { return v.config }, mine.config},
		{"secrets: ", func(v pathValues) map[string]string { return v.secrets }, mine.secrets},
	} {
		now, err := valuesAt(head)
		if err != nil {
			return nil, err
		}
		base := domain.of(now)
		flags := map[string]undo{}
		for path := range unionKeys(domain.mine, base) {
			if same(domain.mine, base, path) {
				continue
			}
			for i, sha := range chain {
				cur, err := valuesAt(sha)
				if err != nil {
					return nil, err
				}
				// The oldest version in the chain, a root or one whose parent
				// is not in the gather, has nothing before it to put back.
				if i+1 == len(chain) {
					break
				}
				before, err := valuesAt(chain[i+1])
				if err != nil {
					return nil, err
				}
				c, b := domain.of(cur), domain.of(before)
				if same(c, b, path) {
					continue
				}
				if same(domain.mine, b, path) {
					flags[path] = undo{sha: sha, how: how(domain.mine, c, b, path)}
				}
				break
			}
		}
		all := unionKeys(domain.mine, base)
		shown := collapse(flags, all)
		for _, path := range slices.Sorted(maps.Keys(shown)) {
			f := shown[path]
			v := g.Versions[f.sha]
			who := g.Signers[f.sha]
			if who == "" {
				who = v.SignerFP
			}
			out = append(out, fmt.Sprintf("%s%s: %s", domain.label, path, fmt.Sprintf(f.how, v.Revision, who)))
		}
	}
	return out, nil
}

// pathValues are one version's values: its configuration by dotted path, and
// the key names of its secrets, which carry no value.
type pathValues struct{ config, secrets map[string]string }

// undo is a flagged path: the revision that changed it and the wording, a
// format taking the revision and the signer.
type undo struct{ sha, how string }

// how is the wording of a flag for path, given the draft, the revision's
// version and the version before it.
func how(draft, at, before map[string]string, path string) string {
	_, inDraft := draft[path]
	_, inAt := at[path]
	_, inBefore := before[path]
	switch {
	case !inDraft && inAt && !inBefore:
		return "removed; revision %d (signed by %s) added it."
	case inDraft && !inAt && inBefore:
		return "added back; revision %d (signed by %s) removed it."
	}
	return "put back; revision %d (signed by %s) changed it."
}

// valuesOf is the dotted paths of a version's paisans-stack.yaml with their
// values, and the key names of its secrets, which carry no value.
func valuesOf(f configstore.Files) (pathValues, error) {
	var v pathValues
	var err error
	if v.config, err = draftmerge.Paths(f.Paisans); err != nil {
		return v, err
	}
	keys, err := flatKeys(f.Secrets)
	if err != nil {
		return v, err
	}
	v.secrets = map[string]string{}
	for k := range keys {
		v.secrets[k] = ""
	}
	return v, nil
}

func unionKeys(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// same reports whether path holds one value in a and b, absent in both
// counting as the same.
func same(a, b map[string]string, path string) bool {
	x, inA := a[path]
	y, inB := b[path]
	return inA == inB && x == y
}

// collapse is the paths to print with their flags. A flagged path is folded
// into the shortest ancestor under which every path of all is flagged for
// the same revision and wording, so a site put back is one line, not one per
// field.
func collapse(flags map[string]undo, all map[string]bool) map[string]undo {
	shown := map[string]undo{}
	for path, f := range flags {
		parts := strings.Split(path, ".")
		pick := path
		for n := 1; n < len(parts); n++ {
			prefix := strings.Join(parts[:n], ".")
			whole := true
			for p := range all {
				if p == prefix || strings.HasPrefix(p, prefix+".") {
					if g, ok := flags[p]; !ok || g != f {
						whole = false
						break
					}
				}
			}
			if whole {
				pick = prefix
				break
			}
		}
		shown[pick] = f
	}
	return shown
}

// Package draftmerge merges a draft forward: the three-way merge of
// paisans-stack.yaml and .sops.yaml between the version a draft was drafted
// from (base), the draft (ours) and the sites' newest version (theirs)
// (docs/specs/2026-10-24-system-copy.md, *Deploying a draft*). The secrets
// merge by key in configstore.MergeSecrets.
//
// A document merges path by path on its yaml.v3 node tree. A mapping key is
// a path segment; a sequence or a scalar is one value, compared by its
// canonical YAML, so a change of style or comment alone is no change. A path
// unchanged on one side takes the other's value; the same change on both
// sides is taken; a path removed on one side and unchanged on the other is
// removed. A path removed on one side and changed on the other, or changed
// differently on the two, is a conflict: the merge keeps ours there and
// lists it. Key order and the blank lines before keys come from ours, and
// so do comments, except one only theirs changed, which is theirs; a path
// only theirs adds is appended under its parent, in theirs' order, with
// theirs' blank line before it. A document with a YAML
// anchor, an alias, a merge key (<<) or a key given twice is refused.
package draftmerge

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config merges paisans-stack.yaml. merged holds ours at each path in
// conflicts, which are dotted paths, sorted. When nothing is taken from
// theirs, merged is ours byte for byte.
func Config(base, ours, theirs []byte) (merged []byte, conflicts []string, err error) {
	return run(base, ours, theirs)
}

// SOPS merges .sops.yaml the same way. creation_rules, a sequence, is one
// value: a list only one side changed is taken whole, and two different
// changes conflict as creation_rules, ours' whole list kept. A rule is never
// paired with another by its place, so a recipient one side removed is
// never put back by the other side's reordering. The keys beside
// creation_rules merge path by path.
func SOPS(base, ours, theirs []byte) (merged []byte, conflicts []string, err error) {
	return run(base, ours, theirs)
}

// Paths is every value of a YAML document by dotted path (JoinPath), as the
// merge compares them: a mapping key is a path segment, and a sequence, a scalar
// or an empty mapping is one value, its canonical YAML. A document that is
// not a mapping is one value at "".
func Paths(doc []byte) (map[string]string, error) {
	_, root, err := parse(doc, "the document")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if root == nil {
		return out, nil
	}
	return out, flatten(nil, root, out)
}

func flatten(path []string, n *yaml.Node, out map[string]string) error {
	if n.Kind == yaml.MappingNode && (len(n.Content) > 0 || len(path) == 0) {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if err := flatten(append(slices.Clip(path), n.Content[i].Value), n.Content[i+1], out); err != nil {
				return err
			}
		}
		return nil
	}
	c, err := canon(n)
	if err != nil {
		return err
	}
	out[JoinPath(path)] = c
	return nil
}

// JoinPath is the dotted path of a list of mapping keys: each key a
// segment, after a dot. A key that is empty, or holds a dot, a bracket or
// a double quote, is written ["key"] instead, quoted as Go quotes a string,
// so a key with a dot in it is never read as two segments.
func JoinPath(keys []string) string {
	var b strings.Builder
	for i, k := range keys {
		if k == "" || strings.ContainsAny(k, `.[]"`) {
			b.WriteString("[" + strconv.Quote(k) + "]")
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(k)
	}
	return b.String()
}

// SplitPath is the mapping keys of a path JoinPath wrote, and false for one
// it did not write.
func SplitPath(path string) ([]string, bool) {
	var keys []string
	for path != "" {
		if path[0] == '[' {
			ok := false
			for end := 1; end < len(path) && !ok; end++ {
				if path[end] != ']' {
					continue
				}
				if k, err := strconv.Unquote(path[1:end]); err == nil {
					keys, path, ok = append(keys, k), path[end+1:], true
				}
			}
			if !ok {
				return nil, false
			}
		} else {
			i := strings.IndexAny(path, ".[")
			if i < 0 {
				i = len(path)
			}
			if i == 0 {
				return nil, false
			}
			keys = append(keys, path[:i])
			path = path[i:]
		}
		if strings.HasPrefix(path, ".") {
			path = path[1:]
			if path == "" || strings.HasPrefix(path, "[") {
				return nil, false
			}
		}
	}
	return keys, true
}

// canon is n's value as YAML with no comment and no style of its own.
func canon(n *yaml.Node) (string, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return "", err
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// parse is doc's document node and its root, nil for an empty document.
// A document with an anchor, an alias, a merge key or a mapping key given
// twice is refused: the toolkit writes none, and a merge path by path cannot place
// what they share.
func parse(doc []byte, what string) (*yaml.Node, *yaml.Node, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(doc, &n); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", what, err)
	}
	if n.Kind != yaml.DocumentNode || len(n.Content) == 0 {
		return nil, nil, nil
	}
	if err := plain(&n); err != nil {
		return nil, nil, fmt.Errorf("%s %w", what, err)
	}
	return &n, n.Content[0], nil
}

// plain refuses a node tree holding an anchor, an alias, a merge key or a
// mapping key given twice.
func plain(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return fmt.Errorf("uses a YAML anchor or alias at line %d, which a merge cannot place; replace each alias with the value it names by hand, and remove the anchor", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if n.Content[i].Tag == "!!merge" {
				return fmt.Errorf("uses a YAML merge key (<<) at line %d, which a merge cannot place; write out the keys it merges in by hand, and remove it", n.Content[i].Line)
			}
			if seen[k] {
				return fmt.Errorf("gives the key %q twice at line %d", k, n.Content[i].Line)
			}
			seen[k] = true
		}
	}
	for _, c := range n.Content {
		if err := plain(c); err != nil {
			return err
		}
	}
	return nil
}

func run(base, ours, theirs []byte) ([]byte, []string, error) {
	switch {
	case bytes.Equal(ours, theirs), bytes.Equal(theirs, base):
		return ours, nil, nil
	case bytes.Equal(ours, base):
		return theirs, nil, nil
	}
	_, b, err := parse(base, "the base")
	if err != nil {
		return nil, nil, err
	}
	doc, o, err := parse(ours, "the draft")
	if err != nil {
		return nil, nil, err
	}
	_, t, err := parse(theirs, "the sites' version")
	if err != nil {
		return nil, nil, err
	}
	spaced := map[*yaml.Node]bool{}
	blankBefore(ours, o, spaced)
	blankBefore(theirs, t, spaced)
	m := &merger{}
	root := m.value(nil, b, o, t)
	if m.err != nil {
		return nil, nil, m.err
	}
	sort.Strings(m.conflicts)
	m.conflicts = slices.Compact(m.conflicts)
	if !m.changed {
		return ours, m.conflicts, nil
	}
	if root == nil {
		return nil, m.conflicts, nil
	}
	if doc == nil {
		doc = &yaml.Node{Kind: yaml.DocumentNode}
	}
	doc.Content = []*yaml.Node{root}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, nil, err
	}
	encoded, err := respace(out.Bytes(), root, spaced)
	if err != nil {
		return nil, nil, err
	}
	return encoded, m.conflicts, nil
}

// blankBefore marks in spaced each mapping key under n that src, the
// document n was parsed from, has a blank line before, above its comment.
// The encoder writes none, so respace puts them back.
func blankBefore(src []byte, n *yaml.Node, spaced map[*yaml.Node]bool) {
	if n == nil {
		return
	}
	lines := strings.Split(string(src), "\n")
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for i, c := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 {
				if above := top(c) - 2; above >= 0 && above < len(lines) && strings.TrimSpace(lines[above]) == "" {
					spaced[c] = true
				}
			}
			walk(c)
		}
	}
	walk(n)
}

// top is the first line of key k, its comment included.
func top(k *yaml.Node) int {
	if k.HeadComment == "" {
		return k.Line
	}
	return k.Line - strings.Count(k.HeadComment, "\n") - 1
}

// respace is out, the encoding of root, with a blank line before each key
// spaced marks. out is parsed again so each key's line is known; its tree
// is root's, node for node.
func respace(out []byte, root *yaml.Node, spaced map[*yaml.Node]bool) ([]byte, error) {
	if len(spaced) == 0 {
		return out, nil
	}
	_, again, err := parse(out, "the merge")
	if err != nil || again == nil {
		return out, err
	}
	blank := map[int]bool{}
	var walk func(m, a *yaml.Node)
	walk = func(m, a *yaml.Node) {
		if len(m.Content) != len(a.Content) {
			return
		}
		for i := range m.Content {
			if m.Kind == yaml.MappingNode && i%2 == 0 && spaced[m.Content[i]] {
				blank[top(a.Content[i])] = true
			}
			walk(m.Content[i], a.Content[i])
		}
	}
	walk(root, again)
	lines := strings.SplitAfter(string(out), "\n")
	var b strings.Builder
	for i, l := range lines {
		if blank[i+1] && i > 0 && strings.TrimSpace(lines[i-1]) != "" {
			b.WriteString("\n")
		}
		b.WriteString(l)
	}
	return []byte(b.String()), nil
}

// merger is one merge: the paths that conflict, whether anything was taken
// from theirs or removed, and the first error.
type merger struct {
	conflicts []string
	changed   bool
	err       error
}

// value merges the values at path, nil where a side has none, and returns
// the merged value, nil for none. Two mappings merge key by key.
func (m *merger) value(path []string, b, o, t *yaml.Node) *yaml.Node {
	switch {
	case o != nil && t != nil && o.Kind == yaml.MappingNode && t.Kind == yaml.MappingNode:
		if b != nil && b.Kind != yaml.MappingNode {
			b = nil
		}
		return m.mapping(path, b, o, t)
	}
	return m.leaf(path, b, o, t)
}

// leaf merges the values at path as one value each.
func (m *merger) leaf(path []string, b, o, t *yaml.Node) *yaml.Node {
	switch {
	case m.same(o, t), m.same(t, b):
		return o
	case m.same(o, b):
		m.changed = true
		return t
	}
	m.conflict(path, b, o, t)
	return o
}

// mapping merges two mappings key by key into o: ours' keys in ours'
// order, then the keys only theirs has, in theirs' order.
func (m *merger) mapping(path []string, b, o, t *yaml.Node) *yaml.Node {
	var content []*yaml.Node
	for i := 0; i+1 < len(o.Content); i += 2 {
		k, ov := o.Content[i], o.Content[i+1]
		bk, bv := entry(b, k.Value)
		tk, tv := entry(t, k.Value)
		m.comments(bk, k, tk)
		v := m.value(append(slices.Clip(path), k.Value), bv, ov, tv)
		if v == ov {
			m.comments(bv, ov, tv)
		}
		if v != nil {
			content = append(content, k, v)
		}
	}
	for i := 0; i+1 < len(t.Content); i += 2 {
		k := t.Content[i]
		if get(o, k.Value) != nil {
			continue
		}
		if v := m.value(append(slices.Clip(path), k.Value), get(b, k.Value), nil, t.Content[i+1]); v != nil {
			content = append(content, k, v)
		}
	}
	o.Content = content
	return o
}

// comments gives o each comment theirs changed and ours did not, b, o and
// t being one key or value as base, ours and theirs hold it. A side without
// it, or a comment changed on both, keeps ours.
func (m *merger) comments(b, o, t *yaml.Node) {
	if b == nil || o == nil || t == nil {
		return
	}
	for _, c := range []struct{ b, o, t *string }{
		{&b.HeadComment, &o.HeadComment, &t.HeadComment},
		{&b.LineComment, &o.LineComment, &t.LineComment},
		{&b.FootComment, &o.FootComment, &t.FootComment},
	} {
		if *c.o == *c.b && *c.t != *c.b {
			*c.o = *c.t
			m.changed = true
		}
	}
}

// conflict lists the paths under path that conflict: each value below it
// the two sides changed differently, or path itself.
func (m *merger) conflict(path []string, b, o, t *yaml.Node) {
	sides := make([]map[string]string, 3)
	for i, n := range []*yaml.Node{b, o, t} {
		sides[i] = map[string]string{}
		if n != nil {
			if err := flatten(slices.Clip(path), n, sides[i]); err != nil && m.err == nil {
				m.err = err
			}
		}
	}
	var found []string
	for _, p := range keys(sides) {
		bv, inB := sides[0][p]
		ov, inO := sides[1][p]
		tv, inT := sides[2][p]
		if (inO == inT && ov == tv) || (inO == inB && ov == bv) || (inT == inB && tv == bv) {
			continue
		}
		found = append(found, p)
	}
	if len(found) == 0 {
		found = []string{JoinPath(path)}
	}
	for _, p := range found {
		m.conflicts = append(m.conflicts, label(p))
	}
}

// label is a conflict's path as it is listed: the root of a document that
// is not a mapping is the whole file.
func label(path string) string {
	if path == "" {
		return "(whole file)"
	}
	return path
}

func keys(maps []map[string]string) []string {
	var out []string
	for _, side := range maps {
		for p := range side {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// same reports whether a and b are both absent, or the same value.
func (m *merger) same(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ca, err := canon(a)
	if err != nil {
		m.fail(err)
		return false
	}
	cb, err := canon(b)
	if err != nil {
		m.fail(err)
		return false
	}
	return ca == cb
}

func (m *merger) fail(err error) {
	if m.err == nil {
		m.err = err
	}
}

// get is the value under key in the mapping n, nil when there is none.
func get(n *yaml.Node, key string) *yaml.Node {
	_, v := entry(n, key)
	return v
}

// entry is the key node and value under key in the mapping n, nils when
// there is none.
func entry(n *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}

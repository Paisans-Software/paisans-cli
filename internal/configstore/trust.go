package configstore

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Graph is every version the workstation holds, with a way to read each
// version's admins and to check its digest.
type Graph struct {
	Versions map[string]Version
	Admins   func(sha string) (map[string]string, error) // fingerprint -> admin id, from that version's paisans.yaml
	Digest   func(sha string) error                      // CheckDigest of that version
}

// Verdict is what Judge decides about a Graph.
type Verdict struct {
	Valid   map[string]bool
	Reasons map[string]string // why a version is invalid
	Roots   []string          // valid roots
	Heads   []string          // valid tips, highest revision first
	Fork    bool              // more than one head
}

// Judge decides which versions the workstation trusts and which are the
// heads, by the rules of the spec's "Which versions the workstation trusts".
// The rules are applied in an order chosen so that no rule can be turned
// against another:
//
//  1. Each version on its own and against its parents (rules 1 and 2), parents
//     first. A version is valid only if every parent is.
//  2. The root (rule 4). It is settled before any removal is counted, so a
//     forged root that lists a real admin and then removes them cannot use
//     rule 3 to invalidate the real root and leave itself the only one.
//  3. Removals (rule 3), counted only from versions valid after steps 1 and 2.
//     The removals are collected once and then applied, so two admins who
//     remove each other on separate branches each lose their later versions,
//     and the result does not depend on which is looked at first.
//  4. Digests, on heads only, until no head fails. A version the operator's
//     age key cannot decrypt, published before they were a recipient, stays
//     valid as long as a version built on it verifies. A head that fails here
//     still counts as a removal in step 3: its signature and admins were
//     valid, and a removal is a fact of the signed history rather than of the
//     secrets this operator can read.
//
// Heads are ordered by revision, highest first, then by hash. Commit dates are
// never used: they are whatever the signer's clock said.
//
// The only error is more than one valid root with pinnedRoot unset. A graph
// with no versions gives an empty Verdict.
func Judge(g Graph, pinnedRoot string) (Verdict, error) {
	j := judgement{g: g, valid: map[string]bool{}, reasons: map[string]string{}, admins: map[string]adminsResult{}}
	order, cyclic := j.topo()
	for _, sha := range cyclic {
		j.invalid(sha, "its parents form a cycle")
	}
	for _, sha := range order {
		if reason := j.checkOwn(sha); reason != "" {
			j.invalid(sha, reason)
		} else {
			j.valid[sha] = true
		}
	}

	// Rule 4.
	var roots []string
	for _, sha := range order {
		if len(g.Versions[sha].Parents) == 0 && j.valid[sha] {
			roots = append(roots, sha)
		}
	}
	if pinnedRoot != "" {
		for _, sha := range order {
			v := g.Versions[sha]
			if len(v.Parents) == 0 && sha != pinnedRoot {
				j.invalid(sha, "a root other than the pinned root (rule 4)")
			}
		}
	} else if len(roots) > 1 {
		return Verdict{}, j.rootsError(roots)
	}
	j.propagate(order)

	// Rule 3.
	type removal struct {
		by    string          // the removing version
		fps   map[string]bool // every key the admin held in a parent
		admin string
	}
	var removals []removal
	for _, sha := range order {
		if !j.valid[sha] {
			continue
		}
		own := j.adminsOf(sha).m
		kept := map[string]bool{}
		for _, id := range own {
			kept[id] = true
		}
		gone := map[string]map[string]bool{}
		for _, p := range g.Versions[sha].Parents {
			for fp, id := range j.adminsOf(p).m {
				if !kept[id] {
					if gone[id] == nil {
						gone[id] = map[string]bool{}
					}
					gone[id][fp] = true
				}
			}
		}
		ids := make([]string, 0, len(gone))
		for id := range gone {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			removals = append(removals, removal{by: sha, fps: gone[id], admin: id})
		}
	}
	barred := map[string]string{}
	for _, r := range removals {
		anc := j.ancestors(r.by)
		for _, sha := range order {
			if _, done := barred[sha]; done || !j.valid[sha] || sha == r.by || anc[sha] {
				continue
			}
			if r.fps[g.Versions[sha].SignerFP] {
				barred[sha] = fmt.Sprintf("signed by %s after a version removed them (rule 3: removed in %s)", r.admin, r.by)
			}
		}
	}
	for sha, reason := range barred {
		j.invalid(sha, reason)
	}
	j.propagate(order)

	// Digests, on heads only.
	checked := map[string]bool{}
	for {
		failed := false
		for _, sha := range j.heads(order) {
			if checked[sha] {
				continue
			}
			checked[sha] = true
			if err := g.Digest(sha); err != nil {
				j.invalid(sha, fmt.Sprintf("its digest does not verify: %v", err))
				failed = true
			}
		}
		if !failed {
			break
		}
	}

	out := Verdict{Valid: j.valid, Reasons: j.reasons, Heads: j.heads(order)}
	for _, sha := range order {
		if len(g.Versions[sha].Parents) == 0 && j.valid[sha] {
			out.Roots = append(out.Roots, sha)
		}
	}
	sort.Slice(out.Heads, func(a, b int) bool {
		va, vb := g.Versions[out.Heads[a]], g.Versions[out.Heads[b]]
		if va.Revision != vb.Revision {
			return va.Revision > vb.Revision
		}
		return out.Heads[a] < out.Heads[b]
	})
	out.Fork = len(out.Heads) > 1
	return out, nil
}

type adminsResult struct {
	m   map[string]string
	err error
}

type judgement struct {
	g       Graph
	valid   map[string]bool
	reasons map[string]string
	admins  map[string]adminsResult
}

func (j *judgement) invalid(sha, reason string) {
	if j.valid[sha] || j.reasons[sha] == "" {
		j.reasons[sha] = reason
	}
	j.valid[sha] = false
}

func (j *judgement) adminsOf(sha string) adminsResult {
	if r, ok := j.admins[sha]; ok {
		return r
	}
	m, err := j.g.Admins(sha)
	j.admins[sha] = adminsResult{m, err}
	return j.admins[sha]
}

// topo returns the held versions with every parent before its children, ties
// broken by hash so the order never depends on map iteration, and separately
// those that can never be ordered because they are on or below a cycle.
func (j *judgement) topo() (order, cyclic []string) {
	pending := map[string]int{}
	children := map[string][]string{}
	for sha, v := range j.g.Versions {
		seen := map[string]bool{}
		for _, p := range v.Parents {
			if _, held := j.g.Versions[p]; held && !seen[p] {
				seen[p] = true
				pending[sha]++
				children[p] = append(children[p], sha)
			}
		}
	}
	var ready []string
	for sha := range j.g.Versions {
		if pending[sha] == 0 {
			ready = append(ready, sha)
		}
	}
	for len(ready) > 0 {
		sort.Strings(ready)
		sha := ready[0]
		ready = ready[1:]
		order = append(order, sha)
		for _, c := range children[sha] {
			if pending[c]--; pending[c] == 0 {
				ready = append(ready, c)
			}
		}
	}
	for sha := range j.g.Versions {
		if pending[sha] > 0 {
			cyclic = append(cyclic, sha)
		}
	}
	sort.Strings(cyclic)
	return order, cyclic
}

// checkOwn applies rules 1 and 2 to one version whose parents are already
// judged, and returns why it is invalid, or "" when it is valid. A version
// whose own paisans.yaml cannot be read is invalid too: its admins decide
// whether its children are, and whom it removes.
func (j *judgement) checkOwn(sha string) string {
	v := j.g.Versions[sha]
	if !v.TreeOK {
		return "its tree does not hold exactly the three files (rule 1)"
	}
	if !v.Signed {
		return "it has no good signature from a known key (rule 1)"
	}
	want := 1
	for _, p := range v.Parents {
		pv, held := j.g.Versions[p]
		if !held {
			return fmt.Sprintf("parent %s is not held", p)
		}
		if pv.Revision+1 > want {
			want = pv.Revision + 1
		}
	}
	if v.Revision != want {
		return fmt.Sprintf("its revision is %d, not %d (rule 1)", v.Revision, want)
	}
	for _, p := range v.Parents {
		if !j.valid[p] {
			return fmt.Sprintf("parent %s is not valid (rule 2)", p)
		}
	}
	own := j.adminsOf(sha)
	if own.err != nil {
		return fmt.Sprintf("its admins cannot be read: %v (rule 2)", own.err)
	}
	if len(v.Parents) == 0 {
		if _, ok := own.m[v.SignerFP]; !ok {
			return fmt.Sprintf("signed by %s, which is not a key of an admin it lists (rule 2)", v.SignerFP)
		}
		return ""
	}
	for _, p := range v.Parents {
		pa := j.adminsOf(p)
		if pa.err != nil {
			return fmt.Sprintf("the admins of parent %s cannot be read: %v (rule 2)", p, pa.err)
		}
		if _, ok := pa.m[v.SignerFP]; !ok {
			return fmt.Sprintf("signed by %s, which is not a key of an admin in parent %s (rule 2)", v.SignerFP, p)
		}
	}
	return ""
}

// propagate makes every version with an invalid parent invalid, in order.
func (j *judgement) propagate(order []string) {
	for _, sha := range order {
		if !j.valid[sha] {
			continue
		}
		for _, p := range j.g.Versions[sha].Parents {
			if !j.valid[p] {
				j.invalid(sha, fmt.Sprintf("parent %s is not valid (rule 2)", p))
				break
			}
		}
	}
}

// ancestors returns every held version reachable from sha through parents,
// not counting sha itself.
func (j *judgement) ancestors(sha string) map[string]bool {
	seen := map[string]bool{}
	stack := append([]string(nil), j.g.Versions[sha].Parents...)
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		v, held := j.g.Versions[p]
		if !held || seen[p] {
			continue
		}
		seen[p] = true
		stack = append(stack, v.Parents...)
	}
	return seen
}

// heads returns the valid versions that no valid version builds on.
func (j *judgement) heads(order []string) []string {
	built := map[string]bool{}
	for _, sha := range order {
		if j.valid[sha] {
			for _, p := range j.g.Versions[sha].Parents {
				built[p] = true
			}
		}
	}
	var out []string
	for _, sha := range order {
		if j.valid[sha] && !built[sha] {
			out = append(out, sha)
		}
	}
	return out
}

func (j *judgement) rootsError(roots []string) error {
	sort.Strings(roots)
	lines := make([]string, 0, len(roots))
	for _, sha := range roots {
		v := j.g.Versions[sha]
		who := j.adminsOf(sha).m[v.SignerFP]
		lines = append(lines, fmt.Sprintf("  %s signed by %s (%s) on %s", sha, who, v.SignerFP, v.Committed.UTC().Format(time.RFC3339)))
	}
	return fmt.Errorf("the store holds %d valid roots; pin one with --root <hash>:\n%s", len(roots), strings.Join(lines, "\n"))
}

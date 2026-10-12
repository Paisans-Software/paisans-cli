package configstore

import (
	"errors"
	"fmt"
	"sort"
)

// Graph is every version the workstation holds, with a way to read each
// version's admins.
type Graph struct {
	Versions map[string]Version
	Admins   func(sha string) (map[string]string, error) // fingerprint -> admin id, from that version's paisans.yaml
}

// Anchor is what the operator holds that nobody else can forge: their own
// signing key, a pinned root, or both.
type Anchor struct {
	Signer string // the operator's key fingerprint, from .paisans/signing-key; may be empty
	Root   string // a pinned root sha; may be empty
}

// Verdict is what Judge decides about a Graph.
type Verdict struct {
	Trusted  map[string]bool
	Reasons  map[string]string
	AnchorAt string   // the version trust starts from
	Heads    []string // highest revision first, then sha
	Fork     bool
}

// ErrNoAnchor is returned when trust has nowhere to start. A history found
// only on the sites, however consistent, is one someone with root on those
// sites could have written and signed with their own key.
var ErrNoAnchor = errors.New("no version is signed by your key and no root is pinned")

// anchorError explains why there is no anchor in a particular case, and is
// ErrNoAnchor to errors.Is.
type anchorError struct{ msg string }

func (e *anchorError) Error() string        { return e.msg }
func (e *anchorError) Is(target error) bool { return target == ErrNoAnchor }

// ConflictError is two versions outside the anchor's history whose removals
// bar each other, directly or through a chain of such. Nothing decides between
// them by revision or hash, since whoever signs chooses both; the operator
// does.
type ConflictError struct{ A, B string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("versions %s and %s each remove a key the other depends on, and neither is in your anchor's history; choose one by publishing on it", e.A, e.B)
}

// Judge decides which versions to trust, starting from the operator's anchor,
// by the spec's "Which versions the workstation trusts":
//
//   - The anchor is the operator's newest version that passes shape, or else
//     the pinned root. With both, the signed anchor must descend from the
//     pin. The anchor and every ancestor of it are trusted whatever their
//     shape, because the operator signed after them.
//   - Removals in that history always stand, and bar the versions signed by
//     each removed key that are not ancestors of the removal.
//   - Every other version is a candidate when it passes shape, is not a root,
//     has only trusted or candidate parents, is signed by a key of an admin in
//     every parent, and is not barred by the history.
//   - A candidate's removals bar other candidates, and with them every
//     candidate built on a barred one. A candidate nobody undecided bars is
//     trusted and its removals stand; one barred by a trusted candidate is
//     not trusted and removes nothing; repeat until nothing changes. What is
//     left bars itself in a loop, and is a *ConflictError.
//
// The result does not depend on map order, and Judge terminates on any graph,
// including one with a missing parent or a cycle.
func Judge(g Graph, a Anchor) (Verdict, error) {
	j := &judgement{g: g, admins: map[string]adminsResult{}, anc: map[string]map[string]bool{}}
	order, cyclic := j.topo()

	anchor, err := j.anchor(a, order)
	if err != nil {
		return Verdict{}, err
	}
	history := map[string]bool{anchor: true}
	for sha := range j.ancestors(anchor) {
		history[sha] = true
	}

	out := Verdict{Trusted: map[string]bool{}, Reasons: map[string]string{}, AnchorAt: anchor}
	for _, sha := range cyclic {
		out.Trusted[sha] = false
		out.Reasons[sha] = "its ancestry contains a cycle (shape)"
	}

	// Bars from the anchor's history.
	barredBy := map[string]string{} // version -> reason
	for _, r := range order {
		if !history[r] {
			continue
		}
		for _, sha := range order {
			if history[sha] || barredBy[sha] != "" {
				continue
			}
			if reason := j.bars(r, sha); reason != "" {
				barredBy[sha] = reason
			}
		}
	}

	// Candidates.
	cand := map[string]bool{}
	for _, sha := range order {
		if history[sha] {
			out.Trusted[sha] = true
			continue
		}
		reason := j.extend(sha, history, cand)
		if reason == "" {
			reason = barredBy[sha]
		}
		if reason != "" {
			out.Trusted[sha] = false
			out.Reasons[sha] = reason
			continue
		}
		cand[sha] = true
	}

	// Which candidate's removals bar which: R attacks D when R bars D or an
	// ancestor of D.
	attackers := map[string][]string{}
	direct := map[string]map[string]string{} // barred -> remover -> reason
	for _, r := range order {
		if !cand[r] {
			continue
		}
		for _, v := range order {
			if !cand[v] {
				continue
			}
			if reason := j.bars(r, v); reason != "" {
				if direct[v] == nil {
					direct[v] = map[string]string{}
				}
				direct[v][r] = reason
			}
		}
	}
	for _, d := range order {
		if !cand[d] {
			continue
		}
		for _, r := range order {
			if !cand[r] {
				continue
			}
			for v := range direct {
				if direct[v][r] != "" && (v == d || j.ancestors(d)[v]) {
					attackers[d] = append(attackers[d], r)
					break
				}
			}
		}
	}

	const undecided, in, outed = 0, 1, -1
	status := map[string]int{}
	for changed := true; changed; {
		changed = false
		for _, x := range order {
			if !cand[x] || status[x] != undecided {
				continue
			}
			anyIn, allOut := false, true
			for _, r := range attackers[x] {
				anyIn = anyIn || status[r] == in
				allOut = allOut && status[r] == outed
			}
			switch {
			case anyIn:
				status[x], changed = outed, true
			case allOut:
				status[x], changed = in, true
			}
		}
	}
	if c := conflict(order, cand, status, attackers); c != nil {
		return Verdict{}, c
	}

	for _, sha := range order {
		if !cand[sha] {
			continue
		}
		if status[sha] == in {
			out.Trusted[sha] = true
			continue
		}
		out.Trusted[sha] = false
		for _, r := range order {
			if status[r] == in && direct[sha][r] != "" {
				out.Reasons[sha] = direct[sha][r]
				break
			}
		}
		if out.Reasons[sha] == "" {
			for _, p := range j.g.Versions[sha].Parents {
				if !out.Trusted[p] {
					out.Reasons[sha] = fmt.Sprintf("parent %s is not trusted (signer)", p)
					break
				}
			}
		}
	}

	built := map[string]bool{}
	for _, sha := range order {
		if out.Trusted[sha] {
			for _, p := range g.Versions[sha].Parents {
				built[p] = true
			}
		}
	}
	for _, sha := range order {
		if out.Trusted[sha] && !built[sha] {
			out.Heads = append(out.Heads, sha)
		}
	}
	sort.Slice(out.Heads, func(x, y int) bool {
		vx, vy := g.Versions[out.Heads[x]], g.Versions[out.Heads[y]]
		if vx.Revision != vy.Revision {
			return vx.Revision > vy.Revision
		}
		return out.Heads[x] < out.Heads[y]
	})
	out.Fork = len(out.Heads) > 1
	return out, nil
}

// conflict names two candidates left undecided that bar each other, directly
// or through a chain. Each undecided candidate has an undecided attacker, so
// following them from the lowest hash must come back round.
func conflict(order []string, cand map[string]bool, status map[string]int, attackers map[string][]string) *ConflictError {
	var left []string
	for _, sha := range order {
		if cand[sha] && status[sha] == 0 {
			left = append(left, sha)
		}
	}
	if len(left) == 0 {
		return nil
	}
	sort.Strings(left)
	next := func(x string) string {
		var rs []string
		for _, r := range attackers[x] {
			if status[r] == 0 {
				rs = append(rs, r)
			}
		}
		sort.Strings(rs)
		return rs[0]
	}
	seen := map[string]bool{}
	x := left[0]
	for !seen[x] {
		seen[x] = true
		x = next(x)
	}
	a, b := x, next(x)
	if b < a {
		a, b = b, a
	}
	return &ConflictError{A: a, B: b}
}

type adminsResult struct {
	m   map[string]string
	err error
}

type judgement struct {
	g      Graph
	admins map[string]adminsResult
	anc    map[string]map[string]bool
}

func (j *judgement) adminsOf(sha string) adminsResult {
	if r, ok := j.admins[sha]; ok {
		return r
	}
	m, err := j.g.Admins(sha)
	j.admins[sha] = adminsResult{m, err}
	return j.admins[sha]
}

// anchor picks where trust starts.
func (j *judgement) anchor(a Anchor, order []string) (string, error) {
	signed := ""
	if a.Signer != "" {
		for _, sha := range order {
			v := j.g.Versions[sha]
			if v.SignerFP != a.Signer || j.shape(sha) != "" {
				continue
			}
			if signed == "" || v.Revision > j.g.Versions[signed].Revision ||
				(v.Revision == j.g.Versions[signed].Revision && sha < signed) {
				signed = sha
			}
		}
	}
	if a.Root != "" && signed != "" {
		if a.Root != signed && !j.ancestors(signed)[a.Root] {
			return "", &anchorError{fmt.Sprintf("your newest version %s does not descend from the pinned root %s", signed, a.Root)}
		}
		return signed, nil
	}
	if signed != "" {
		return signed, nil
	}
	if a.Root != "" {
		if reason := j.goodRoot(a.Root); reason != "" {
			return "", &anchorError{fmt.Sprintf("the pinned root %s %s, and no version is signed by your key", a.Root, reason)}
		}
		return a.Root, nil
	}
	return "", ErrNoAnchor
}

// goodRoot says why sha cannot anchor as a pinned root, or "" when it can.
func (j *judgement) goodRoot(sha string) string {
	v, held := j.g.Versions[sha]
	switch {
	case !held:
		return "is not held"
	case len(v.Parents) > 0:
		return "is not a root"
	case j.shape(sha) != "":
		return "fails shape"
	}
	own := j.adminsOf(sha)
	if own.err != nil {
		return fmt.Sprintf("has unreadable admins: %v", own.err)
	}
	if _, ok := own.m[v.SignerFP]; !ok {
		return "is not signed by an admin it lists"
	}
	return ""
}

// shape says why a version fails rule 1, or "" when it passes.
func (j *judgement) shape(sha string) string {
	v := j.g.Versions[sha]
	if !v.TreeOK {
		return "its tree does not hold exactly the three files (shape)"
	}
	if !v.Signed {
		return "it has no good signature from a known key (shape)"
	}
	want := 1
	for _, p := range v.Parents {
		pv, held := j.g.Versions[p]
		if !held {
			return fmt.Sprintf("parent %s is not held (shape)", p)
		}
		if pv.Revision+1 > want {
			want = pv.Revision + 1
		}
	}
	if v.Revision != want {
		return fmt.Sprintf("its revision is %d, not %d (shape)", v.Revision, want)
	}
	return ""
}

// extend says why a version outside the anchor's history cannot be a
// candidate, or "" when it can. A version whose own paisans.yaml cannot be
// read is not trusted: its admins decide its children and its removals.
func (j *judgement) extend(sha string, history, cand map[string]bool) string {
	if reason := j.shape(sha); reason != "" {
		return reason
	}
	v := j.g.Versions[sha]
	if len(v.Parents) == 0 {
		return "a root outside the anchor's history (anchor)"
	}
	for _, p := range v.Parents {
		if !history[p] && !cand[p] {
			return fmt.Sprintf("parent %s is not trusted (signer)", p)
		}
	}
	if own := j.adminsOf(sha); own.err != nil {
		return fmt.Sprintf("its admins cannot be read: %v (signer)", own.err)
	}
	for _, p := range v.Parents {
		pa := j.adminsOf(p)
		if pa.err != nil {
			return fmt.Sprintf("the admins of parent %s cannot be read: %v (signer)", p, pa.err)
		}
		if _, ok := pa.m[v.SignerFP]; !ok {
			return fmt.Sprintf("signed by %s, which is not a key of an admin in parent %s (signer)", v.SignerFP, p)
		}
	}
	return ""
}

// bars says why removals in r bar v, or "" when they do not: v is signed by a
// key one of r's parents listed and r does not, and v is neither r nor an
// ancestor of it. A version with unreadable admins is taken to remove nothing,
// since what it lists is unknown.
func (j *judgement) bars(r, v string) string {
	if r == v || j.ancestors(r)[v] {
		return ""
	}
	fp := j.g.Versions[v].SignerFP
	own := j.adminsOf(r)
	if own.err != nil {
		return ""
	}
	if _, kept := own.m[fp]; kept {
		return ""
	}
	for _, p := range j.g.Versions[r].Parents {
		if id, listed := j.adminsOf(p).m[fp]; listed {
			return fmt.Sprintf("signed by %s, a key of %s that %s removed (removal)", fp, id, r)
		}
	}
	return ""
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

// ancestors returns every held version reachable from sha through parents,
// not counting sha itself unless it is on a cycle.
func (j *judgement) ancestors(sha string) map[string]bool {
	if a, ok := j.anc[sha]; ok {
		return a
	}
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
	j.anc[sha] = seen
	return seen
}

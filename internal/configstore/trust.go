package configstore

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Graph is every version the workstation holds, with a way to read each
// version's operators.
type Graph struct {
	Versions  map[string]Version
	Operators func(sha string) (map[string]string, error) // fingerprint -> operator id, from that version's paisans-stack.yaml
	// Served is the versions the sites answered with in this gather. The
	// cache keeps every version any gather saw, so whether the sites hold a
	// pinned root's history is read from Served alone. Nil or empty is no
	// evidence either way.
	Served map[string]bool
}

// Anchor is what the operator holds that nobody else can forge: their own
// signing keys, a pinned root, or both. Signers is a set of fingerprints:
// the key .paisans/signing-key records, or, with Named, the key the operator
// names with --key on a workstation that records none. Signers never come
// from the sites or the ssh-agent.
//
// A recorded key is the operator's by their own choice, on a workstation
// that also pins the root its history grew from, so its newest version
// anchors whatever its history. A named key comes with no such pin: it may
// be one a later version removed, and an empty workstation cannot tell this
// deployment's history from one the key signed before a redeploy under the
// same id, whose other operators may have written more since. So a named key
// anchors only on versions it signed itself, under one root, where the trust
// rules trust them already.
type Anchor struct {
	Signers []string // the operator's key fingerprints; may be empty
	Named   bool     // Signers is the key --key names, not a recorded one
	Root    string   // a pinned root sha; may be empty
}

// PinNotServedError is a gather from a pinned root none of whose history the
// sites serve: they hold another incarnation of the deployment, or a site
// serves one it should not. It names no root the sites served, since a hash
// from them is not one to pin.
type PinNotServedError struct{ Root string }

func (e *PinNotServedError) Error() string {
	return fmt.Sprintf("the sites serve no version of the pinned root %s's history", e.Root)
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
	return fmt.Sprintf("versions %s and %s are part of a loop of removals that bar one another, none of them in your anchor's history; choose one by publishing on it", e.A, e.B)
}

// UnreadableOperatorsError is a version in the anchor's history whose
// paisans-stack.yaml cannot be read. Its removals still stand, but which keys it
// removed is unknown, so Judge refuses rather than trust what they would bar.
type UnreadableOperatorsError struct {
	SHA string
	Err error
}

func (e *UnreadableOperatorsError) Error() string {
	return fmt.Sprintf("the operators of version %s, in your anchor's history, cannot be read: %v", e.SHA, e.Err)
}

func (e *UnreadableOperatorsError) Unwrap() error { return e.Err }

// Judge decides which versions to trust, starting from the operator's anchor,
// by the spec's "Which versions the workstation trusts":
//
//   - The anchor is the operator's newest version that passes shape, signed
//     by any of the anchor's keys, or else the pinned root. With both, the
//     signed anchor must descend from the pin. A pin the sites serve none
//     of the history of, when they serve anything, is a *PinNotServedError.
//     The anchor and every ancestor of it are trusted whatever their shape,
//     because the operator signed after them.
//   - A named key anchors only with no root pinned, and Judge refuses the
//     two together. The key must have
//     signed versions under exactly one good root, which is judged by these
//     rules as if pinned. The versions the key signed that this trusts are
//     the candidates, and the newest of those not in another's history is
//     the anchor. Each such version is judged as the anchor in turn; when
//     their heads differ, the verdict is a fork of every head any of them
//     trusts. Every head must be signed by the named key and list it under an
//     operator; otherwise the operator pins --root.
//   - Removals in that history always stand, and bar the versions signed by
//     each removed key that are not ancestors of the removal.
//   - Every other version is a candidate when it passes shape, is not a root,
//     has only trusted or candidate parents, is signed by a key of an operator in
//     every parent, and is not barred by the history.
//   - A candidate's removals bar other candidates, and with them every
//     candidate built on a barred one. A candidate nobody undecided bars is
//     trusted and its removals stand; one barred by a trusted candidate is
//     not trusted and removes nothing; repeat until nothing changes. What is
//     left bars itself in a loop, and is a *ConflictError.
//
// Judge returns ErrNoAnchor (or an error that is it to errors.Is) when trust
// has nowhere to start or the pin is not a good root, *UnreadableOperatorsError
// when a version in the anchor's history has operators that cannot be read, and
// *ConflictError for a loop of removals. The result does not depend on map
// order, and Judge terminates on any graph, including one with a missing
// parent or a cycle.
func Judge(g Graph, a Anchor) (Verdict, error) {
	if g.Operators == nil {
		return Verdict{}, errors.New("judging versions needs a way to read their operators")
	}
	j := &judgement{g: g, operators: map[string]operatorsResult{}, anc: map[string]map[string]bool{}}
	order, cyclic := j.topo()

	if a.Named && a.Root != "" {
		return Verdict{}, errors.New("a key named with --key anchors only a workstation with no root pinned; with a root, the root anchors alone")
	}
	// A root that is not held is answered by the pin's own checks, which
	// name it, rather than by what the sites serve.
	if _, held := j.g.Versions[a.Root]; a.Root != "" && held {
		if err := j.pinServed(a, order); err != nil {
			return Verdict{}, err
		}
	}
	if a.Named {
		return j.named(a, order, cyclic)
	}
	anchor, err := j.anchor(a, order)
	if err != nil {
		return Verdict{}, err
	}
	return j.from(anchor, order, cyclic)
}

// from judges every version with trust starting at anchor.
func (j *judgement) from(anchor string, order, cyclic []string) (Verdict, error) {
	g := j.g
	history := map[string]bool{anchor: true}
	for sha := range j.ancestors(anchor) {
		history[sha] = true
	}
	for _, sha := range order {
		if history[sha] {
			if err := j.operatorsOf(sha).err; err != nil {
				return Verdict{}, &UnreadableOperatorsError{SHA: sha, Err: err}
			}
		}
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

type operatorsResult struct {
	m   map[string]string
	err error
}

type judgement struct {
	g         Graph
	operators map[string]operatorsResult
	anc       map[string]map[string]bool
}

func (j *judgement) operatorsOf(sha string) operatorsResult {
	if r, ok := j.operators[sha]; ok {
		return r
	}
	m, err := j.g.Operators(sha)
	j.operators[sha] = operatorsResult{m, err}
	return j.operators[sha]
}

// anchor picks where trust starts from a recorded key: the newest version
// it signed that passes shape, or the pinned root.
func (j *judgement) anchor(a Anchor, order []string) (string, error) {
	signed := ""
	for _, sha := range order {
		v := j.g.Versions[sha]
		if !slices.Contains(a.Signers, v.SignerFP) || j.shape(sha) != "" {
			continue
		}
		if signed == "" || j.newer(sha, signed) {
			signed = sha
		}
	}
	if a.Root != "" {
		if reason := j.goodRoot(a.Root); reason != "" {
			return "", &anchorError{fmt.Sprintf("the pinned root %s %s", a.Root, reason)}
		}
		if signed == "" {
			return a.Root, nil
		}
		if a.Root != signed && !j.ancestors(signed)[a.Root] {
			return "", &anchorError{fmt.Sprintf("your newest version %s does not descend from the pinned root %s", signed, a.Root)}
		}
	}
	if signed != "" {
		return signed, nil
	}
	return "", ErrNoAnchor
}

// pinServed refuses a pinned root none of whose history the sites serve in
// this gather, when they serve anything, rather than judge from what this
// workstation's cache holds.
func (j *judgement) pinServed(a Anchor, order []string) error {
	if len(j.g.Served) == 0 {
		return nil
	}
	for _, sha := range order {
		if j.g.Served[sha] && (sha == a.Root || j.ancestors(sha)[a.Root]) {
			return nil
		}
	}
	return &PinNotServedError{Root: a.Root}
}

// named judges from the key --key names, with no root pinned, by the rule in
// Judge's comment: the key anchors only on versions it signed itself, under
// one root, that the trust rules trust already.
func (j *judgement) named(a Anchor, order, cyclic []string) (Verdict, error) {
	key := strings.Join(a.Signers, ", ")
	signed := func(sha string) bool {
		return slices.Contains(a.Signers, j.g.Versions[sha].SignerFP) && j.shape(sha) == ""
	}
	var roots []string
	for _, root := range order {
		if len(j.g.Versions[root].Parents) > 0 || j.goodRoot(root) != "" {
			continue
		}
		for _, sha := range order {
			if signed(sha) && (sha == root || j.ancestors(sha)[root]) {
				roots = append(roots, root)
				break
			}
		}
	}
	switch len(roots) {
	case 0:
		return Verdict{}, &anchorError{fmt.Sprintf("no version the trust rules trust is signed by the key --key names, %s, and no root is pinned", key)}
	case 1:
	default:
		return Verdict{}, &anchorError{fmt.Sprintf("the key --key names, %s, signed versions under more than one root, and which is this deployment's is not for a key to say: get the current root from another operator, the full hash in their .paisans/config-root or on a root's line of their paisans deployment log, and recover with --root <sha>. A root may be held only in this workstation's own cache, .paisans/store.git, from an earlier run; on a workstation recovering from nothing, removing .paisans/ clears it", key)}
	}
	v, err := j.from(roots[0], order, cyclic)
	if err != nil {
		return Verdict{}, err
	}
	var cands []string
	for _, sha := range order {
		if v.Trusted[sha] && signed(sha) {
			cands = append(cands, sha)
		}
	}
	tips := j.newest(cands)
	if len(tips) == 0 {
		return Verdict{}, &anchorError{fmt.Sprintf("no version the trust rules trust is signed by the key --key names, %s, and no root is pinned", key)}
	}
	verdicts := make([]Verdict, 0, len(tips))
	agree := true
	for _, tip := range tips {
		v, err := j.from(tip, order, cyclic)
		if err != nil {
			return Verdict{}, err
		}
		verdicts = append(verdicts, v)
		agree = agree && slices.Equal(v.Heads, verdicts[0].Heads)
	}
	out := verdicts[0]
	if !agree {
		out = j.union(verdicts)
	}
	for _, h := range out.Heads {
		ops := j.operatorsOf(h)
		listed := ops.err == nil
		for _, fp := range a.Signers {
			if _, ok := ops.m[fp]; !ok {
				listed = false
			}
		}
		if !listed {
			return Verdict{}, &anchorError{fmt.Sprintf("the key --key names, %s, is not a key of an operator the newest version %s lists", key, h)}
		}
		if !slices.Contains(a.Signers, j.g.Versions[h].SignerFP) {
			return Verdict{}, &anchorError{fmt.Sprintf("the newest version %s is signed by %s, not by the key --key names, %s; a key alone cannot tell this deployment's history from one it signed before a redeploy, so it trusts only versions it signed: get the current root from another operator, the full hash in their .paisans/config-root or on a root's line of their paisans deployment log, and pin it with --root <sha>", h, j.g.Versions[h].SignerFP, key)}
		}
	}
	return out, nil
}

// newest is the versions among shas that are in no other's history, newest
// first.
func (j *judgement) newest(shas []string) []string {
	var out []string
	for _, sha := range shas {
		below := false
		for _, other := range shas {
			if other != sha && j.ancestors(other)[sha] {
				below = true
				break
			}
		}
		if !below {
			out = append(out, sha)
		}
	}
	sort.Slice(out, func(x, y int) bool { return j.newer(out[x], out[y]) })
	return out
}

// newer orders versions by revision, highest first, then by lowest hash.
func (j *judgement) newer(x, y string) bool {
	vx, vy := j.g.Versions[x], j.g.Versions[y]
	if vx.Revision != vy.Revision {
		return vx.Revision > vy.Revision
	}
	return x < y
}

// union is a fork of verdicts that disagree: every version any of them
// trusts, and as heads those of their heads that are in no other's history.
// Which line goes on is the operator's to say, by merging them.
func (j *judgement) union(verdicts []Verdict) Verdict {
	out := Verdict{Trusted: map[string]bool{}, Reasons: map[string]string{}, Fork: true}
	var heads []string
	for _, v := range verdicts {
		for sha, ok := range v.Trusted {
			out.Trusted[sha] = out.Trusted[sha] || ok
		}
		for _, h := range v.Heads {
			if !slices.Contains(heads, h) {
				heads = append(heads, h)
			}
		}
	}
	for sha := range j.g.Versions {
		if out.Trusted[sha] {
			continue
		}
		out.Trusted[sha] = false
		for _, v := range verdicts {
			if r := v.Reasons[sha]; r != "" {
				out.Reasons[sha] = r
				break
			}
		}
	}
	out.Heads = j.newest(heads)
	out.Fork = len(out.Heads) > 1
	return out
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
		return "fails shape: " + j.shape(sha)
	}
	own := j.operatorsOf(sha)
	if own.err != nil {
		return fmt.Sprintf("has unreadable operators: %v", own.err)
	}
	if _, ok := own.m[v.SignerFP]; !ok {
		return "is not signed by an operator it lists"
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
	switch {
	case v.BuildNewer:
		return fmt.Sprintf("it was published by paisans %q, a version this paisans cannot order, so a newer paisans may have published it; upgrade paisans (shape)", v.Build.Version)
	case !v.BuildOK && len(v.Parents) == 0:
		return "it is a root that does not say which build of paisans published it, so no version can be published on it and the sites keep it: redeploy, by each host's owner running `paisans site evict` and admitting the deployment again, then `paisans deployment apply --execute` publishes a new first version (shape)"
	case !v.BuildOK:
		return "it does not say which build of paisans published it; publish the configuration again, with `paisans deployment publish --execute` (shape)"
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
// candidate, or "" when it can. A version whose own paisans-stack.yaml cannot be
// read is not trusted: its operators decide its children and its removals.
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
	if own := j.operatorsOf(sha); own.err != nil {
		return fmt.Sprintf("its operators cannot be read: %v (signer)", own.err)
	}
	for _, p := range v.Parents {
		pa := j.operatorsOf(p)
		if pa.err != nil {
			return fmt.Sprintf("the operators of parent %s cannot be read: %v (signer)", p, pa.err)
		}
		if _, ok := pa.m[v.SignerFP]; !ok {
			return fmt.Sprintf("signed by %s, which is not a key of an operator in parent %s (signer)", v.SignerFP, p)
		}
	}
	return ""
}

// bars says why removals in r bar v, or "" when they do not: v is signed by a
// key one of r's parents listed and r does not, and v is neither r nor an
// ancestor of it. Judge refuses an anchor's history with unreadable operators and
// never makes a candidate of a version with them, so own.err is not expected
// here; it is treated as removing nothing.
func (j *judgement) bars(r, v string) string {
	if r == v || j.ancestors(r)[v] {
		return ""
	}
	fp := j.g.Versions[v].SignerFP
	own := j.operatorsOf(r)
	if own.err != nil {
		return ""
	}
	if _, kept := own.m[fp]; kept {
		return ""
	}
	for _, p := range j.g.Versions[r].Parents {
		if id, listed := j.operatorsOf(p).m[fp]; listed {
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

package configstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fake struct {
	vs     map[string]Version
	admins map[string]map[string]string
	badDig map[string]bool
}

func newFake() *fake {
	return &fake{vs: map[string]Version{}, admins: map[string]map[string]string{}, badDig: map[string]bool{}}
}

func (f *fake) add(sha string, rev int, signer string, parents ...string) {
	f.vs[sha] = Version{SHA: sha, Parents: parents, Revision: rev, Signed: true, SignerFP: signer, TreeOK: true}
}

// lists sets the admins a version's paisans.yaml names, each by its key
// fingerprint "fp-<name>".
func (f *fake) lists(sha string, names ...string) {
	m := map[string]string{}
	for _, n := range names {
		m["fp-"+n] = n
	}
	f.admins[sha] = m
}

func (f *fake) graph() Graph {
	return Graph{
		Versions: f.vs,
		Admins:   func(sha string) (map[string]string, error) { return f.admins[sha], nil },
		Digest: func(sha string) error {
			if f.badDig[sha] {
				return ErrDigestMismatch
			}
			return nil
		},
	}
}

func judge(t *testing.T, f *fake, pinned string) Verdict {
	t.Helper()
	v, err := Judge(f.graph(), pinned)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	return v
}

func wantHeads(t *testing.T, v Verdict, heads ...string) {
	t.Helper()
	if !reflect.DeepEqual(v.Heads, heads) {
		t.Fatalf("Heads = %v, want %v (reasons %v)", v.Heads, heads, v.Reasons)
	}
	if v.Fork != (len(heads) > 1) {
		t.Fatalf("Fork = %v with heads %v", v.Fork, heads)
	}
}

func wantInvalid(t *testing.T, v Verdict, sha, reason string) {
	t.Helper()
	if v.Valid[sha] {
		t.Fatalf("%s is valid, want invalid", sha)
	}
	if !strings.Contains(v.Reasons[sha], reason) {
		t.Fatalf("reason for %s = %q, want it to contain %q", sha, v.Reasons[sha], reason)
	}
}

func wantValid(t *testing.T, v Verdict, shas ...string) {
	t.Helper()
	for _, s := range shas {
		if !v.Valid[s] {
			t.Fatalf("%s is invalid: %s", s, v.Reasons[s])
		}
	}
}

// chain is v1 <- v2 <- v3, all signed by alice and listing alice.
func chain() *fake {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.add("v2", 2, "fp-alice", "v1")
	f.add("v3", 3, "fp-alice", "v2")
	for _, s := range []string{"v1", "v2", "v3"} {
		f.lists(s, "alice")
	}
	return f
}

func TestJudgeLinearChain(t *testing.T) {
	v := judge(t, chain(), "")
	wantValid(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
	if !reflect.DeepEqual(v.Roots, []string{"v1"}) {
		t.Fatalf("Roots = %v", v.Roots)
	}
}

func TestJudgeEmptyGraph(t *testing.T) {
	v := judge(t, newFake(), "")
	if len(v.Heads) != 0 || len(v.Roots) != 0 || v.Fork {
		t.Fatalf("empty graph gave %+v", v)
	}
}

func TestJudgeForgedRoot(t *testing.T) {
	f := chain()
	f.add("m1", 1, "fp-mallory")
	f.add("m2", 2, "fp-mallory", "m1")
	f.lists("m1", "mallory")
	f.lists("m2", "mallory")
	f.vs["v1"] = withDate(f.vs["v1"], time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))

	_, err := Judge(f.graph(), "")
	if err == nil {
		t.Fatal("two valid roots were not refused")
	}
	for _, s := range []string{"v1", "m1", "fp-alice", "fp-mallory", "alice", "mallory", "2026-10-01T12:00:00Z"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q does not name %q", err, s)
		}
	}

	v := judge(t, f, "v1")
	wantValid(t, v, "v1", "v2", "v3")
	wantInvalid(t, v, "m1", "pinned root")
	wantInvalid(t, v, "m2", "not valid")
	wantHeads(t, v, "v3")
	if !reflect.DeepEqual(v.Roots, []string{"v1"}) {
		t.Fatalf("Roots = %v", v.Roots)
	}
}

// A forged root that lists the real admin and then removes them must not be
// able to invalidate the real root by rule 3 and so leave itself the only
// root: removals are judged only after the root is settled.
func TestJudgeForgedRootCannotRemoveRealAdmin(t *testing.T) {
	f := chain()
	f.add("m1", 1, "fp-mallory")
	f.add("m2", 2, "fp-mallory", "m1")
	f.lists("m1", "mallory", "alice")
	f.lists("m2", "mallory")

	if _, err := Judge(f.graph(), ""); err == nil {
		t.Fatal("two valid roots were not refused")
	}
	v := judge(t, f, "v1")
	wantValid(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
}

func TestJudgeSignerNotInParentAdmins(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-eve", "v3")
	f.lists("v4", "alice", "eve")
	v := judge(t, f, "")
	wantInvalid(t, v, "v4", "fp-eve")
	wantInvalid(t, v, "v4", "rule 2")
	wantHeads(t, v, "v3")
}

func TestJudgeRootSignerMustBeListedByIt(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-eve")
	f.lists("v1", "alice")
	v := judge(t, f, "")
	wantInvalid(t, v, "v1", "rule 2")
	wantHeads(t, v)
}

func TestJudgeRemovalIsFinal(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.lists("v1", "alice", "bob")
	f.add("v2", 2, "fp-alice", "v1")
	f.lists("v2", "alice")
	f.add("v3", 2, "fp-bob", "v1")
	f.lists("v3", "alice", "bob")
	f.add("v4", 3, "fp-alice", "v3")
	f.lists("v4", "alice", "bob")

	v := judge(t, f, "")
	wantInvalid(t, v, "v3", "signed by bob after a version removed them")
	wantInvalid(t, v, "v3", "rule 3")
	wantInvalid(t, v, "v4", "not valid")
	wantValid(t, v, "v1", "v2")
	wantHeads(t, v, "v2")
}

// bob's versions from before the removal are its ancestors and stay valid.
func TestJudgeRemovalKeepsAncestors(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.lists("v1", "alice", "bob")
	f.add("v2", 2, "fp-bob", "v1")
	f.lists("v2", "alice", "bob")
	f.add("v3", 3, "fp-alice", "v2")
	f.lists("v3", "alice")
	v := judge(t, f, "")
	wantValid(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
}

// An admin who signs their own removal publishes a valid version.
func TestJudgeSelfRemoval(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.lists("v1", "alice", "bob")
	f.add("v2", 2, "fp-bob", "v1")
	f.lists("v2", "alice")
	v := judge(t, f, "")
	wantValid(t, v, "v1", "v2")
	wantHeads(t, v, "v2")
}

// Every key the removed admin held in the parent is barred, not only one.
func TestJudgeRemovalCoversEveryKey(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.admins["v1"] = map[string]string{"fp-alice": "alice", "fp-bob": "bob", "fp-bob2": "bob"}
	f.add("v2", 2, "fp-alice", "v1")
	f.lists("v2", "alice")
	f.add("v3", 2, "fp-bob2", "v1")
	f.lists("v3", "alice", "bob")
	v := judge(t, f, "")
	wantInvalid(t, v, "v3", "signed by bob after a version removed them")
	wantHeads(t, v, "v2")
}

// Two admins who remove each other on separate branches: each removal stands,
// so each removes the other's later versions, and the verdict is stable.
func TestJudgeMutualRemoval(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.lists("v1", "alice", "bob")
	f.add("a2", 2, "fp-alice", "v1")
	f.lists("a2", "alice")
	f.add("b2", 2, "fp-bob", "v1")
	f.lists("b2", "bob")
	f.add("a3", 3, "fp-alice", "a2")
	f.lists("a3", "alice")
	for i := 0; i < 5; i++ {
		v := judge(t, f, "")
		wantInvalid(t, v, "a2", "signed by alice after a version removed them")
		wantInvalid(t, v, "a3", "signed by alice after a version removed them")
		wantInvalid(t, v, "b2", "signed by bob after a version removed them")
		wantValid(t, v, "v1")
		wantHeads(t, v, "v1")
	}
}

func TestJudgeRealFork(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.add("v2", 2, "fp-alice", "v1")
	f.add("va", 3, "fp-alice", "v2")
	f.add("vc", 3, "fp-carol", "v2")
	for _, s := range []string{"v1", "v2", "va", "vc"} {
		f.lists(s, "alice", "carol")
	}
	v := judge(t, f, "")
	wantValid(t, v, "va", "vc")
	wantHeads(t, v, "va", "vc")
}

func TestJudgeMergeResolvesFork(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.add("v2", 2, "fp-alice", "v1")
	f.add("va", 3, "fp-alice", "v2")
	f.add("vc", 3, "fp-carol", "v2")
	f.add("vm", 4, "fp-alice", "va", "vc")
	for _, s := range []string{"v1", "v2", "va", "vc", "vm"} {
		f.lists(s, "alice", "carol")
	}
	v := judge(t, f, "")
	wantValid(t, v, "vm")
	wantHeads(t, v, "vm")
}

// A merge's signer must be an admin in every parent, not only in one.
func TestJudgeMergeSignerInEveryParent(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.lists("v1", "alice", "carol")
	f.add("va", 2, "fp-alice", "v1")
	f.lists("va", "alice", "carol")
	f.add("vc", 2, "fp-carol", "v1")
	f.lists("vc", "alice", "carol", "dave")
	f.add("vm", 3, "fp-dave", "va", "vc")
	f.lists("vm", "alice", "carol", "dave")
	v := judge(t, f, "")
	wantInvalid(t, v, "vm", "rule 2")
	wantHeads(t, v, "va", "vc")
}

func TestJudgeHeadDigestFails(t *testing.T) {
	f := chain()
	f.badDig["v3"] = true
	v := judge(t, f, "")
	wantInvalid(t, v, "v3", "digest")
	wantHeads(t, v, "v2")

	// An ancestor whose secrets the operator cannot decrypt stays valid when a
	// descendant's digest verifies (Review Focus 1).
	f = chain()
	f.badDig["v1"] = true
	f.badDig["v2"] = true
	v = judge(t, f, "")
	wantValid(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")

	// Every head failing walks back until one verifies.
	f = chain()
	f.badDig["v3"] = true
	f.badDig["v2"] = true
	v = judge(t, f, "")
	wantInvalid(t, v, "v2", "digest")
	wantHeads(t, v, "v1")
}

func TestJudgeRevisionMustFollowParent(t *testing.T) {
	f := chain()
	f.add("v4", 5, "fp-alice", "v3")
	f.lists("v4", "alice")
	v := judge(t, f, "")
	wantInvalid(t, v, "v4", "rule 1")
	wantHeads(t, v, "v3")

	f = newFake()
	f.add("v1", 2, "fp-alice")
	f.lists("v1", "alice")
	v = judge(t, f, "")
	wantInvalid(t, v, "v1", "rule 1")
}

func TestJudgeMergeRevisionFollowsHighestParent(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice")
	f.add("v2", 2, "fp-alice", "v1")
	f.add("v3", 3, "fp-alice", "v2")
	f.add("vb", 2, "fp-alice", "v1")
	f.add("vm", 4, "fp-alice", "v3", "vb")
	for _, s := range []string{"v1", "v2", "v3", "vb", "vm"} {
		f.lists(s, "alice")
	}
	v := judge(t, f, "")
	wantHeads(t, v, "vm")
}

func TestJudgeUnsignedOrBadTree(t *testing.T) {
	f := chain()
	x := f.vs["v3"]
	x.Signed = false
	f.vs["v3"] = x
	v := judge(t, f, "")
	wantInvalid(t, v, "v3", "rule 1")
	wantHeads(t, v, "v2")

	f = chain()
	x = f.vs["v3"]
	x.TreeOK = false
	f.vs["v3"] = x
	v = judge(t, f, "")
	wantInvalid(t, v, "v3", "rule 1")
	wantHeads(t, v, "v2")
}

func withDate(v Version, t time.Time) Version {
	v.Committed = t
	return v
}

func TestJudgeOrdersByRevisionNotDate(t *testing.T) {
	f := chain()
	f.vs["v2"] = withDate(f.vs["v2"], time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	f.vs["v3"] = withDate(f.vs["v3"], time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	v := judge(t, f, "")
	wantHeads(t, v, "v3")

	// Heads of a fork order by revision, highest first, whatever their dates.
	f = chain()
	f.add("vx", 3, "fp-alice", "v2")
	f.lists("vx", "alice")
	f.add("vy", 4, "fp-alice", "vx")
	f.lists("vy", "alice")
	f.vs["vy"] = withDate(f.vs["vy"], time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	f.vs["v3"] = withDate(f.vs["v3"], time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	v = judge(t, f, "")
	wantHeads(t, v, "vy", "v3")
}

// A re-publish of identical content on different parents is two versions,
// each judged on its own (Review Focus 2).
func TestJudgeDuplicateTrees(t *testing.T) {
	f := chain()
	f.add("d1", 3, "fp-alice", "v2")
	f.lists("d1", "alice")
	f.add("d2", 4, "fp-alice", "v3")
	f.lists("d2", "alice")
	f.badDig["d1"] = true
	v := judge(t, f, "")
	wantInvalid(t, v, "d1", "digest")
	wantValid(t, v, "d2")
	wantHeads(t, v, "d2")
}

func TestJudgeMissingParent(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-alice", "v3", "gone")
	f.lists("v4", "alice")
	v := judge(t, f, "")
	wantInvalid(t, v, "v4", "parent gone is not held")
	wantHeads(t, v, "v3")
}

// Commit ids make a cycle impossible in git, but Judge must still finish on
// one and judge it invalid.
func TestJudgeCycleTerminates(t *testing.T) {
	f := chain()
	f.add("c1", 4, "fp-alice", "c2")
	f.add("c2", 5, "fp-alice", "c1")
	f.lists("c1", "alice")
	f.lists("c2", "alice")
	v := judge(t, f, "")
	wantInvalid(t, v, "c1", "cycle")
	wantInvalid(t, v, "c2", "cycle")
	wantHeads(t, v, "v3")
}

func TestJudgeUnreadableAdmins(t *testing.T) {
	f := chain()
	g := f.graph()
	g.Admins = func(sha string) (map[string]string, error) {
		if sha == "v3" {
			return nil, errors.New("bad yaml")
		}
		return f.admins[sha], nil
	}
	v, err := Judge(g, "")
	if err != nil {
		t.Fatal(err)
	}
	wantInvalid(t, v, "v3", "bad yaml")
	wantHeads(t, v, "v2")
}

func TestJudgePinnedRootNotHeld(t *testing.T) {
	v := judge(t, chain(), "elsewhere")
	wantInvalid(t, v, "v1", "pinned root")
	wantHeads(t, v)
}

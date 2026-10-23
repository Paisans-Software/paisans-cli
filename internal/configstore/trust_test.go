package configstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/version"
)

type fake struct {
	vs        map[string]Version
	operators map[string]map[string]string
}

func newFake() *fake {
	return &fake{vs: map[string]Version{}, operators: map[string]map[string]string{}}
}

// add records a version listing the given operators, each by the key "fp-<name>".
func (f *fake) add(sha string, rev int, signer string, parents []string, operators ...string) {
	f.vs[sha] = Version{SHA: sha, Parents: parents, Revision: rev, Signed: true, SignerFP: signer, TreeOK: true, Build: version.Current(), BuildOK: true}
	m := map[string]string{}
	for _, n := range operators {
		m["fp-"+n] = n
	}
	f.operators[sha] = m
}

func (f *fake) graph() Graph {
	return Graph{
		Versions:  f.vs,
		Operators: func(sha string) (map[string]string, error) { return f.operators[sha], nil },
	}
}

func on(parents ...string) []string { return parents }

func judgeOK(t *testing.T, f *fake, a Anchor) Verdict {
	t.Helper()
	v, err := Judge(f.graph(), a)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	return v
}

func alice() Anchor { return Anchor{Signers: []string{"fp-alice"}} }

func wantHeads(t *testing.T, v Verdict, heads ...string) {
	t.Helper()
	if !reflect.DeepEqual(v.Heads, heads) {
		t.Fatalf("Heads = %v, want %v (reasons %v)", v.Heads, heads, v.Reasons)
	}
	if v.Fork != (len(heads) > 1) {
		t.Fatalf("Fork = %v with heads %v", v.Fork, heads)
	}
}

func wantUntrusted(t *testing.T, v Verdict, sha, reason string) {
	t.Helper()
	if v.Trusted[sha] {
		t.Fatalf("%s is trusted, want untrusted", sha)
	}
	if !strings.Contains(v.Reasons[sha], reason) {
		t.Fatalf("reason for %s = %q, want it to contain %q", sha, v.Reasons[sha], reason)
	}
}

func wantTrusted(t *testing.T, v Verdict, shas ...string) {
	t.Helper()
	for _, s := range shas {
		if !v.Trusted[s] {
			t.Fatalf("%s is untrusted: %s", s, v.Reasons[s])
		}
	}
}

// chain is v1 <- v2 <- v3, all signed by alice and listing alice.
func chain() *fake {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice")
	f.add("v2", 2, "fp-alice", on("v1"), "alice")
	f.add("v3", 3, "fp-alice", on("v2"), "alice")
	return f
}

func TestJudgeLinearChain(t *testing.T) {
	v := judgeOK(t, chain(), alice())
	wantTrusted(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
	if v.AnchorAt != "v3" {
		t.Fatalf("AnchorAt = %s", v.AnchorAt)
	}
}

// The signer anchor is the operator's newest version, ties to the lowest hash.
func TestJudgeSignerAnchorIsNewest(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-bob", on("v3"), "alice", "bob")
	f.operators["v3"] = map[string]string{"fp-alice": "alice", "fp-bob": "bob"}
	f.add("x3", 3, "fp-alice", on("v2"), "alice")
	v := judgeOK(t, f, alice())
	if v.AnchorAt != "v3" {
		t.Fatalf("AnchorAt = %s, want v3", v.AnchorAt)
	}
	wantHeads(t, v, "v4", "x3")
}

// An attacker with root on every site writes a root signed by their own key,
// listing themselves and the real operator's key. The operator's own signature on
// the real chain is the anchor, so the forged chain is never trusted.
func TestJudgeForgedRoot(t *testing.T) {
	f := chain()
	f.add("m1", 1, "fp-mallory", nil, "mallory", "alice")
	f.add("m2", 2, "fp-mallory", on("m1"), "mallory")
	v := judgeOK(t, f, alice())
	wantUntrusted(t, v, "m1", "root")
	wantUntrusted(t, v, "m2", "not trusted")
	wantTrusted(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
}

func TestJudgeNoAnchor(t *testing.T) {
	_, err := Judge(chain().graph(), Anchor{Signers: []string{"fp-zed"}})
	if !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("err = %v, want ErrNoAnchor", err)
	}
	_, err = Judge(chain().graph(), Anchor{})
	if !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("err = %v, want ErrNoAnchor", err)
	}
}

func TestJudgePinAloneAnchors(t *testing.T) {
	v := judgeOK(t, chain(), Anchor{Root: "v1"})
	if v.AnchorAt != "v1" {
		t.Fatalf("AnchorAt = %s", v.AnchorAt)
	}
	wantTrusted(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
}

// A pin is only an anchor when it is a root that passes shape and is signed
// by an operator it lists itself.
func TestJudgePinMustBeAGoodRoot(t *testing.T) {
	f := chain()
	f.add("e1", 1, "fp-eve", nil, "alice")
	for _, pin := range []string{"e1", "v2", "elsewhere"} {
		if _, err := Judge(f.graph(), Anchor{Root: pin}); !errors.Is(err, ErrNoAnchor) {
			t.Fatalf("pin %s: err = %v, want ErrNoAnchor", pin, err)
		}
	}
}

func TestJudgeSignerAnchorMustDescendFromPin(t *testing.T) {
	f := chain()
	f.add("m1", 1, "fp-mallory", nil, "mallory")
	_, err := Judge(f.graph(), Anchor{Signers: []string{"fp-alice"}, Root: "m1"})
	var re *RootError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a *RootError", err)
	}
	for _, s := range []string{"v3", "m1"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q does not name %s", err, s)
		}
	}

	v := judgeOK(t, f, Anchor{Signers: []string{"fp-alice"}, Root: "v1"})
	if v.AnchorAt != "v3" {
		t.Fatalf("AnchorAt = %s, want v3", v.AnchorAt)
	}
	wantUntrusted(t, v, "m1", "root")
}

func TestJudgeEmptyGraph(t *testing.T) {
	for _, a := range []Anchor{{Root: "v1"}, {Signers: []string{"fp-alice"}}, {}} {
		if _, err := Judge(newFake().graph(), a); !errors.Is(err, ErrNoAnchor) {
			t.Fatalf("anchor %+v: err = %v, want ErrNoAnchor", a, err)
		}
	}
}

// The review's attack: alice's a2 removes bob; bob, already removed, signs b2
// on v1 removing alice.
func reviewAttack(v1Signer string) *fake {
	f := newFake()
	f.add("v1", 1, v1Signer, nil, "op", "alice", "bob")
	f.add("a2", 2, "fp-alice", on("v1"), "op", "alice")
	f.add("a3", 3, "fp-alice", on("a2"), "op", "alice")
	f.add("b2", 2, "fp-bob", on("v1"), "op", "bob")
	return f
}

func TestJudgeRemovalInHistoryStands(t *testing.T) {
	v := judgeOK(t, reviewAttack("fp-alice"), alice())
	wantUntrusted(t, v, "b2", "removal")
	wantTrusted(t, v, "v1", "a2", "a3")
	wantHeads(t, v, "a3")
}

func TestJudgeMutualRemovalOutsideHistoryIsAConflict(t *testing.T) {
	f := reviewAttack("fp-op")
	for i := 0; i < 5; i++ {
		_, err := Judge(f.graph(), Anchor{Signers: []string{"fp-op"}})
		var c *ConflictError
		if !errors.As(err, &c) {
			t.Fatalf("err = %v, want *ConflictError", err)
		}
		if c.A != "a2" || c.B != "b2" {
			t.Fatalf("ConflictError = %+v, want a2 and b2", c)
		}
		for _, s := range []string{"a2", "b2"} {
			if !strings.Contains(err.Error(), s) {
				t.Fatalf("error %q does not name %s", err, s)
			}
		}
	}
}

// A removal outside the anchor's history that nobody contests stands.
func TestJudgeUncontestedRemovalOutsideHistory(t *testing.T) {
	f := reviewAttack("fp-op")
	f.operators["b2"] = map[string]string{"fp-op": "op", "fp-alice": "alice", "fp-bob": "bob"}
	f.add("b3", 3, "fp-alice", on("b2"), "op", "alice", "bob")
	v := judgeOK(t, f, Anchor{Signers: []string{"fp-op"}})
	wantUntrusted(t, v, "b2", "removal")
	wantUntrusted(t, v, "b3", "not trusted")
	wantTrusted(t, v, "a2", "a3")
	wantHeads(t, v, "a3")
}

// Removal is per key: a key rotated out of an operator who stays can no longer
// sign on an older parent.
func TestJudgeKeyRotation(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-old", nil)
	f.operators["v1"] = map[string]string{"fp-old": "alice"}
	f.add("v2", 2, "fp-old", on("v1"))
	f.operators["v2"] = map[string]string{"fp-new": "alice"}
	f.add("v3", 3, "fp-new", on("v2"))
	f.operators["v3"] = map[string]string{"fp-new": "alice"}
	f.add("x2", 2, "fp-old", on("v1"))
	f.operators["x2"] = map[string]string{"fp-old": "alice"}
	v := judgeOK(t, f, Anchor{Signers: []string{"fp-new"}})
	wantUntrusted(t, v, "x2", "removal")
	wantHeads(t, v, "v3")
}

func TestJudgeSelfRemoval(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice", "bob")
	f.add("v2", 2, "fp-bob", on("v1"), "alice")
	v := judgeOK(t, f, alice())
	wantTrusted(t, v, "v1", "v2")
	wantHeads(t, v, "v2")
}

func TestJudgeFork(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice", "carol")
	f.add("v2", 2, "fp-alice", on("v1"), "alice", "carol")
	f.add("a3", 3, "fp-alice", on("v2"), "alice", "carol")
	f.add("c3", 3, "fp-carol", on("v2"), "alice", "carol")
	v := judgeOK(t, f, alice())
	if v.AnchorAt != "a3" {
		t.Fatalf("AnchorAt = %s", v.AnchorAt)
	}
	wantHeads(t, v, "a3", "c3")

	f.add("m4", 4, "fp-carol", on("a3", "c3"), "alice", "carol")
	wantHeads(t, judgeOK(t, f, alice()), "m4")
}

func TestJudgeSignerNotInParentAdmins(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-eve", on("v3"), "alice", "eve")
	v := judgeOK(t, f, Anchor{Root: "v1"})
	wantUntrusted(t, v, "v4", "signer")
	wantHeads(t, v, "v3")
}

// A merge's signer must be an operator in every parent, not only in one.
func TestJudgeMergeSignerInEveryParent(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice", "carol")
	f.add("va", 2, "fp-alice", on("v1"), "alice", "carol")
	f.add("vc", 2, "fp-carol", on("v1"), "alice", "carol", "dave")
	f.add("vm", 3, "fp-dave", on("va", "vc"), "alice", "carol", "dave")
	v := judgeOK(t, f, Anchor{Root: "v1"})
	wantUntrusted(t, v, "vm", "signer")
	wantHeads(t, v, "va", "vc")
}

func TestJudgeShape(t *testing.T) {
	for name, mut := range map[string]func(*Version){
		"revision": func(v *Version) { v.Revision = 5 },
		"unsigned": func(v *Version) { v.Signed = false },
		"tree":     func(v *Version) { v.TreeOK = false },
		"no build": func(v *Version) { v.BuildOK = false },
	} {
		f := chain()
		f.add("v4", 4, "fp-alice", on("v3"), "alice")
		x := f.vs["v4"]
		mut(&x)
		f.vs["v4"] = x
		v := judgeOK(t, f, Anchor{Root: "v1"})
		if v.Trusted["v4"] || !strings.Contains(v.Reasons["v4"], "shape") {
			t.Fatalf("%s: v4 trusted=%v reason %q", name, v.Trusted["v4"], v.Reasons["v4"])
		}
		wantHeads(t, v, "v3")
	}
}

// The anchor's history is trusted whatever its own shape: the operator signed
// after it.
func TestJudgeHistoryTrustedWhateverShape(t *testing.T) {
	f := chain()
	x := f.vs["v2"]
	x.Signed = false
	f.vs["v2"] = x
	v := judgeOK(t, f, alice())
	wantTrusted(t, v, "v1", "v2", "v3")
	wantHeads(t, v, "v3")
}

func withDate(v Version, t time.Time) Version {
	v.Committed = t
	return v
}

func TestJudgeOrdersByRevisionNotDate(t *testing.T) {
	f := chain()
	f.vs["v2"] = withDate(f.vs["v2"], time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	f.vs["v3"] = withDate(f.vs["v3"], time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	wantHeads(t, judgeOK(t, f, Anchor{Root: "v1"}), "v3")

	f.add("vx", 3, "fp-alice", on("v2"), "alice")
	f.add("vy", 4, "fp-alice", on("vx"), "alice")
	f.vs["vy"] = withDate(f.vs["vy"], time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	f.vs["v3"] = withDate(f.vs["v3"], time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	wantHeads(t, judgeOK(t, f, Anchor{Root: "v1"}), "vy", "v3")
}

// A re-publish of identical content on different parents is two versions,
// each judged on its own.
func TestJudgeDuplicateTrees(t *testing.T) {
	f := chain()
	f.add("d1", 3, "fp-alice", on("v2"), "alice")
	f.add("d2", 4, "fp-alice", on("v3"), "alice")
	f.add("d3", 3, "fp-eve", on("v2"), "alice")
	v := judgeOK(t, f, Anchor{Root: "v1"})
	wantTrusted(t, v, "d1", "d2")
	wantUntrusted(t, v, "d3", "signer")
	wantHeads(t, v, "d2", "d1")
}

func TestJudgeMissingParent(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-alice", on("v3", "gone"), "alice")
	v := judgeOK(t, f, Anchor{Root: "v1"})
	wantUntrusted(t, v, "v4", "gone")
	wantHeads(t, v, "v3")
}

// Commit ids make a cycle impossible in git, but Judge must still finish on
// one and not trust it.
func TestJudgeCycleTerminates(t *testing.T) {
	f := chain()
	f.add("c1", 4, "fp-alice", on("c2"), "alice")
	f.add("c2", 5, "fp-alice", on("c1"), "alice")
	f.add("c3", 6, "fp-alice", on("c2"), "alice")
	v := judgeOK(t, f, alice())
	wantUntrusted(t, v, "c1", "cycle")
	wantUntrusted(t, v, "c2", "cycle")
	wantUntrusted(t, v, "c3", "cycle")
	wantHeads(t, v, "v3")
}

func TestJudgeUnreadableAdmins(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-alice", on("v3"), "alice")
	g := f.graph()
	g.Operators = func(sha string) (map[string]string, error) {
		if sha == "v4" {
			return nil, errors.New("bad yaml")
		}
		return f.operators[sha], nil
	}
	v, err := Judge(g, Anchor{Root: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	wantUntrusted(t, v, "v4", "bad yaml")
	wantHeads(t, v, "v3")
}

// Three versions on v1, each removing the key of the next: a chain of bars
// with no way in, so the operator decides.
func TestJudgeConflictChain(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-op", nil, "op", "a", "b", "c")
	f.add("ax", 2, "fp-a", on("v1"), "op", "a", "c")
	f.add("by", 2, "fp-b", on("v1"), "op", "a", "b")
	f.add("cz", 2, "fp-c", on("v1"), "op", "b", "c")
	_, err := Judge(f.graph(), Anchor{Signers: []string{"fp-op"}})
	var c *ConflictError
	if !errors.As(err, &c) {
		t.Fatalf("err = %v, want *ConflictError", err)
	}
}

// A removal never bars the versions it builds on.
func TestJudgeRemovalKeepsItsAncestors(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice", "bob")
	f.add("b2", 2, "fp-bob", on("v1"), "alice", "bob")
	f.add("a3", 3, "fp-alice", on("b2"), "alice")
	v := judgeOK(t, f, Anchor{Root: "v1"})
	wantTrusted(t, v, "b2", "a3")
	wantHeads(t, v, "a3")
}

// Among the operator's versions of equal revision the lowest hash anchors,
// whatever order they are reached in, and one failing shape never anchors.
func TestJudgeSignerAnchorTies(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice")
	f.add("a2", 2, "fp-alice", on("v1"), "alice")
	f.add("z2", 2, "fp-alice", on("v1"), "alice")
	f.add("k3", 3, "fp-alice", on("a2"), "alice")
	f.add("c3", 3, "fp-alice", on("z2"), "alice")
	f.add("x4", 7, "fp-alice", on("k3"), "alice")
	v := judgeOK(t, f, alice())
	if v.AnchorAt != "c3" {
		t.Fatalf("AnchorAt = %s, want c3", v.AnchorAt)
	}
	wantUntrusted(t, v, "x4", "shape")
	wantHeads(t, v, "c3", "k3")
}

// An invalid pin is refused even when the operator's own signed version
// descends from it.
func TestJudgeInvalidPinWithSignerAnchor(t *testing.T) {
	f := chain()
	f.add("e1", 1, "fp-eve", nil, "alice")
	f.add("e2", 2, "fp-alice", on("e1"), "alice")
	for _, pin := range []string{"elsewhere", "v2", "e1"} {
		_, err := Judge(f.graph(), Anchor{Signers: []string{"fp-alice"}, Root: pin})
		if !errors.Is(err, ErrNoAnchor) {
			t.Fatalf("pin %s: err = %v, want ErrNoAnchor", pin, err)
		}
	}
}

// A version in the anchor's history whose operators cannot be read would hide
// its removals, so Judge refuses.
func TestJudgeUnreadableAdminsInHistory(t *testing.T) {
	f := newFake()
	f.add("h1", 1, "fp-alice", nil, "alice", "mallory")
	f.add("h2", 2, "fp-alice", on("h1"), "alice")
	f.add("h3", 3, "fp-alice", on("h2"), "alice")
	f.add("s2", 2, "fp-mallory", on("h1"), "alice", "mallory")
	g := f.graph()
	g.Operators = func(sha string) (map[string]string, error) {
		if sha == "h2" {
			return nil, errors.New("bad yaml")
		}
		return f.operators[sha], nil
	}
	_, err := Judge(g, alice())
	var u *UnreadableOperatorsError
	if !errors.As(err, &u) || u.SHA != "h2" {
		t.Fatalf("err = %v, want *UnreadableOperatorsError for h2", err)
	}
}

func TestJudgeNilAdmins(t *testing.T) {
	if _, err := Judge(Graph{Versions: chain().vs}, alice()); err == nil {
		t.Fatal("no error with nil Operators")
	}
}

func TestJudgeConflictMessage(t *testing.T) {
	msg := (&ConflictError{A: "ax", B: "cz"}).Error()
	if !strings.Contains(msg, "loop of removals") {
		t.Fatalf("message %q", msg)
	}
}

// A key rotated twice: the stale keys cannot sign on the parents that still
// listed them.
func TestJudgeKeyRotatedTwice(t *testing.T) {
	f := newFake()
	keys := func(sha string, fps ...string) {
		m := map[string]string{}
		for _, fp := range fps {
			m[fp] = "alice"
		}
		f.operators[sha] = m
	}
	f.add("v1", 1, "k1", nil)
	keys("v1", "k1")
	f.add("v2", 2, "k1", on("v1"))
	keys("v2", "k2")
	f.add("v3", 3, "k2", on("v2"))
	keys("v3", "k3")
	f.add("v4", 4, "k3", on("v3"))
	keys("v4", "k3")
	f.add("s2", 2, "k1", on("v1"))
	keys("s2", "k1")
	f.add("s3", 3, "k2", on("v2"))
	keys("s3", "k2")
	for _, a := range []Anchor{{Signers: []string{"k3"}}, {Root: "v1"}} {
		v := judgeOK(t, f, a)
		wantUntrusted(t, v, "s2", "removal")
		wantUntrusted(t, v, "s3", "removal")
		wantTrusted(t, v, "v1", "v2", "v3", "v4")
		wantHeads(t, v, "v4")
	}
}

// An operator removed and later re-added with a new key signs again; their old
// key stays barred.
func TestJudgeRemovedThenReaddedWithNewKey(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice", "bob")
	f.add("v2", 2, "fp-alice", on("v1"), "alice")
	f.add("v3", 3, "fp-alice", on("v2"), "alice")
	f.operators["v3"]["fp-bob2"] = "bob"
	f.add("v4", 4, "fp-bob2", on("v3"), "alice")
	f.operators["v4"]["fp-bob2"] = "bob"
	f.add("v5", 5, "fp-alice", on("v4"), "alice")
	f.operators["v5"]["fp-bob2"] = "bob"
	f.add("s2", 2, "fp-bob", on("v1"), "alice", "bob")
	for _, a := range []Anchor{alice(), {Root: "v1"}} {
		v := judgeOK(t, f, a)
		wantUntrusted(t, v, "s2", "removal")
		wantTrusted(t, v, "v4", "v5")
		wantHeads(t, v, "v5")
	}
}

// A version a newer paisans may have published is untrusted with the
// upgrade remedy, one without a build with the publish remedy, and a pinned
// root without a build says how a new first version is made.
func TestJudgeBuildReasons(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-alice", on("v3"), "alice")
	x := f.vs["v4"]
	x.BuildOK, x.BuildNewer, x.Build = false, true, version.Build{Version: "1.0", Commit: "v1.0"}
	f.vs["v4"] = x
	if v := judgeOK(t, f, Anchor{Root: "v1"}); v.Trusted["v4"] || !strings.Contains(v.Reasons["v4"], "upgrade paisans") {
		t.Errorf("newer: %q", v.Reasons["v4"])
	}
	x.BuildNewer = false
	f.vs["v4"] = x
	if v := judgeOK(t, f, Anchor{Root: "v1"}); !strings.Contains(v.Reasons["v4"], "`paisans deployment publish --execute`") {
		t.Errorf("no build: %q", v.Reasons["v4"])
	}
	f = chain()
	root := f.vs["v1"]
	root.BuildOK = false
	f.vs["v1"] = root
	if _, err := Judge(f.graph(), Anchor{Root: "v1"}); err == nil || !strings.Contains(err.Error(), "`paisans deployment apply --execute` publishes a new first version") {
		t.Errorf("root: %v", err)
	}
}

// named is an anchor of the key --key names.
func named(root, fp string) Anchor { return Anchor{Signers: []string{fp}, Named: true, Root: root} }

// A named key's newest version that the rules trust anchors; a key that
// signed nothing is no anchor.
func TestJudgeNamedKeyAnchors(t *testing.T) {
	f := chain()
	f.add("v4", 4, "fp-bob", on("v3"), "alice", "bob")
	f.operators["v3"] = map[string]string{"fp-alice": "alice", "fp-bob": "bob"}
	v := judgeOK(t, f, named("", "fp-bob"))
	if v.AnchorAt != "v4" {
		t.Fatalf("AnchorAt = %s, want v4", v.AnchorAt)
	}
	wantHeads(t, v, "v4")
	if _, err := Judge(f.graph(), named("", "fp-zed")); !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("err = %v, want ErrNoAnchor", err)
	}
}

// A named key trusts only versions it signed: a newer version by another
// operator is refused, since a key alone cannot tell this deployment's line
// from one it signed before a redeploy under the same id, where an operator
// since removed may still be listed.
func TestJudgeNamedKeyRefusesAnotherOperatorsHead(t *testing.T) {
	f := newFake()
	f.add("o1", 1, "fp-alice", nil, "alice", "mallory")
	f.add("o2", 2, "fp-alice", on("o1"), "alice", "mallory")
	f.add("o3", 3, "fp-mallory", on("o2"), "alice", "mallory")
	_, err := Judge(f.graph(), named("", "fp-alice"))
	if !errors.Is(err, ErrNoAnchor) || !strings.Contains(err.Error(), "o3") || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("err = %v, want ErrNoAnchor naming o3 and --root", err)
	}
	// Pinned, the same line is judged as any pinned history is.
	wantHeads(t, judgeOK(t, f, named("o1", "fp-alice")), "o3")
}

// A named key a version removed is refused, whether its last version is on
// the removal or beside it; the removed key's versions are never trusted.
func TestJudgeNamedRemovedKeyIsRefused(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-old", nil, "old", "new")
	f.add("v2", 2, "fp-new", on("v1"), "new") // removes old
	f.add("x3", 3, "fp-old", on("v2"), "new", "old")
	f.add("y2", 2, "fp-old", on("v1"), "old", "new")
	_, err := Judge(f.graph(), named("", "fp-old"))
	if !errors.Is(err, ErrNoAnchor) || !strings.Contains(err.Error(), "fp-old") {
		t.Fatalf("err = %v, want ErrNoAnchor naming fp-old", err)
	}
	v := judgeOK(t, f, named("", "fp-new"))
	wantUntrusted(t, v, "x3", "signer")
	wantUntrusted(t, v, "y2", "removal")
	wantHeads(t, v, "v2")
}

// A named key no version lists as an operator's anchors nothing.
func TestJudgeNamedKeyNeverAnOperatorIsRefused(t *testing.T) {
	f := chain()
	f.add("x4", 4, "fp-shared", on("v3"), "shared")
	if _, err := Judge(f.graph(), named("", "fp-shared")); !errors.Is(err, ErrNoAnchor) {
		t.Fatalf("err = %v, want ErrNoAnchor", err)
	}
}

// A pinned root anchors alone: the named key neither blocks nor overrides
// it, wherever the key signed.
func TestJudgeNamedKeyPinnedRootWins(t *testing.T) {
	f := chain()
	f.add("o1", 1, "fp-bob", nil, "bob")
	f.add("o2", 2, "fp-bob", on("o1"), "bob")
	for _, fp := range []string{"fp-bob", "fp-alice", "fp-zed"} {
		v := judgeOK(t, f, named("v1", fp))
		if v.AnchorAt != "v1" {
			t.Fatalf("%s: AnchorAt = %s, want v1", fp, v.AnchorAt)
		}
		wantHeads(t, v, "v3")
		wantUntrusted(t, v, "o2", "not trusted")
	}
}

// A merge the named key signed over another operator's version is one line,
// not a fork.
func TestJudgeNamedKeyMergeIsOneLine(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-k1", nil, "k1", "carol")
	f.add("a2", 2, "fp-k1", on("v1"), "k1", "carol")
	f.add("b2", 2, "fp-carol", on("v1"), "k1", "carol")
	f.add("m3", 3, "fp-k1", on("a2", "b2"), "k1", "carol")
	v := judgeOK(t, f, named("", "fp-k1"))
	wantHeads(t, v, "m3")
}

// The heads are the rules' heads, as a recorded key's are.
func TestJudgeNamedKeyHeadsAreTheRulesHeads(t *testing.T) {
	f := newFake()
	f.add("v1", 1, "fp-alice", nil, "alice", "bob")
	f.add("c2", 2, "fp-bob", on("v1"), "alice", "bob")
	f.add("a3", 3, "fp-alice", on("c2"), "alice", "bob")
	f.add("b2", 2, "fp-alice", on("v1"), "alice", "bob")
	f.add("b3", 3, "fp-alice", on("b2"), "alice", "bob")
	f.add("b4", 4, "fp-alice", on("b3"), "alice", "bob")
	want := judgeOK(t, f, Anchor{Signers: []string{"fp-alice"}})
	v := judgeOK(t, f, named("", "fp-alice"))
	wantHeads(t, v, want.Heads...)
	wantHeads(t, v, "b4", "a3")
}

// A named key that signed versions under two roots is refused: which root
// is this deployment's is for a pin to say, never a revision.
func TestJudgeNamedKeyUnderTwoRootsIsRefused(t *testing.T) {
	f := chain()
	f.add("o1", 1, "fp-alice", nil, "alice")
	f.add("o2", 2, "fp-alice", on("o1"), "alice")
	_, err := Judge(f.graph(), named("", "fp-alice"))
	if !errors.Is(err, ErrNoAnchor) || !strings.Contains(err.Error(), "more than one root") {
		t.Fatalf("err = %v, want ErrNoAnchor for two roots", err)
	}
	wantHeads(t, judgeOK(t, f, named("v1", "fp-alice")), "v3")
}

// oldAndNew is an older incarnation, o1 to o4, and a newer one after a
// redeploy under the same id, n1 to n<newRev>, all signed by alice.
func oldAndNew(newRev int) *fake {
	f := newFake()
	f.add("o1", 1, "fp-alice", nil, "alice", "mallory")
	f.add("o2", 2, "fp-alice", on("o1"), "alice", "mallory")
	f.add("o3", 3, "fp-alice", on("o2"), "alice", "mallory")
	f.add("o4", 4, "fp-alice", on("o3"), "alice", "mallory")
	f.add("n1", 1, "fp-alice", nil, "alice")
	prev := "n1"
	for r := 2; r <= newRev; r++ {
		sha := "n" + string(rune('0'+r))
		f.add(sha, r, "fp-alice", on(prev), "alice")
		prev = sha
	}
	return f
}

// serving makes the sites serve only the versions whose names start with
// prefix; the rest are held only in the workstation's cache.
func serving(f *fake, prefix string) Graph {
	g := f.graph()
	g.Served = map[string]bool{}
	for sha := range f.vs {
		if strings.HasPrefix(sha, prefix) {
			g.Served[sha] = true
		}
	}
	return g
}

// A recorded key pinned to one incarnation refuses when the sites serve a
// version it signed under another root, whatever the revisions; a root the
// operator gives for the run judges from it alone; and a version held only in
// the cache, which never forgets, refuses nothing.
func TestJudgePinnedKeyAndAnotherRoot(t *testing.T) {
	alice := []string{"fp-alice"}
	for _, newRev := range []int{2, 9} {
		f := oldAndNew(newRev)
		for _, pin := range []string{"o1", "n1"} {
			var re *RootError
			if _, err := Judge(f.graph(), Anchor{Signers: alice, Root: pin}); !errors.As(err, &re) || !strings.Contains(err.Error(), "--root") {
				t.Fatalf("new line at revision %d, pin %s: err = %v, want a *RootError asking for --root", newRev, pin, err)
			}
		}
		v, err := Judge(f.graph(), Anchor{Signers: alice, Root: "n1", Chosen: true})
		if err != nil {
			t.Fatalf("chosen n1: %v", err)
		}
		wantHeads(t, v, "n"+string(rune('0'+newRev)))
		v, err = Judge(serving(f, "n"), Anchor{Signers: alice, Root: "n1"})
		if err != nil {
			t.Fatalf("pinned n1, the sites serving only its line: %v", err)
		}
		wantHeads(t, v, "n"+string(rune('0'+newRev)))
	}
	// Another operator's root, which the key never signed under, refuses
	// nothing.
	f := chain()
	f.add("m1", 1, "fp-mallory", nil, "mallory")
	wantHeads(t, judgeOK(t, f, Anchor{Signers: alice, Root: "v1"}), "v3")
}

// A pin the sites serve nothing of refuses rather than staying on the cache,
// whoever signed what they serve; a pin given for the run as well.
func TestJudgePinTheSitesDoNotServe(t *testing.T) {
	f := oldAndNew(2)
	for _, a := range []Anchor{{Root: "o1"}, {Root: "o1", Signers: []string{"fp-alice"}}, {Root: "o1", Chosen: true}} {
		var re *RootError
		if _, err := Judge(serving(f, "n"), a); !errors.As(err, &re) || !strings.Contains(err.Error(), "serve no version of the pinned root o1") {
			t.Fatalf("%+v: err = %v", a, err)
		}
	}
}

// A recorded key with no pin that signed under two roots refuses, whatever
// the revisions, rather than picking the higher one.
func TestJudgeUnpinnedKeyUnderTwoRoots(t *testing.T) {
	for _, newRev := range []int{2, 9} {
		var re *RootError
		if _, err := Judge(serving(oldAndNew(newRev), "n"), Anchor{Signers: []string{"fp-alice"}}); !errors.As(err, &re) || !strings.Contains(err.Error(), "more than one root") {
			t.Fatalf("new line at revision %d: err = %v", newRev, err)
		}
	}
}

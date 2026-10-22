package apply_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/apply/etcdwaltest"
)

// testdata/etcd-v3.5.16-wal-head.bin is the first 4096 bytes of the WAL a
// real etcd wrote: gcr.io/etcd-development/etcd:v3.5.16, run alone in
// Docker as --name=fixture with --initial-cluster-state=new, one key put,
// then stopped, and member/wal/0000000000000000-0000000000000000.wal copied
// out. `etcdctl member list -w json` on it answered cluster_id
// 2037210783374497686 and member ID 13195394291058371180.
const (
	fixtureCluster uint64 = 2037210783374497686
	fixtureMember  uint64 = 13195394291058371180
)

func fixtureWAL(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "etcd-v3.5.16-wal-head.bin"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseEtcdWALMetadataReadsARealWAL(t *testing.T) {
	node, cluster, err := apply.ParseEtcdWALMetadata(fixtureWAL(t))
	if err != nil {
		t.Fatal(err)
	}
	if node != fixtureMember || cluster != fixtureCluster {
		t.Errorf("member %d cluster %d, want %d and %d", node, cluster, fixtureMember, fixtureCluster)
	}
}

// The test helper that writes a WAL head writes what etcd writes, byte for
// byte up to the metadata record, so the fakes built on it are etcd's
// format.
func TestTheTestWALIsEtcdsFormat(t *testing.T) {
	real := fixtureWAL(t)
	made := etcdwaltest.Head(fixtureMember, fixtureCluster)
	n := len(made) - 64
	if hex.EncodeToString(made[:n]) != hex.EncodeToString(real[:n]) {
		t.Errorf("made:\n%x\nreal:\n%x", made[:n], real[:n])
	}
	node, cluster, err := apply.ParseEtcdWALMetadata(made)
	if err != nil || node != fixtureMember || cluster != fixtureCluster {
		t.Errorf("member %d cluster %d err %v", node, cluster, err)
	}
}

// Anything that does not decode exactly is an error, never an ID: a
// corrupted byte, a cut head, zeros, text.
func TestParseEtcdWALMetadataRefusesWhatItCannotRead(t *testing.T) {
	real := fixtureWAL(t)
	flipped := append([]byte(nil), real...)
	flipped[30] ^= 0x01 // inside the metadata record's data
	for name, b := range map[string][]byte{
		"a flipped bit": flipped,
		"cut short":     real[:40],
		"zeros":         make([]byte, 4096),
		"text":          []byte("member 7f3a, removed"),
		"nothing":       nil,
	} {
		if _, _, err := apply.ParseEtcdWALMetadata(b); err == nil {
			t.Errorf("%s: read as a WAL", name)
		}
	}
}

func odHex(b []byte) string {
	var lines []string
	for i := 0; i < len(b); i += 16 {
		end := min(i+16, len(b))
		var parts []string
		for _, c := range b[i:end] {
			parts = append(parts, hex.EncodeToString([]byte{c}))
		}
		lines = append(lines, " "+strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

const walA = "0000000000000000-0000000000000000.wal"
const walB = "0000000000000001-0000000000000005.wal"

func etcdListing(entries string, wals map[string][]byte) string {
	var b strings.Builder
	b.WriteString("[rootlesskit:parent] a warning\npaisans-etcd-data: listing\npaisans-list-begin\n" + entries + "paisans-list-end\n")
	names := make([]string, 0, len(wals))
	for n := range wals {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		b.WriteString("paisans-wal-begin " + n + "\n" + odHex(wals[n]) + "\npaisans-wal-end\n")
	}
	return b.String()
}

const memberDirs = "d 0 member\nd 0 member/snap\nd 0 member/wal\nf 16801792 member/snap/db\n"

func TestParseEtcdData(t *testing.T) {
	d, err := apply.ParseEtcdData(etcdListing(memberDirs+"f 64000000 member/wal/"+walA+"\n", map[string][]byte{walA: fixtureWAL(t)}))
	if err != nil {
		t.Fatal(err)
	}
	if d.State != apply.EtcdDataListing || len(d.Entries) != 5 || len(d.WALs[walA]) != 4096 {
		t.Errorf("read %+v", d)
	}
	for _, s := range []string{"absent", "link", "notdir", "unreadable"} {
		if d, err := apply.ParseEtcdData("paisans-etcd-data: " + s + "\n"); err != nil || d.State != s {
			t.Errorf("%s: %+v %v", s, d, err)
		}
	}
	for name, out := range map[string]string{
		"no answer":      "rootlesskit: command not found\n",
		"unknown answer": "paisans-etcd-data: maybe\n",
		"listing cut":    "paisans-etcd-data: listing\npaisans-list-begin\nd 0 member\n",
		"bad listing":    "paisans-etcd-data: listing\npaisans-list-begin\nd member\npaisans-list-end\n",
		"WAL cut":        "paisans-etcd-data: listing\npaisans-list-begin\npaisans-list-end\npaisans-wal-begin x.wal\n 04 00\n",
		"WAL not hex":    "paisans-etcd-data: listing\npaisans-list-begin\npaisans-list-end\npaisans-wal-begin x.wal\n zz\npaisans-wal-end\n",
		"two answers":    "paisans-etcd-data: absent\npaisans-etcd-data: link\n",
		"one WAL twice":  "paisans-etcd-data: listing\npaisans-list-begin\npaisans-list-end\npaisans-wal-begin x.wal\npaisans-wal-end\npaisans-wal-begin x.wal\npaisans-wal-end\n",
	} {
		if d, err := apply.ParseEtcdData(out); err == nil {
			t.Errorf("%s: read as %+v", name, d)
		}
	}
}

// Assess recognises nothing, a failed start and a member whose logs agree,
// each exactly, and nothing else.
func TestAssessEtcdData(t *testing.T) {
	real := fixtureWAL(t)
	other := etcdwaltest.Head(1, 2)
	parse := func(out string) apply.EtcdData {
		t.Helper()
		d, err := apply.ParseEtcdData(out)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	walEntry := func(n string) string { return "f 64000000 member/wal/" + n + "\n" }
	for name, c := range map[string]struct {
		data      apply.EtcdData
		kind, why string
	}{
		"absent":            {apply.EtcdData{State: apply.EtcdDataAbsent}, apply.EtcdDataNothing, ""},
		"empty":             {parse(etcdListing("", nil)), apply.EtcdDataNothing, ""},
		"failed start":      {parse(etcdListing("d 0 member\nd 0 member/snap\nf 16801792 member/snap/db\n", nil)), apply.EtcdDataFailedBootstrap, ""},
		"a member":          {parse(etcdListing(memberDirs+walEntry(walA)+"f 64000000 member/wal/0.tmp\nf 9 member/snap/0000000000000002-0000000000000005.snap\nf 9 member/snap/0000000000000005.snap.db\n", map[string][]byte{walA: real})), apply.EtcdDataMember, ""},
		"two agreeing":      {parse(etcdListing(memberDirs+walEntry(walA)+walEntry(walB), map[string][]byte{walA: real, walB: real})), apply.EtcdDataMember, ""},
		"link":              {apply.EtcdData{State: apply.EtcdDataLink}, apply.EtcdDataUnknown, "symbolic link"},
		"not a directory":   {apply.EtcdData{State: apply.EtcdDataNotDir}, apply.EtcdDataUnknown, "not a directory"},
		"unreadable":        {apply.EtcdData{State: apply.EtcdDataUnreadable}, apply.EtcdDataUnknown, "could not be read"},
		"a moved member":    {parse(etcdListing("d 0 member.bak\nd 0 member.bak/wal\nf 64000000 member.bak/wal/"+walA+"\n", nil)), apply.EtcdDataUnknown, "member.bak"},
		"a stray file":      {parse(etcdListing("f 1 .lock\n", nil)), apply.EtcdDataUnknown, ".lock"},
		"a linked member":   {parse(etcdListing("l 0 member\n", nil)), apply.EtcdDataUnknown, "symbolic link, member"},
		"a linked wal":      {parse(etcdListing("d 0 member\nl 0 member/wal\n", nil)), apply.EtcdDataUnknown, "symbolic link, member/wal"},
		"db too big":        {parse(etcdListing("d 0 member\nd 0 member/snap\nf 16801793 member/snap/db\n", nil)), apply.EtcdDataUnknown, "no write-ahead log"},
		"db and a snap":     {parse(etcdListing("d 0 member\nd 0 member/snap\nf 16801792 member/snap/db\nf 9 member/snap/0000000000000002-0000000000000005.snap\n", nil)), apply.EtcdDataUnknown, "no write-ahead log"},
		"a disagreeing log": {parse(etcdListing(memberDirs+walEntry(walA)+walEntry(walB), map[string][]byte{walA: real, walB: other})), apply.EtcdDataUnknown, "disagree"},
		"an unread log":     {parse(etcdListing(memberDirs+walEntry(walA)+walEntry(walB), map[string][]byte{walA: real})), apply.EtcdDataUnknown, "not read"},
		"a bad log":         {parse(etcdListing(memberDirs+walEntry(walA), map[string][]byte{walA: []byte("nope")})), apply.EtcdDataUnknown, "does not decode"},
	} {
		v := c.data.Assess()
		if v.Kind != c.kind || !strings.Contains(v.Why, c.why) {
			t.Errorf("%s: %+v, want %s with %q", name, v, c.kind, c.why)
		}
		if v.Kind == apply.EtcdDataMember && (v.ClusterID != fixtureCluster || v.NodeID != fixtureMember) {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// The probe, run by a real shell against a tree under a temporary root,
// answers each state, lists everything without following links, and reads
// every WAL file.
func TestEtcdDataProbeUnderAShell(t *testing.T) {
	dir := "srv/paisans/f2a9/infra/etcd"
	read := func(base string) apply.EtcdData {
		t.Helper()
		out, err := runRootless(t, base, apply.EtcdDataProbe(rmDeployment))
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		d, err := apply.ParseEtcdData(out)
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return d
	}
	base := t.TempDir()
	if v := read(base).Assess(); v.Kind != apply.EtcdDataNothing {
		t.Errorf("no directory: %+v", v)
	}
	if err := os.MkdirAll(filepath.Join(base, dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if d := read(base); d.State != apply.EtcdDataListing || d.Assess().Kind != apply.EtcdDataNothing {
		t.Errorf("an empty directory: %+v", d)
	}
	db := filepath.Join(base, dir, "member/snap/db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := read(base).Assess(); v.Kind != apply.EtcdDataFailedBootstrap {
		t.Errorf("a failed start: %+v", v)
	}
	wal := filepath.Join(base, dir, "member/wal")
	if err := os.MkdirAll(wal, 0o700); err != nil {
		t.Fatal(err)
	}
	full := append(fixtureWAL(t), make([]byte, 8192)...)
	if err := os.WriteFile(filepath.Join(wal, walA), full, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wal, "0.tmp"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	d := read(base)
	if v := d.Assess(); v.Kind != apply.EtcdDataMember || v.ClusterID != fixtureCluster || v.NodeID != fixtureMember {
		t.Errorf("a member: %+v\n%+v", v, d.Entries)
	}
	// A newer WAL of another cluster behind this one is read too, and the
	// logs disagree.
	if err := os.WriteFile(filepath.Join(wal, walB), etcdwaltest.Head(1, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := read(base).Assess(); v.Kind != apply.EtcdDataUnknown || !strings.Contains(v.Why, "disagree") {
		t.Errorf("a second WAL of another cluster: %+v", v)
	}
	if err := os.Remove(filepath.Join(wal, walB)); err != nil {
		t.Fatal(err)
	}
	// A member directory moved aside by hand, with another cluster's WAL.
	moved := filepath.Join(base, dir, "member.bak/wal")
	if err := os.MkdirAll(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moved, walA), etcdwaltest.Head(1, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := read(base).Assess(); v.Kind != apply.EtcdDataUnknown || !strings.Contains(v.Why, "member.bak") {
		t.Errorf("a moved member directory: %+v", v)
	}
	if err := os.RemoveAll(filepath.Join(base, dir, "member.bak")); err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() != 0 {
		if err := os.Chmod(wal, 0); err != nil {
			t.Fatal(err)
		}
		if d := read(base); d.State != apply.EtcdDataUnreadable {
			t.Errorf("a directory that cannot be walked: %+v", d.State)
		}
		if err := os.Chmod(wal, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// A symlinked WAL directory is listed as a link and not read through.
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, walA), etcdwaltest.Head(1, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wal); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, wal); err != nil {
		t.Fatal(err)
	}
	if d := read(base); len(d.WALs) != 0 || !strings.Contains(d.Assess().Why, "symbolic link, member/wal") {
		t.Errorf("a symlinked WAL directory: %+v %+v", d.Assess(), d.WALs)
	}

	notdir := t.TempDir()
	writeTree(t, notdir, dir)
	if d := read(notdir); d.State != apply.EtcdDataNotDir {
		t.Errorf("a file at the data path: %+v", d)
	}
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "srv/paisans/f2a9/infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, dir), filepath.Join(other, dir)); err != nil {
		t.Fatal(err)
	}
	if d := read(other); d.State != apply.EtcdDataLink {
		t.Errorf("a symlinked directory: %+v", d)
	}
}

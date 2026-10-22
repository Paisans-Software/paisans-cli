package apply_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
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

func TestParseEtcdData(t *testing.T) {
	wal := "paisans-etcd-data: wal 0000000000000000-0000000000000000.wal\npaisans-wal-begin\n" + odHex(fixtureWAL(t)) + "\npaisans-wal-end\n"
	d, err := apply.ParseEtcdData("[rootlesskit:parent] some warning\n" + wal)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != apply.EtcdDataWAL || d.ClusterID != fixtureCluster || d.NodeID != fixtureMember || d.WAL != "0000000000000000-0000000000000000.wal" {
		t.Errorf("read %+v", d)
	}
	for _, s := range []string{"absent", "empty", "nomember", "nowal", "link", "unreadable"} {
		if d, err := apply.ParseEtcdData("paisans-etcd-data: " + s + "\n"); err != nil || d.State != s {
			t.Errorf("%s: %+v %v", s, d, err)
		}
	}
	for name, out := range map[string]string{
		"no answer":         "rootlesskit: command not found\n",
		"unknown answer":    "paisans-etcd-data: maybe\n",
		"cut before end":    strings.TrimSuffix(wal, "paisans-wal-end\n"),
		"not a WAL":         "paisans-etcd-data: wal x.wal\npaisans-wal-begin\n 6d 65 6d\npaisans-wal-end\n",
		"two answers":       "paisans-etcd-data: empty\npaisans-etcd-data: absent\n",
		"wal with no bytes": "paisans-etcd-data: wal x.wal\npaisans-wal-begin\npaisans-wal-end\n",
	} {
		if d, err := apply.ParseEtcdData(out); err == nil {
			t.Errorf("%s: read as %+v", name, d)
		}
	}
}

// The probe, run by a real shell against a tree under a temporary root,
// answers each state, and the WAL it reads is the one parsed.
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
	if d := read(base); d.State != apply.EtcdDataAbsent {
		t.Errorf("no directory: %+v", d)
	}
	if err := os.MkdirAll(filepath.Join(base, dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if d := read(base); d.State != apply.EtcdDataEmpty {
		t.Errorf("empty directory: %+v", d)
	}
	writeTree(t, base, dir+"/.lock")
	if d := read(base); d.State != apply.EtcdDataNoMember {
		t.Errorf("no member directory: %+v", d)
	}
	writeTree(t, base, dir+"/member/snap/db")
	if d := read(base); d.State != apply.EtcdDataNoWAL {
		t.Errorf("no WAL: %+v", d)
	}
	wal := filepath.Join(base, dir, "member/wal")
	if err := os.MkdirAll(wal, 0o700); err != nil {
		t.Fatal(err)
	}
	full := append(fixtureWAL(t), make([]byte, 8192)...)
	if err := os.WriteFile(filepath.Join(wal, "0000000000000001-0000000000000005.wal"), etcdwaltest.Head(1, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wal, "0000000000000000-0000000000000000.wal"), full, 0o600); err != nil {
		t.Fatal(err)
	}
	if d := read(base); d.State != apply.EtcdDataWAL || d.ClusterID != fixtureCluster || d.WAL != "0000000000000000-0000000000000000.wal" {
		t.Errorf("a WAL, the first by name: %+v", d)
	}

	if os.Geteuid() != 0 {
		if err := os.Chmod(filepath.Join(base, dir), 0); err != nil {
			t.Fatal(err)
		}
		if d := read(base); d.State != apply.EtcdDataUnreadable {
			t.Errorf("a directory that cannot be listed: %+v", d)
		}
		if err := os.Chmod(filepath.Join(base, dir), 0o700); err != nil {
			t.Fatal(err)
		}
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

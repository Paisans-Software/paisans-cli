package apply

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"google.golang.org/protobuf/encoding/protowire"
)

// What EtcdDataProbe finds at a site's etcd data path, read on the host,
// offline, whether or not an etcd container runs there.
const (
	// EtcdDataAbsent: nothing at the path.
	EtcdDataAbsent = "absent"
	// EtcdDataLink: the path is a symbolic link.
	EtcdDataLink = "link"
	// EtcdDataNotDir: the path is not a directory.
	EtcdDataNotDir = "notdir"
	// EtcdDataUnreadable: the directory could not be walked.
	EtcdDataUnreadable = "unreadable"
	// EtcdDataListing: a directory, with Entries and the WAL heads read.
	EtcdDataListing = "listing"
)

// EtcdData is what a site's etcd data path holds.
type EtcdData struct {
	State string
	// Entries is everything under the directory, when State is
	// EtcdDataListing.
	Entries []EtcdDataEntry
	// WALs is the head of every write-ahead log file under member/wal, by
	// name, when State is EtcdDataListing.
	WALs map[string][]byte
	// DB is the head of member/snap/db, its first two pages, when it is a
	// regular file reached through no link.
	DB []byte
}

// EtcdDataEntry is one path under the directory: its type, f a regular
// file, d a directory, l a symbolic link and o anything else; its size,
// for a regular file; and its path relative to the directory.
type EtcdDataEntry struct {
	Type string
	Size int64
	Path string
}

// FreshEtcdDBSize is the size of member/snap/db as etcd v3.5.16 leaves it
// when it exits before it reaches any peer: it opens its backend first and
// creates its write-ahead log only once it has the cluster's membership
// (server/etcdserver/server.go, NewServer). Measured, not derived: the
// v3.5.16 image (linux/arm64) run with --initial-cluster-state=existing
// and an --initial-cluster whose only other member does not answer exited
// with "discovery failed" and left member/snap/db, 16801792 bytes, and
// nothing else.
//
// The size alone is no evidence the database is fresh: bbolt grows the file
// in 16 MiB steps, so a member holding a few keys has a file of the same
// size. FreshEtcdDBTxid is.
const FreshEtcdDBSize = 16801792

// FreshEtcdDBTxid is the highest transaction ID in the two bbolt meta pages
// of that database: etcd's bootstrap commits two transactions before it
// reaches a peer, and every write after them commits another. Measured on
// the same database: meta page 0 at txid 2 and page 1 at txid 1. A member
// that held one key and was stopped had txids 8 and 9 in a file of the same
// size.
const FreshEtcdDBTxid = 2

// boltPageSize is the only bbolt page size read: the page size of the hosts
// measured and of linux/amd64 and most linux/arm64 kernels. A database with
// another is not read as a fresh one.
const boltPageSize = 4096

const (
	etcdDataMark  = "paisans-etcd-data: "
	etcdListBegin = "paisans-list-begin"
	etcdListEnd   = "paisans-list-end"
	etcdWALBegin  = "paisans-wal-begin "
	etcdWALEnd    = "paisans-wal-end"
	etcdDBBegin   = "paisans-db-begin"
	etcdDBEnd     = "paisans-db-end"
	// etcdWALHead is how much of each write-ahead log file is read. Every
	// WAL file starts with a CRC record and then the metadata record (etcd
	// v3.5.16, server/storage/wal/wal.go, Create and cut), a few dozen
	// bytes in.
	etcdWALHead = 4096
	// etcdMaxWALs is the most WAL files read. etcd keeps five by default
	// (--max-wals); more than this many is not a layout this reads.
	etcdMaxWALs = 64
	walMetadata = 1 // walpb record type metadataType
	walCRC      = 4 // walpb record type crcType
	walMaxRecs  = 16
)

// EtcdDataProbe reads a site's etcd data path: what it is, every path under
// it with its type and size, and the head of every write-ahead log file in
// member/wal and of the first two pages of member/snap/db, as od prints
// them. It runs through rootlesskit, as root of the
// user namespace rootless Docker runs containers in, because the files are
// the etcd container's and are expected to be owned by a subordinate UID
// that the deployment's user cannot read. find does not follow symbolic
// links and the WAL heads are not read through one. The answer is between
// marker lines, so anything else the command prints is ignored, and a
// failure to walk the directory is an answer of its own.
func EtcdDataProbe(d deployment.Deployment) string {
	script := fmt.Sprintf(`d=%s
m=%s
if [ -L "$d" ]; then echo "${m}link"; exit 0; fi
if [ ! -e "$d" ]; then echo "${m}absent"; exit 0; fi
if [ ! -d "$d" ]; then echo "${m}notdir"; exit 0; fi
l=$(find "$d" -mindepth 1) || { echo "${m}unreadable"; exit 0; }
echo "${m}listing"
echo %s
printf '%%s\n' "$l" | while IFS= read -r p; do
  [ -n "$p" ] || continue
  r=${p#"$d"/}
  if [ -L "$p" ]; then echo "l 0 $r"
  elif [ -d "$p" ]; then echo "d 0 $r"
  elif [ -f "$p" ]; then echo "f $(wc -c < "$p" | tr -d ' ') $r"
  else echo "o 0 $r"; fi
done
echo %s
if [ ! -L "$d/member" ] && [ ! -L "$d/member/wal" ]; then
  n=0
  for f in "$d"/member/wal/*.wal; do
    [ -f "$f" ] && [ ! -L "$f" ] || continue
    n=$((n + 1)); [ "$n" -le %d ] || break
    echo "%s${f##*/}"
    head -c %d "$f" | od -An -v -tx1
    echo %s
  done
fi
if [ ! -L "$d/member" ] && [ ! -L "$d/member/snap" ] && [ -f "$d/member/snap/db" ] && [ ! -L "$d/member/snap/db" ]; then
  echo %s
  head -c %d "$d/member/snap/db" | od -An -v -tx1
  echo %s
fi`, shellQuote(EtcdDataDir(d)), shellQuote(etcdDataMark), etcdListBegin, etcdListEnd, etcdMaxWALs, etcdWALBegin, etcdWALHead, etcdWALEnd, etcdDBBegin, 2*boltPageSize, etcdDBEnd)
	return "rootlesskit sh -c " + shellQuote(script)
}

// ReadEtcdData reads a site's etcd data path with EtcdDataProbe.
func ReadEtcdData(t Transport, d deployment.Deployment) (EtcdData, error) {
	out, err := t.Run(EtcdDataProbe(d))
	if err != nil {
		return EtcdData{}, fmt.Errorf("reading %s: %w: %s", EtcdDataDir(d), err, strings.TrimSpace(out))
	}
	return ParseEtcdData(out)
}

// ParseEtcdData reads EtcdDataProbe's answer. Anything it does not read
// exactly is an error, never an answer.
func ParseEtcdData(out string) (EtcdData, error) {
	var data EtcdData
	var listing, ended bool
	var wal string
	var inDB, sawDB bool
	var hexed strings.Builder
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case inDB && line == etcdDBEnd:
			head, err := hex.DecodeString(hexed.String())
			if err != nil {
				return EtcdData{}, fmt.Errorf("the head of member/snap/db is not hex: %w", err)
			}
			data.DB, inDB, sawDB = head, false, true
			hexed.Reset()
		case inDB:
			hexed.WriteString(strings.ReplaceAll(strings.TrimSpace(line), " ", ""))
		case ended && wal == "" && line == etcdDBBegin:
			if sawDB {
				return EtcdData{}, errors.New("the etcd data probe read member/snap/db twice")
			}
			inDB = true
		case wal != "" && line == etcdWALEnd:
			head, err := hex.DecodeString(hexed.String())
			if err != nil {
				return EtcdData{}, fmt.Errorf("the head of %s is not hex: %w", wal, err)
			}
			data.WALs[wal] = head
			wal = ""
			hexed.Reset()
		case wal != "":
			hexed.WriteString(strings.ReplaceAll(strings.TrimSpace(line), " ", ""))
		case strings.HasPrefix(line, etcdDataMark):
			if data.State != "" {
				return EtcdData{}, errors.New("the etcd data probe answered twice")
			}
			data.State = strings.TrimPrefix(line, etcdDataMark)
		case line == etcdListBegin:
			listing = true
		case line == etcdListEnd:
			listing, ended = false, true
		case listing:
			f := strings.SplitN(line, " ", 3)
			size, err := strconv.ParseInt(f[min(1, len(f)-1)], 10, 64)
			if len(f) != 3 || len(f[0]) != 1 || err != nil || f[2] == "" {
				return EtcdData{}, fmt.Errorf("the etcd data listing has a line it cannot read: %q", line)
			}
			if seen[f[2]] {
				return EtcdData{}, fmt.Errorf("the etcd data listing names %q twice", f[2])
			}
			seen[f[2]] = true
			data.Entries = append(data.Entries, EtcdDataEntry{Type: f[0], Size: size, Path: f[2]})
		case ended && strings.HasPrefix(line, etcdWALBegin):
			wal = strings.TrimPrefix(line, etcdWALBegin)
			if data.WALs == nil {
				data.WALs = map[string][]byte{}
			}
			if _, dup := data.WALs[wal]; dup || wal == "" {
				return EtcdData{}, fmt.Errorf("the etcd data probe read %q twice", wal)
			}
		}
	}
	switch data.State {
	case EtcdDataAbsent, EtcdDataLink, EtcdDataNotDir, EtcdDataUnreadable:
		return EtcdData{State: data.State}, nil
	case EtcdDataListing:
		if !ended || wal != "" || inDB {
			return EtcdData{}, errors.New("the etcd data probe's answer was cut short")
		}
		return data, nil
	case "":
		return EtcdData{}, fmt.Errorf("the etcd data probe gave no answer: %s", strings.TrimSpace(out))
	}
	return EtcdData{}, fmt.Errorf("the etcd data probe answered %q", data.State)
}

// What EtcdData.Assess makes of what a path holds.
const (
	// EtcdDataNothing: no directory, or an empty one. Nothing to lose.
	EtcdDataNothing = "nothing"
	// EtcdDataFailedBootstrap: member/snap/db alone, no larger than a fresh
	// one, whose two bbolt meta pages are valid and record no transaction
	// after the ones etcd's bootstrap commits (FreshEtcdDBSize,
	// FreshEtcdDBTxid): what etcd leaves when it exits before it reaches a
	// peer. Nothing was committed to it after its buckets were created.
	EtcdDataFailedBootstrap = "failed-bootstrap"
	// EtcdDataMember: etcd's member layout, with write-ahead logs that all
	// name the same member and cluster.
	EtcdDataMember = "member"
	// EtcdDataUnknown: anything else. Why says what was found.
	EtcdDataUnknown = "unknown"
)

// EtcdDataVerdict is what a path holds, as far as its files show.
type EtcdDataVerdict struct {
	Kind      string
	NodeID    uint64
	ClusterID uint64
	Why       string
}

var (
	walName    = regexp.MustCompile(`^member/wal/[0-9a-f]{16}-[0-9a-f]{16}\.wal$`)
	walTmp     = regexp.MustCompile(`^member/wal/[0-9]+\.tmp$`)
	snapName   = regexp.MustCompile(`^member/snap/[0-9a-f]{16}-[0-9a-f]{16}\.snap$`)
	snapDBName = regexp.MustCompile(`^member/snap/[0-9a-f]{16}\.snap\.db$`)
)

// Assess says what d holds: nothing, a failed bootstrap, a member whose
// write-ahead logs agree on its member and cluster IDs, or something else.
// Only the first three are recognised, each exactly: etcd's member layout is
// the directories member, member/snap and member/wal, and in them the
// backend db, snapshots, received snapshot databases, WAL files and the WAL
// files etcd preallocates (N.tmp), by etcd v3.5.16's names
// (server/storage/wal/util.go walName, server/etcdserver/api/snap). A
// symbolic link anywhere, any other name, or a WAL that does not decode is
// unknown.
func (d EtcdData) Assess() EtcdDataVerdict {
	unknown := func(format string, a ...any) EtcdDataVerdict {
		return EtcdDataVerdict{Kind: EtcdDataUnknown, Why: fmt.Sprintf(format, a...)}
	}
	switch d.State {
	case EtcdDataAbsent:
		return EtcdDataVerdict{Kind: EtcdDataNothing}
	case EtcdDataLink:
		return unknown("is a symbolic link")
	case EtcdDataNotDir:
		return unknown("is not a directory")
	case EtcdDataUnreadable:
		return unknown("could not be read")
	case EtcdDataListing:
	default:
		return unknown("was answered as %q", d.State)
	}
	if len(d.Entries) == 0 {
		return EtcdDataVerdict{Kind: EtcdDataNothing}
	}
	var wals []string
	paths := map[string]EtcdDataEntry{}
	for _, e := range d.Entries {
		paths[e.Path] = e
		switch {
		case e.Type == "l":
			return unknown("holds a symbolic link, %s", e.Path)
		case e.Type == "d" && (e.Path == "member" || e.Path == "member/snap" || e.Path == "member/wal"):
		case e.Type == "f" && walName.MatchString(e.Path):
			wals = append(wals, strings.TrimPrefix(e.Path, "member/wal/"))
		case e.Type == "f" && (e.Path == "member/snap/db" || walTmp.MatchString(e.Path) || snapName.MatchString(e.Path) || snapDBName.MatchString(e.Path)):
		default:
			return unknown("holds %s, which is not etcd's member layout", e.Path)
		}
	}
	if len(wals) == 0 {
		db, ok := paths["member/snap/db"]
		if len(d.Entries) == 3 && ok && paths["member"].Type == "d" && paths["member/snap"].Type == "d" && db.Size <= FreshEtcdDBSize {
			txid, err := ParseBoltTxid(d.DB)
			if err != nil {
				return unknown("holds an etcd database with no write-ahead log whose meta pages cannot be read (%v), so whose it is cannot be read", err)
			}
			if txid <= FreshEtcdDBTxid {
				return EtcdDataVerdict{Kind: EtcdDataFailedBootstrap}
			}
			return unknown("holds an etcd database with no write-ahead log that has committed transaction %d, more than a failed start commits, so whose it is cannot be read", txid)
		}
		return unknown("holds an etcd member directory with no write-ahead log and more than a failed start leaves, so whose it is cannot be read")
	}
	if len(wals) > etcdMaxWALs {
		return unknown("holds %d write-ahead log files, more than are read", len(wals))
	}
	sort.Strings(wals)
	var verdict EtcdDataVerdict
	for i, name := range wals {
		head, ok := d.WALs[name]
		if !ok {
			return unknown("holds a write-ahead log, %s, that was not read", name)
		}
		node, cluster, err := ParseEtcdWALMetadata(head)
		if err != nil {
			return unknown("holds a write-ahead log, %s, that does not decode: %v", name, err)
		}
		if i == 0 {
			verdict = EtcdDataVerdict{Kind: EtcdDataMember, NodeID: node, ClusterID: cluster}
		} else if node != verdict.NodeID || cluster != verdict.ClusterID {
			return unknown("holds write-ahead logs that disagree: %s names member %x of cluster %x, and %s member %x of cluster %x", wals[0], verdict.NodeID, verdict.ClusterID, name, node, cluster)
		}
	}
	if len(d.WALs) != len(wals) {
		return unknown("had a write-ahead log read that its listing does not hold")
	}
	return verdict
}

// ParseEtcdWALMetadata finds the metadata record at the head of an etcd
// write-ahead log file and returns the member and cluster IDs it records.
//
// The format is etcd v3.5.16's (server/storage/wal: encoder.go, decoder.go,
// wal.go; walpb/record.proto; etcdserverpb/etcdserver.proto). The file is a
// sequence of frames, each an 8 byte little-endian length word and then a
// record padded to 8 bytes: the low 56 bits of the word are the record's
// length, and when the top bit is set the next three give the padding.
// Each record is a walpb.Record, protobuf fields 1 type, 2 crc and 3 data.
// A file starts with a CRC record, whose crc carries the running checksum
// over, and then the metadata record, whose data is an
// etcdserverpb.Metadata, fields 1 NodeID and 2 ClusterID. The metadata
// record's crc is CRC-32C of its data continuing the running checksum, and
// it is checked: anything that does not decode exactly is an error.
func ParseEtcdWALMetadata(b []byte) (nodeID, clusterID uint64, err error) {
	table := crc32.MakeTable(crc32.Castagnoli)
	var running uint32
	sawCRC := false
	for i := 0; i < walMaxRecs; i++ {
		if len(b) < 8 {
			return 0, 0, errors.New("no metadata record in the head of the WAL")
		}
		word := binary.LittleEndian.Uint64(b[:8])
		b = b[8:]
		if word == 0 {
			return 0, 0, errors.New("the WAL ends before its metadata record")
		}
		length := word & (1<<56 - 1)
		pad := uint64(0)
		if word&(1<<63) != 0 {
			pad = (word >> 56) & 0x7
		}
		if length+pad > uint64(len(b)) {
			return 0, 0, errors.New("a WAL record runs past the head that was read")
		}
		typ, crc, data, err := parseWALRecord(b[:length])
		if err != nil {
			return 0, 0, err
		}
		b = b[length+pad:]
		switch typ {
		case walCRC:
			running, sawCRC = crc, true
		case walMetadata:
			if !sawCRC {
				return 0, 0, errors.New("the WAL's metadata record has no CRC record before it")
			}
			if got := crc32.Update(running, table, data); got != crc {
				return 0, 0, fmt.Errorf("the WAL's metadata record fails its checksum: %08x, want %08x", got, crc)
			}
			return parseWALMetadata(data)
		default:
			return 0, 0, fmt.Errorf("the WAL has a record of type %d before its metadata record", typ)
		}
	}
	return 0, 0, errors.New("no metadata record in the head of the WAL")
}

func parseWALRecord(b []byte) (typ int64, crc uint32, data []byte, err error) {
	seen := map[protowire.Number]bool{}
	for len(b) > 0 {
		num, wt, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0, 0, nil, errors.New("a WAL record is not a protobuf message")
		}
		b = b[n:]
		if seen[num] {
			return 0, 0, nil, fmt.Errorf("a WAL record repeats field %d", num)
		}
		seen[num] = true
		switch {
		case num == 1 && wt == protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return 0, 0, nil, errors.New("a WAL record's type is not a varint")
			}
			typ, b = int64(v), b[n:]
		case num == 2 && wt == protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 || v > 1<<32-1 {
				return 0, 0, nil, errors.New("a WAL record's crc is not a 32 bit varint")
			}
			crc, b = uint32(v), b[n:]
		case num == 3 && wt == protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return 0, 0, nil, errors.New("a WAL record's data runs past the record")
			}
			data, b = v, b[n:]
		default:
			return 0, 0, nil, fmt.Errorf("a WAL record has an unknown field %d", num)
		}
	}
	if !seen[1] {
		return 0, 0, nil, errors.New("a WAL record has no type")
	}
	return typ, crc, data, nil
}

func parseWALMetadata(b []byte) (nodeID, clusterID uint64, err error) {
	seen := map[protowire.Number]bool{}
	for len(b) > 0 {
		num, wt, n := protowire.ConsumeTag(b)
		if n < 0 || wt != protowire.VarintType || (num != 1 && num != 2) || seen[num] {
			return 0, 0, errors.New("the WAL's metadata is not an etcd member and cluster ID")
		}
		b = b[n:]
		v, n := protowire.ConsumeVarint(b)
		if n < 0 {
			return 0, 0, errors.New("the WAL's metadata is not an etcd member and cluster ID")
		}
		b = b[n:]
		seen[num] = true
		if num == 1 {
			nodeID = v
		} else {
			clusterID = v
		}
	}
	if clusterID == 0 {
		return 0, 0, errors.New("the WAL's metadata names no cluster")
	}
	return nodeID, clusterID, nil
}

// ParseBoltTxid reads the two meta pages at the head of a bbolt database and
// returns the higher of their transaction IDs. Both pages must be valid:
// bbolt's magic and version 2, the page size boltPageSize, and each page's
// FNV-1a checksum over its meta fields (go.etcd.io/bbolt v1.3, page.go and
// db.go: a page header of 16 bytes, then magic, version, pageSize, flags,
// the root bucket's page and sequence, freelist, pgid, txid and checksum,
// little-endian).
func ParseBoltTxid(b []byte) (uint64, error) {
	if len(b) < 2*boltPageSize {
		return 0, fmt.Errorf("%d bytes read, fewer than two %d byte pages", len(b), boltPageSize)
	}
	var top uint64
	for i := 0; i < 2; i++ {
		page := b[i*boltPageSize : (i+1)*boltPageSize]
		if id := binary.LittleEndian.Uint64(page[0:]); id != uint64(i) {
			return 0, fmt.Errorf("page %d says it is page %d", i, id)
		}
		if flags := binary.LittleEndian.Uint16(page[8:]); flags != 0x04 {
			return 0, fmt.Errorf("page %d is not a meta page (flags %#x)", i, flags)
		}
		m := page[16:]
		if magic := binary.LittleEndian.Uint32(m[0:]); magic != 0xED0CDAED {
			return 0, fmt.Errorf("page %d has no bbolt magic", i)
		}
		if v := binary.LittleEndian.Uint32(m[4:]); v != 2 {
			return 0, fmt.Errorf("page %d is bbolt version %d", i, v)
		}
		if ps := binary.LittleEndian.Uint32(m[8:]); ps != boltPageSize {
			return 0, fmt.Errorf("page %d has page size %d", i, ps)
		}
		h := fnv.New64a()
		h.Write(m[:56])
		if h.Sum64() != binary.LittleEndian.Uint64(m[56:]) {
			return 0, fmt.Errorf("page %d fails its checksum", i)
		}
		if txid := binary.LittleEndian.Uint64(m[48:]); txid > top {
			top = txid
		}
	}
	return top, nil
}

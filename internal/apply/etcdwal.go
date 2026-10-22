package apply

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"google.golang.org/protobuf/encoding/protowire"
)

// What a site's etcd data directory holds, as EtcdDataProbe reads it on the
// host, offline, whether or not an etcd container runs there.
const (
	// EtcdDataAbsent: no directory at all.
	EtcdDataAbsent = "absent"
	// EtcdDataEmpty: a directory with nothing in it.
	EtcdDataEmpty = "empty"
	// EtcdDataNoMember: entries, none of them etcd's member directory, so no
	// member's data.
	EtcdDataNoMember = "nomember"
	// EtcdDataNoWAL: a member directory with no write-ahead log to say whose
	// it is.
	EtcdDataNoWAL = "nowal"
	// EtcdDataLink: the directory is a symbolic link.
	EtcdDataLink = "link"
	// EtcdDataUnreadable: the directory could not be listed.
	EtcdDataUnreadable = "unreadable"
	// EtcdDataWAL: a member directory with a write-ahead log, whose metadata
	// names the member and the cluster.
	EtcdDataWAL = "wal"
)

// EtcdData is what a site's etcd data directory holds.
type EtcdData struct {
	State string
	// WAL is the write-ahead log file read, and NodeID and ClusterID the
	// member and cluster its metadata record names, when State is
	// EtcdDataWAL.
	WAL       string
	NodeID    uint64
	ClusterID uint64
}

// etcdWALHead is how much of a write-ahead log file is read. Every WAL file
// starts with a CRC record and then the metadata record (etcd v3.5.16,
// server/storage/wal/wal.go, Create and cut), a few dozen bytes in.
const etcdWALHead = 4096

const (
	etcdDataMark  = "paisans-etcd-data: "
	etcdWALBegin  = "paisans-wal-begin"
	etcdWALEnd    = "paisans-wal-end"
	walMetadata   = 1 // walpb record type metadataType
	walCRC        = 4 // walpb record type crcType
	walMaxRecords = 16
)

// EtcdDataProbe reads a site's etcd data directory: whether it is there,
// whether it holds etcd's member directory, and the head of its first
// write-ahead log file as hex. It runs through rootlesskit, as root of the
// user namespace rootless Docker runs containers in, because the files are
// the etcd container's and are expected to be owned by a subordinate UID
// that the deployment's user cannot read. Its answer is between marker
// lines, so anything else the command prints is ignored. Every failure to
// read leaves an answer that permits nothing: an unlisted directory is
// unreadable, an unlisted WAL directory has no WAL, and an unread WAL file
// has no metadata.
func EtcdDataProbe(d deployment.Deployment) string {
	script := fmt.Sprintf(`d=%s
m=%s
if [ -L "$d" ]; then echo "${m}link"; exit 0; fi
if [ ! -e "$d" ]; then echo "${m}absent"; exit 0; fi
n=$(ls -A "$d") || { echo "${m}unreadable"; exit 0; }
if [ -z "$n" ]; then echo "${m}empty"; exit 0; fi
if ! printf '%%s\n' "$n" | grep -qx member; then echo "${m}nomember"; exit 0; fi
w=$(ls -1 "$d/member/wal" 2>/dev/null | grep '\.wal$' | sort | head -n 1)
if [ -z "$w" ]; then echo "${m}nowal"; exit 0; fi
echo "${m}wal $w"
echo %s
head -c %d "$d/member/wal/$w" | od -An -v -tx1
echo %s`, shellQuote(EtcdDataDir(d)), shellQuote(etcdDataMark), etcdWALBegin, etcdWALHead, etcdWALEnd)
	return "rootlesskit sh -c " + shellQuote(script)
}

// ReadEtcdData reads a site's etcd data directory with EtcdDataProbe.
func ReadEtcdData(t Transport, d deployment.Deployment) (EtcdData, error) {
	out, err := t.Run(EtcdDataProbe(d))
	if err != nil {
		return EtcdData{}, fmt.Errorf("reading %s: %w: %s", EtcdDataDir(d), err, strings.TrimSpace(out))
	}
	return ParseEtcdData(out)
}

// ParseEtcdData reads EtcdDataProbe's answer. A WAL whose metadata cannot
// be read is an error, never an answer.
func ParseEtcdData(out string) (EtcdData, error) {
	var state string
	var hexed strings.Builder
	in, ended := false, false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, etcdDataMark):
			if state != "" {
				return EtcdData{}, errors.New("the etcd data probe answered twice")
			}
			state = strings.TrimPrefix(line, etcdDataMark)
		case line == etcdWALBegin:
			in = true
		case line == etcdWALEnd:
			in, ended = false, true
		case in:
			hexed.WriteString(strings.ReplaceAll(line, " ", ""))
		}
	}
	switch state {
	case EtcdDataAbsent, EtcdDataEmpty, EtcdDataNoMember, EtcdDataNoWAL, EtcdDataLink, EtcdDataUnreadable:
		return EtcdData{State: state}, nil
	case "":
		return EtcdData{}, fmt.Errorf("the etcd data probe gave no answer: %s", strings.TrimSpace(out))
	}
	name, ok := strings.CutPrefix(state, EtcdDataWAL+" ")
	if !ok || name == "" {
		return EtcdData{}, fmt.Errorf("the etcd data probe answered %q", state)
	}
	if !ended {
		return EtcdData{}, fmt.Errorf("the head of %s was not read to its end", name)
	}
	head, err := hex.DecodeString(hexed.String())
	if err != nil {
		return EtcdData{}, fmt.Errorf("the head of %s is not hex: %w", name, err)
	}
	node, cluster, err := ParseEtcdWALMetadata(head)
	if err != nil {
		return EtcdData{}, fmt.Errorf("%s: %w", name, err)
	}
	return EtcdData{State: EtcdDataWAL, WAL: name, NodeID: node, ClusterID: cluster}, nil
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
	for i := 0; i < walMaxRecords; i++ {
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

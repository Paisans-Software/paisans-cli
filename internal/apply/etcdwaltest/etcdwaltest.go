// Package etcdwaltest writes the head of an etcd write-ahead log file, for
// tests of what reads one: a CRC record and a metadata record, framed and
// checksummed as etcd v3.5.16 writes them (server/storage/wal), then zeros,
// as etcd preallocates the file. apply's tests check it against a WAL a real
// etcd wrote.
package etcdwaltest

import (
	"encoding/binary"
	"hash/crc32"

	"google.golang.org/protobuf/encoding/protowire"
)

// Head is the first bytes of the WAL of member nodeID in cluster clusterID.
func Head(nodeID, clusterID uint64) []byte {
	table := crc32.MakeTable(crc32.Castagnoli)
	var out []byte
	record := func(typ int64, crc uint32, data []byte) {
		var r []byte
		r = protowire.AppendTag(r, 1, protowire.VarintType)
		r = protowire.AppendVarint(r, uint64(typ))
		r = protowire.AppendTag(r, 2, protowire.VarintType)
		r = protowire.AppendVarint(r, uint64(crc))
		if data != nil {
			r = protowire.AppendTag(r, 3, protowire.BytesType)
			r = protowire.AppendBytes(r, data)
		}
		word := uint64(len(r))
		if pad := (8 - len(r)%8) % 8; pad != 0 {
			word |= uint64(0x80|pad) << 56
			r = append(r, make([]byte, pad)...)
		}
		out = binary.LittleEndian.AppendUint64(out, word)
		out = append(out, r...)
	}
	record(4, 0, nil)
	var meta []byte
	meta = protowire.AppendTag(meta, 1, protowire.VarintType)
	meta = protowire.AppendVarint(meta, nodeID)
	meta = protowire.AppendTag(meta, 2, protowire.VarintType)
	meta = protowire.AppendVarint(meta, clusterID)
	record(1, crc32.Update(0, table, meta), meta)
	return append(out, make([]byte, 64)...)
}

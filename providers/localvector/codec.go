// Purpose: on-disk record encoding for FlatVectorStore's per-vector Store
//   entries, plus the two small Iterator-draining helpers Count and
//   Namespaces share. Split out of vector.go to satisfy Art.10.3's
//   300-line file cap (R-14.117 authorizes in-package splits of a file a
//   ticket owns).
// SPORT: providers.localvector.FlatVectorStore/ADDED (P1-E02-W1-S03-T4).

package localvector

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// encodeRecord serializes one vector's embedding and metadata into the
// bytes stored under (dataNamespacePrefix+namespace, id):
//
//	[4 bytes LE uint32: len(values)] [len(values)*4 bytes: values, LE float32 each] [remaining bytes: JSON(meta)]
//
// meta may be nil; json.Marshal(nil map) encodes as the 4-byte literal
// "null", which decodeRecord round-trips back to a nil map.
func encodeRecord(values []float32, meta map[string]any) ([]byte, error) {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4+len(values)*4+len(metaJSON))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(values))) //nolint:gosec // values is a validated small vector
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[4+i*4:8+i*4], math.Float32bits(v))
	}
	copy(buf[4+len(values)*4:], metaJSON)
	return buf, nil
}

// decodeRecord is encodeRecord's inverse. It reports cascade.KindIntegrity
// (never a panic — Art.1) on a record too short to hold its own declared
// length prefix, the one shape of corruption this format cannot detect any
// other way.
func decodeRecord(data []byte) (values []float32, meta map[string]any, err error) {
	if len(data) < 4 {
		return nil, nil, cascade.Newf(cascade.KindIntegrity, "localvector: corrupt vector record (want >= 4 bytes, got %d)", len(data))
	}
	n := int(binary.LittleEndian.Uint32(data[0:4]))
	if len(data) < 4+n*4 {
		return nil, nil, cascade.Newf(cascade.KindIntegrity, "localvector: corrupt vector record (declares %d values, only %d bytes present)", n, len(data))
	}
	values = make([]float32, n)
	off := 4
	for i := 0; i < n; i++ {
		values[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[off : off+4]))
		off += 4
	}
	if err := json.Unmarshal(data[off:], &meta); err != nil {
		return nil, nil, err
	}
	return values, meta, nil
}

// drainCount calls fn once per (id, record-bytes) pair Scan returns,
// closing it iterator regardless of how the loop ends.
func drainCount(ctx context.Context, it provider.Iterator, fn func(value []byte) error) error {
	for it.Next(ctx) {
		if err := fn(it.Value()); err != nil {
			_ = it.Close()
			return err
		}
	}
	if err := it.Err(); err != nil {
		_ = it.Close()
		return err
	}
	return it.Close()
}

// drainKeys is drainCount's key-oriented sibling, used by Namespaces.
func drainKeys(ctx context.Context, it provider.Iterator, fn func(key string) error) error {
	for it.Next(ctx) {
		if err := fn(it.Key()); err != nil {
			_ = it.Close()
			return err
		}
	}
	if err := it.Err(); err != nil {
		_ = it.Close()
		return err
	}
	return it.Close()
}

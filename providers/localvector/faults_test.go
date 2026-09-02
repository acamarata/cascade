// Purpose: coverage of FlatVectorStore's error-PROPAGATION paths — branches
//   that only run when the injected provider.Store itself fails (a
//   dependency-unavailable Put/Delete/Scan/Get, a corrupt on-disk record)
//   rather than when caller input is invalid (that's errors_test.go).
//   storetest.NewMemStore never fails, so these paths need a small
//   deliberately-failing provider.Store test double — exactly the kind
//   Article 1.1 permits in a _test.go file. See vector_test.go for
//   package-level constraints (Art.10.2 test-file exemption, Art.7.1).
// SPORT: providers.localvector.FlatVectorStore/ADDED (P1-E02-W1-S03-T4).

package localvector_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

// failStore wraps a real provider.Store (storetest.NewMemStore) and lets a
// test force any one operation to fail, so FlatVectorStore's own error-
// propagation branches (which storetest.NewMemStore's happy-path
// implementation never exercises) run under test.
type failStore struct {
	inner            provider.Store
	failPut          bool
	failDelete       bool
	failScan         bool
	failTxOuter      bool
	failGetInReadDim bool
}

func (f *failStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	return f.inner.Get(ctx, namespace, key)
}

func (f *failStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if f.failPut {
		return cascade.New(cascade.KindUnavailable, "injected Put failure")
	}
	return f.inner.Put(ctx, namespace, key, value)
}

func (f *failStore) Delete(ctx context.Context, namespace, key string) error {
	if f.failDelete {
		return cascade.New(cascade.KindUnavailable, "injected Delete failure")
	}
	return f.inner.Delete(ctx, namespace, key)
}

func (f *failStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	if f.failScan {
		return nil, cascade.New(cascade.KindUnavailable, "injected Scan failure")
	}
	return f.inner.Scan(ctx, namespace, prefix)
}

func (f *failStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	if f.failTxOuter {
		return cascade.New(cascade.KindUnavailable, "injected Tx failure")
	}
	return f.inner.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, &failTx{inner: tx, parent: f})
	})
}

// failTx wraps the real provider.Tx a Store.Tx call hands FlatVectorStore,
// so Upsert/Delete's writes made THROUGH the transaction can be failed
// too (Put/Delete outside a Tx are never called by FlatVectorStore, but
// readDimension runs against both Store and Tx via the shared getter
// interface).
type failTx struct {
	inner  provider.Tx
	parent *failStore
}

func (tx *failTx) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if tx.parent.failGetInReadDim && namespace == "localvector/index" {
		return nil, cascade.New(cascade.KindUnavailable, "injected Tx.Get failure")
	}
	return tx.inner.Get(ctx, namespace, key)
}

func (tx *failTx) Put(ctx context.Context, namespace, key string, value []byte) error {
	if tx.parent.failPut {
		return cascade.New(cascade.KindUnavailable, "injected Tx.Put failure")
	}
	return tx.inner.Put(ctx, namespace, key, value)
}

func (tx *failTx) Delete(ctx context.Context, namespace, key string) error {
	if tx.parent.failDelete {
		return cascade.New(cascade.KindUnavailable, "injected Tx.Delete failure")
	}
	return tx.inner.Delete(ctx, namespace, key)
}

func (tx *failTx) CompareAndSwap(ctx context.Context, namespace, key string, old, newValue []byte) error {
	return tx.inner.CompareAndSwap(ctx, namespace, key, old, newValue)
}

func TestUpsert_PropagatesTxOuterFailure(t *testing.T) {
	fs := &failStore{inner: storetest.NewMemStore(), failTxOuter: true}
	vs := localvector.New(fs)
	err := vs.Upsert(context.Background(), "ns", []provider.Vector{{ID: "a", Values: []float32{1}}})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Upsert with failing Tx = %v, want KindUnavailable", err)
	}
}

func TestUpsert_PropagatesReadDimensionFailure(t *testing.T) {
	fs := &failStore{inner: storetest.NewMemStore(), failGetInReadDim: true}
	vs := localvector.New(fs)
	err := vs.Upsert(context.Background(), "ns", []provider.Vector{{ID: "a", Values: []float32{1}}})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Upsert with failing dimension read = %v, want KindUnavailable", err)
	}
}

func TestUpsert_PropagatesIndexWriteFailure(t *testing.T) {
	fs := &failStore{inner: storetest.NewMemStore(), failPut: true}
	vs := localvector.New(fs)
	err := vs.Upsert(context.Background(), "ns", []provider.Vector{{ID: "a", Values: []float32{1}}})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Upsert with failing Put = %v, want KindUnavailable", err)
	}
}

func TestDelete_PropagatesTxFailure(t *testing.T) {
	inner := storetest.NewMemStore()
	fs := &failStore{inner: inner}
	vs := localvector.New(fs)
	if err := vs.Upsert(context.Background(), "ns", []provider.Vector{{ID: "a", Values: []float32{1}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	fs.failDelete = true
	err := vs.Delete(context.Background(), "ns", []string{"a"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Delete with failing Tx.Delete = %v, want KindUnavailable", err)
	}
}

func TestCount_PropagatesScanFailure(t *testing.T) {
	fs := &failStore{inner: storetest.NewMemStore(), failScan: true}
	vs := localvector.New(fs)
	_, err := vs.Count(context.Background(), "ns")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Count with failing Scan = %v, want KindUnavailable", err)
	}
}

func TestNamespaces_PropagatesScanFailure(t *testing.T) {
	fs := &failStore{inner: storetest.NewMemStore(), failScan: true}
	vs := localvector.New(fs)
	_, err := vs.Namespaces(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Namespaces with failing Scan = %v, want KindUnavailable", err)
	}
}

func TestQuery_PropagatesScanFailure(t *testing.T) {
	inner := storetest.NewMemStore()
	fs := &failStore{inner: inner}
	vs := localvector.New(fs)
	if err := vs.Upsert(context.Background(), "ns", []provider.Vector{{ID: "a", Values: []float32{1}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	fs.failScan = true
	_, err := vs.Query(context.Background(), "ns", provider.VectorQuery{Values: []float32{1}, TopK: 1})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Query with failing Scan = %v, want KindUnavailable", err)
	}
}

// TestQuery_CorruptRecordReportsIntegrity proves a truncated on-disk
// record surfaces as cascade.KindIntegrity, never a panic, when Query
// tries to decode it. The corrupt bytes are written directly through the
// underlying MemStore (bypassing FlatVectorStore's own Upsert encoding),
// simulating on-disk corruption a real backend could produce.
func TestQuery_CorruptRecordReportsIntegrity(t *testing.T) {
	ctx := context.Background()
	inner := storetest.NewMemStore()
	vs := localvector.New(inner)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Overwrite the record with 2 bytes — too short to hold even the
	// 4-byte length prefix decodeRecord requires.
	if err := inner.Put(ctx, "localvector/vectors/ns", "a", []byte{0x01, 0x02}); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}
	_, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: []float32{1, 0}, TopK: 1})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Query over corrupt record = %v, want KindIntegrity", err)
	}
}

// TestQuery_CorruptDimensionIndexReportsIntegrity does the same for the
// dimension index entry readDimension decodes.
func TestQuery_CorruptDimensionIndexReportsIntegrity(t *testing.T) {
	ctx := context.Background()
	inner := storetest.NewMemStore()
	vs := localvector.New(inner)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := inner.Put(ctx, "localvector/index", "ns", []byte{0x01, 0x02}); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}
	_, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: []float32{1, 0}, TopK: 1})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Query over corrupt dimension index = %v, want KindIntegrity", err)
	}
}

package supervision

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeRPCCaller is a minimal RPCCaller for testing wrapClientErr's two
// branches without a real Registry.
type fakeRPCCaller struct {
	err error
}

func (f fakeRPCCaller) Do(context.Context, string, any, any) error {
	return f.err
}

func TestClientListWrapsNonTaxonomyError(t *testing.T) {
	plain := context.DeadlineExceeded
	c := NewClient(fakeRPCCaller{err: plain})
	_, err := c.List(context.Background(), sessionScope("s1"), nil, false, nil, false)
	if err == nil {
		t.Fatal("List() err = nil, want the wrapped transport error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Errorf("List() err kind = %v (ok=%v), want KindInternal", kind, ok)
	}
}

func TestClientGetPreservesTaxonomyError(t *testing.T) {
	taxErr := ErrNotFound
	c := NewClient(fakeRPCCaller{err: taxErr})
	_, err := c.Get(context.Background(), "missing", sessionScope("s1"), nil)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("Get() err = %v, want the original KindNotFound preserved", err)
	}
}

func TestClientAckWrapsNonTaxonomyError(t *testing.T) {
	c := NewClient(fakeRPCCaller{err: context.Canceled})
	_, err := c.Ack(context.Background(), "x")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Errorf("Ack() err kind = %v (ok=%v), want KindInternal", kind, ok)
	}
}

func TestNewSystemIDGeneratorProducesDistinctIDs(t *testing.T) {
	gen := NewSystemIDGenerator()
	a, b := gen(), gen()
	if a == "" || b == "" {
		t.Fatal("NewSystemIDGenerator produced an empty ID")
	}
	if a == b {
		t.Errorf("two calls produced the same ID: %q", a)
	}
}

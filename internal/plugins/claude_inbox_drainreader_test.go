package plugins

// Purpose: the inbox drain's read-only session view fails closed on every
// write and when no drain transaction is active, and the taint requirement
// is a no-op for an empty drain.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/claude"
)

func TestDrainReaderFailsClosed(t *testing.T) {
	ctx := context.Background()
	r := drainReader{}
	calls := map[string]func() error{
		"Get":    func() error { _, err := r.Get(ctx, "ns", "k"); return err },
		"Put":    func() error { return r.Put(ctx, "ns", "k", []byte("v")) },
		"Delete": func() error { return r.Delete(ctx, "ns", "k") },
		"Scan":   func() error { it, err := r.Scan(ctx, "ns", ""); _ = it; return err },
		"Tx":     func() error { return r.Tx(ctx, func(context.Context, provider.Tx) error { return nil }) },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, errDrainReadOnly) {
			t.Errorf("%s with no drain = %v, want errDrainReadOnly", name, err)
		}
		if !cascade.HasKind(err, cascade.KindInternal) {
			t.Errorf("%s error = %v, want kind internal", name, err)
		}
	}
}

func TestRequireTaintIgnoresEmptyDrain(t *testing.T) {
	check := requireTaint(runtime.NewSystemClock(), "sess-1")
	if err := check(context.Background(), nil); err != nil {
		t.Fatalf("empty drain = %v, want nil without reading the session", err)
	}
	if err := check(context.Background(), []claude.InboxRecord{}); err != nil {
		t.Fatalf("zero records = %v, want nil", err)
	}
}

func TestTaintNeededMessageAndSentinel(t *testing.T) {
	var err error = &taintNeeded{ref: "rec-1"}
	var need *taintNeeded
	if !errors.As(err, &need) || need.ref != "rec-1" || err.Error() == "" {
		t.Fatalf("taintNeeded = %v / %+v", err, need)
	}
	if errTaintNotCommitted.Error() == errDrainReadOnly.Error() {
		t.Fatal("the two refusal sentinels must carry distinct messages")
	}
}

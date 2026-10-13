package cascadepa

// Purpose (this file): ThreadMapStore.Bind's error exits over the memState
//   fake with injected failures: a failing write of either row, a write that
//   reports a conflict although no row exists, a conflict whose read-back
//   fails, and a corrupt reverse row met by the pre-check.
//
// SPORT: plugins/cascade-pa ThreadMapStore/TEST.

import (
	"context"
	"testing"
)

// failSave is a BridgeState whose Save fails for one subject key.
type failSave struct {
	BridgeState
	key string
	err error
}

func (f *failSave) Save(ctx context.Context, st SubjectState) error {
	if st.Subject == f.key {
		return f.err
	}
	return f.BridgeState.Save(ctx, st)
}

func TestThreadMapStoreBindSurfacesWriteFailures(t *testing.T) {
	ctx := context.Background()
	const chat, thread = int64(71), "thread-71"
	for name, key := range map[string]string{"chat row": chatMapKey(mapSubject, chat), "thread row": threadMapKey(mapSubject, thread)} {
		s := NewThreadMapStore(&failSave{BridgeState: newMemState(), key: key, err: errStoreDown})
		if _, err := s.Bind(ctx, mapSubject, chat, thread); err != errStoreDown {
			t.Fatalf("Bind with the %s write failing = %v, want the store error", name, err)
		}
	}
}

func TestThreadMapStoreBindConflictWithNoRowIsCorrupt(t *testing.T) {
	ctx := context.Background()
	s := NewThreadMapStore(&failSave{BridgeState: newMemState(), key: chatMapKey(mapSubject, 72), err: ErrStateConflict})
	if _, err := s.Bind(ctx, mapSubject, 72, "thread-72"); err != ErrThreadMapCorrupt {
		t.Fatalf("Bind after a conflict that left no row = %v, want ErrThreadMapCorrupt", err)
	}
}

func TestThreadMapStoreBindConflictReadBackFailure(t *testing.T) {
	ctx := context.Background()
	mem := newMemState()
	s := NewThreadMapStore(&failSave{BridgeState: mem, key: chatMapKey(mapSubject, 73), err: ErrStateConflict})
	mem.loadErr = errStoreDown
	if _, err := s.Bind(ctx, mapSubject, 73, "thread-73"); err != errStoreDown {
		t.Fatalf("Bind whose conflict read-back fails = %v, want the store error", err)
	}
}

func TestThreadMapStoreBindRefusesCorruptReverseRow(t *testing.T) {
	ctx := context.Background()
	mem := newMemState()
	mem.seed(SubjectState{Subject: threadMapKey(mapSubject, "thread-74"), TrustTier: threadMapTier, CodeDigest: "+74"})
	if _, err := NewThreadMapStore(mem).Bind(ctx, mapSubject, 74, "thread-74"); err != ErrThreadMapCorrupt {
		t.Fatalf("Bind over a corrupt reverse row = %v, want ErrThreadMapCorrupt", err)
	}
}

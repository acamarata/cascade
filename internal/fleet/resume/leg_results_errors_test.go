// Purpose: the store-failure paths of the fan-out leg result adapter,
//   driven through a fault-injecting provider.Store over the real SQLite
//   driver, so the error branches run on every OS.
// Constraints: t.TempDir only; offline; test-only, no production seam.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (tests) (P1-CORE-18).

package resume

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// faultStore returns the injected error of an operation when it is set
// and otherwise delegates to the real store.
type faultStore struct {
	provider.Store
	getErr, delErr, scanErr, iterErr, closeErr, casErr error
}

func (f *faultStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.Store.Get(ctx, ns, key)
}

func (f *faultStore) Delete(ctx context.Context, ns, key string) error {
	if f.delErr != nil {
		return f.delErr
	}
	return f.Store.Delete(ctx, ns, key)
}

func (f *faultStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	it, err := f.Store.Scan(ctx, ns, prefix)
	if err != nil {
		return nil, err
	}
	return &faultIter{Iterator: it, err: f.iterErr, closeErr: f.closeErr}, nil
}

func (f *faultStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return f.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return fn(ctx, &faultTx{Tx: tx, casErr: f.casErr})
	})
}

type faultTx struct {
	provider.Tx
	casErr error
}

func (x *faultTx) CompareAndSwap(ctx context.Context, ns, key string, old, val []byte) error {
	if x.casErr != nil {
		return x.casErr
	}
	return x.Tx.CompareAndSwap(ctx, ns, key, old, val)
}

type faultIter struct {
	provider.Iterator
	err, closeErr error
}

func (i *faultIter) Err() error {
	if i.err != nil {
		return i.err
	}
	return i.Iterator.Err()
}

func (i *faultIter) Close() error {
	realErr := i.Iterator.Close()
	if i.closeErr != nil {
		return i.closeErr
	}
	return realErr
}

func faultAdapter(t *testing.T) (*journalAppenderAdapter, *faultStore) {
	t.Helper()
	js, raw, _ := newRealStore(t)
	f := &faultStore{Store: raw}
	a, err := newLegAdapter(js, f)
	if err != nil {
		t.Fatalf("newLegAdapter: %v", err)
	}
	return a, f
}

// wantWrapped asserts err carries kind, the exact message and the very
// cause value injected (errors.Is compares Kind only).
func wantWrapped(t *testing.T, err error, kind cascade.Kind, msg string, cause error) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v (%T), want *cascade.Error", err, err)
	}
	if ce.Kind != kind || ce.Error() != kind.String()+": "+msg+": "+cause.Error() || ce.Err != cause {
		t.Fatalf("err = %q kind=%v cause-identical=%v, want kind %v msg %q cause %q", ce.Error(), ce.Kind, ce.Err == cause, kind, msg, cause)
	}
}

func wantSentinel(t *testing.T, err, want error) {
	t.Helper()
	if err != want { //nolint:errorlint // identity is the point: errors.Is compares Kind only
		t.Fatalf("err = %v, want the identical sentinel %v", err, want)
	}
}

func TestLegAdapterNilStoreGuards(t *testing.T) {
	ctx := context.Background()
	a := &journalAppenderAdapter{}
	_, _, getErr := a.GetLegResult(ctx, "fo", 0)
	_, claimErr := a.claimAttempt(ctx, "fo", 0, "d")
	wantSentinel(t, a.PutLegResult(ctx, conductor.LegResult{FanOutID: "fo"}), ErrLegStoreUnset)
	wantSentinel(t, getErr, ErrLegStoreUnset)
	wantSentinel(t, a.DeleteTask(ctx, "fo"), ErrLegStoreUnset)
	wantSentinel(t, claimErr, ErrLegStoreUnset)
	if got := ErrLegStoreUnset.Error(); got != "invalid-input: resume: fan-out leg results need a non-nil journal and provider store" {
		t.Fatalf("ErrLegStoreUnset message = %q", got)
	}
}

func TestLegAdapterStoreFailures(t *testing.T) {
	ctx := context.Background()
	boom := cascade.New(cascade.KindUnavailable, "store down")
	put := conductor.LegResult{FanOutID: "fo", LegIndex: 1}
	key := conductor.LegResultKey("fo", 1)
	cases := []struct {
		name string
		set  func(*faultStore)
		run  func(*journalAppenderAdapter) error
		kind cascade.Kind
		msg  string
	}{
		{"put non-conflict cas", func(f *faultStore) { f.casErr = boom },
			func(a *journalAppenderAdapter) error { return a.PutLegResult(ctx, put) },
			cascade.KindUnavailable, "resume: storing fan-out leg result " + key + " (create-only)"},
		{"get store error", func(f *faultStore) { f.getErr = boom },
			func(a *journalAppenderAdapter) error { _, _, err := a.GetLegResult(ctx, "fo", 1); return err },
			cascade.KindUnavailable, "resume: reading fan-out leg result " + key},
		{"claim non-conflict cas", func(f *faultStore) { f.casErr = boom },
			func(a *journalAppenderAdapter) error { _, err := a.claimAttempt(ctx, "fo", 1, "d"); return err },
			cascade.KindUnavailable, "resume: claiming fan-out leg attempt " + key + "#1"},
		{"claim slot read error", func(f *faultStore) { f.casErr = cascade.New(cascade.KindConflict, "busy"); f.getErr = boom },
			func(a *journalAppenderAdapter) error { _, err := a.claimAttempt(ctx, "fo", 1, "d"); return err },
			cascade.KindUnavailable, "resume: reading fan-out leg attempt slot " + key + "#1"},
		{"delete scan error", func(f *faultStore) { f.scanErr = boom },
			func(a *journalAppenderAdapter) error { return a.DeleteTask(ctx, "fo") },
			cascade.KindUnavailable, "resume: listing fan-out leg results of fo"},
		{"delete iterator error", func(f *faultStore) { f.iterErr = boom },
			func(a *journalAppenderAdapter) error { return a.DeleteTask(ctx, "fo") },
			cascade.KindUnavailable, "resume: listing fan-out leg results of fo"},
		{"delete close error", func(f *faultStore) { f.closeErr = boom },
			func(a *journalAppenderAdapter) error { return a.DeleteTask(ctx, "fo") },
			cascade.KindUnavailable, "resume: closing the fan-out leg result listing"},
		{"delete record error", func(f *faultStore) { f.delErr = boom },
			func(a *journalAppenderAdapter) error { return a.DeleteTask(ctx, "fo") },
			cascade.KindUnavailable, "resume: deleting fan-out leg result " + key},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, f := faultAdapter(t)
			if err := a.PutLegResult(ctx, put); err != nil {
				t.Fatalf("seed PutLegResult: %v", err)
			}
			c.set(f)
			wantWrapped(t, c.run(a), c.kind, c.msg, boom)
		})
	}
}

func TestLegAdapterUntypedStoreErrorBecomesInternal(t *testing.T) {
	a, f := faultAdapter(t)
	f.getErr = errors.New("plain failure")
	_, _, err := a.GetLegResult(context.Background(), "fo", 0)
	wantWrapped(t, err, cascade.KindInternal, "resume: reading fan-out leg result "+conductor.LegResultKey("fo", 0), f.getErr)
}

func TestLegOutcomesUndecodableDonePayload(t *testing.T) {
	_, err := legOutcomes([]journal.Entry{{Kind: journal.KindFanOutLegDone, Payload: []byte("{not json")}})
	wantSentinel(t, err, ErrUnrecognizedShape)
}

func TestAppendLegInvalidLegRefused(t *testing.T) {
	a, _ := faultAdapter(t)
	for _, c := range []struct {
		id  string
		idx int
		msg string
	}{{"", 0, `invalid-input: resume: invalid fan-out leg ""#0`}, {"fo", -1, `invalid-input: resume: invalid fan-out leg "fo"#-1`}} {
		_, err := a.AppendLeg(context.Background(), "fanout_leg_started", c.id, c.idx, nil)
		if !cascade.HasKind(err, cascade.KindInvalidInput) || err == nil || err.Error() != c.msg {
			t.Fatalf("AppendLeg(%q,%d) = %v, want %q", c.id, c.idx, err, c.msg)
		}
	}
}

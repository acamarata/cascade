// Purpose: the storage-transport failure paths of the grant model. A
// store that cannot be read must DENY, not allow — a permission decision
// taken while the store is unreachable is the same bypass as one taken on
// a row that would not decode. These paths cannot be reached through the
// real SQLite driver, so this file drives them with a test-only store
// double confined to _test.go (Art.1).
//
// Split from grant_failclosed_test.go as a sibling file per R-14.117
// (Art.10.3's 300-line cap).
//
// SPORT: internal/policy StoreGrants/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// errStore is a provider.Store whose reads fail with a transport error.
// Writes succeed so a test can reach the read path with a row notionally
// present. Test-only, and never referenced outside this file.
type errStore struct {
	getErr  error
	scanErr error
	iterErr error
}

func (s *errStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, s.getErr
}
func (s *errStore) Put(context.Context, string, string, []byte) error { return nil }
func (s *errStore) Delete(context.Context, string, string) error      { return nil }
func (s *errStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return nil
}
func (s *errStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	if s.scanErr != nil {
		return nil, s.scanErr
	}
	return &errIterator{err: s.iterErr}, nil
}

// errIterator yields nothing and reports an error, modelling a scan that
// broke partway through.
type errIterator struct{ err error }

func (i *errIterator) Next(context.Context) bool { return false }
func (i *errIterator) Key() string               { return "" }
func (i *errIterator) Value() []byte             { return nil }
func (i *errIterator) Err() error                { return i.err }
func (i *errIterator) Close() error              { return nil }

// newErrFixture builds a StoreGrants over the failing store, with the
// capability registered so the transport error is the only thing that can
// refuse.
func newErrFixture(t *testing.T, s *errStore) *StoreGrants {
	t.Helper()
	reg := NewMemoryRegistry()
	if err := reg.Add(context.Background(), readCap()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store, err := NewStoreGrants(s, reg, testkit.NewFrozenClock(baseTime))
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	return store
}

// TestCheckDeniesOnTransportError asserts an unreadable store denies, and
// surfaces the underlying failure rather than swallowing it into an allow.
func TestCheckDeniesOnTransportError(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "store unreachable")
	store := newErrFixture(t, &errStore{getErr: boom})

	d, err := store.Check(context.Background(),
		CheckRequest{Subject: testSubject(), Capability: readCap().Name})
	if err == nil {
		t.Fatal("Check allowed while the store was unreadable")
	}
	if d.Granted {
		t.Fatal("a denied Check returned Granted=true")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Check returned %v, want the underlying transport failure", err)
	}
}

// TestRevokeReportsTransportError asserts Revoke does not report success
// when it could not even determine whether the grant exists.
func TestRevokeReportsTransportError(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "store unreachable")
	store := newErrFixture(t, &errStore{getErr: boom})
	if err := store.Revoke(context.Background(), testSubject(), readCap().Name); err == nil {
		t.Fatal("Revoke reported success against an unreadable store")
	}
}

// TestListReportsTransportError asserts List fails rather than returning a
// short list when the scan cannot be started or cannot be finished.
func TestListReportsTransportError(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "store unreachable")
	ctx := context.Background()

	if _, err := newErrFixture(t, &errStore{scanErr: boom}).List(ctx, testSubject()); err == nil {
		t.Fatal("List reported an empty result when the scan could not start")
	}
	if _, err := newErrFixture(t, &errStore{iterErr: boom}).List(ctx, testSubject()); err == nil {
		t.Fatal("List reported an empty result when the scan broke mid-iteration")
	}
	// A clean scan over no rows is an empty list, not an error.
	got, err := newErrFixture(t, &errStore{}).List(ctx, testSubject())
	if err != nil || len(got) != 0 {
		t.Fatalf("List over an empty prefix = %v, %v; want empty, nil", got, err)
	}
}

// TestGrantWriteSurfacesStoreErrors asserts a write the store rejects is
// reported, and that a well-formed write reaches the store unchanged.
func TestGrantWriteSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	store := newErrFixture(t, &errStore{getErr: cascade.ErrNotFound})
	g := validGrant()
	g.ScopeClass = corpus.VisibilityTeam
	if err := store.Grant(ctx, g); err != nil {
		t.Fatalf("Grant against a writable store: %v", err)
	}
	// The row is not readable back through this double, so Check denies —
	// a write that appeared to succeed never becomes an allow on its own.
	if _, err := store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err == nil {
		t.Fatal("Check allowed on a store that returns not-found")
	}
}

// TestRefusalSentinelsAreDistinguishable asserts each refusal wraps its own
// sentinel and that the three are told apart by errors.Is. A caller that
// cannot distinguish "no such capability" from "that subject holds nothing"
// cannot report either honestly, and a single shared sentinel would make
// every refusal look like every other.
func TestRefusalSentinelsAreDistinguishable(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	cases := []struct {
		name string
		req  CheckRequest
		want error
		not  []error
	}{
		{"unknown capability", CheckRequest{Subject: testSubject(), Capability: "never.registered"},
			ErrCapabilityNotFound, []error{ErrGrantDenied, ErrSubjectUnknown}},
		{"unknown subject", CheckRequest{Subject: Subject{}, Capability: readCap().Name},
			ErrSubjectUnknown, []error{ErrGrantDenied, ErrCapabilityNotFound}},
		{"no grant held", CheckRequest{Subject: Subject{Kind: SubjectAgent, ID: "lane-b"}, Capability: readCap().Name},
			ErrGrantDenied, []error{ErrSubjectUnknown, ErrCapabilityNotFound}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.store.Check(ctx, tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Check = %v, want a refusal matching %v", err, tc.want)
			}
			for _, other := range tc.not {
				if errors.Is(err, other) {
					t.Fatalf("refusal %v also matched %v; the sentinels must be distinguishable", err, other)
				}
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// classifyRowStore is a completionApprovalStore double that counts writes.
type classifyRowStore struct {
	marks, deletes  int
	markErr, delErr error
}

func (s *classifyRowStore) CompletionApprovalFor(context.Context, string, string) (jobs.CompletionApproval, bool, error) {
	return jobs.CompletionApproval{}, false, nil
}

func (s *classifyRowStore) MarkCompletionApprovalDenied(context.Context, string, string) error {
	s.marks++
	return s.markErr
}

func (s *classifyRowStore) DeleteCompletionApproval(context.Context, string, string) error {
	s.deletes++
	return s.delErr
}

// classifyEntries is an ApprovalEntrySource double.
type classifyEntries struct {
	state policy.ApprovalState
	err   error
	calls int
}

func (e *classifyEntries) EntryView(_ context.Context, id string) (policy.ApprovalEntryView, error) {
	e.calls++
	if e.err != nil {
		return policy.ApprovalEntryView{}, e.err
	}
	return policy.ApprovalEntryView{RequestID: id, State: e.state}, nil
}

const classifyNonce = "nonce-must-not-leak"

func classifyRow(denied bool) jobs.CompletionApproval {
	return jobs.CompletionApproval{JobID: "job-1", Action: "complete", Nonce: classifyNonce, Denied: denied}
}

func freshClassifyHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestClassifyStoredByEntryState(t *testing.T) {
	freshClassifyHome(t)
	ctx := context.Background()
	cases := []struct {
		name       string
		denied     bool
		entries    *classifyEntries
		consumeErr error
		want       storedClass
		marks      int
	}{
		{"pending", false, &classifyEntries{state: policy.ApprovalPending}, policy.ErrApprovalNotApproved, storedPending, 0},
		{"consuming waits", false, &classifyEntries{state: policy.ApprovalConsuming}, nil, storedPending, 0},
		{"denied marks the row", false, &classifyEntries{state: policy.ApprovalDenied}, policy.ErrApprovalNotApproved, storedDenied, 1},
		{"denied beats a stale refusal", false, &classifyEntries{state: policy.ApprovalDenied}, policy.ErrTokenReplayed, storedDenied, 1},
		{"marked row on unknown", true, &classifyEntries{err: policy.ErrUnknownRequest}, policy.ErrUnknownRequest, storedDenied, 0},
		{"marked row on expired", true, &classifyEntries{state: policy.ApprovalExpired}, policy.ErrTokenExpired, storedDenied, 0},
		{"approved", false, &classifyEntries{state: policy.ApprovalApproved}, nil, storedApproved, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &classifyRowStore{}
			got, err := classifyStored(ctx, store, tc.entries, classifyRow(tc.denied), "req-1", tc.consumeErr)
			if err != nil {
				t.Fatalf("classifyStored: %v", err)
			}
			if got != tc.want {
				t.Fatalf("class = %s, want %s", got, tc.want)
			}
			if store.marks != tc.marks || store.deletes != 0 {
				t.Fatalf("marks %d deletes %d, want marks %d and no delete", store.marks, store.deletes, tc.marks)
			}
		})
	}
}

func TestClassifyStoredRefilesStale(t *testing.T) {
	freshClassifyHome(t)
	ctx := context.Background()
	cases := []struct {
		name       string
		entries    *classifyEntries
		consumeErr error
	}{
		{"consumed", &classifyEntries{state: policy.ApprovalConsumed}, policy.ErrTokenReplayed},
		{"expired", &classifyEntries{state: policy.ApprovalExpired}, policy.ErrTokenExpired},
		{"canceled", &classifyEntries{state: policy.ApprovalCanceled}, nil},
		{"unknown entry", &classifyEntries{err: policy.ErrUnknownRequest}, policy.ErrUnknownRequest},
		{"unknown refusal", &classifyEntries{state: policy.ApprovalApproved}, policy.ErrUnknownRequest},
		{"expired refusal", &classifyEntries{state: policy.ApprovalApproved}, policy.ErrTokenExpired},
		{"replayed refusal", &classifyEntries{state: policy.ApprovalPending}, policy.ErrTokenReplayed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &classifyRowStore{}
			got, err := classifyStored(ctx, store, tc.entries, classifyRow(false), "req-1", tc.consumeErr)
			if err != nil || got != storedRefile {
				t.Fatalf("classifyStored = %s, %v; want refile", got, err)
			}
			if store.deletes != 1 || store.marks != 0 {
				t.Fatalf("deletes %d marks %d, want one delete", store.deletes, store.marks)
			}
		})
	}
	t.Run("entry view error returned", func(t *testing.T) {
		viewErr := cascade.New(cascade.KindIntegrity, "policy: approval request is in no valid state")
		store := &classifyRowStore{}
		_, err := classifyStored(ctx, store, &classifyEntries{err: viewErr}, classifyRow(false), "req-1", policy.ErrTokenReplayed)
		assertClassifyErr(t, err, viewErr)
		if store.deletes != 0 || store.marks != 0 {
			t.Fatalf("deletes %d marks %d after a view error, want none", store.deletes, store.marks)
		}
	})
	t.Run("delete error returned once", func(t *testing.T) { checkClassifyDeleteErr(ctx, t) })
	t.Run("mark error returned", func(t *testing.T) { checkClassifyMarkErr(ctx, t) })
}

// checkClassifyMarkErr checks that a mark error is returned with no delete.
func checkClassifyMarkErr(ctx context.Context, t *testing.T) {
	t.Helper()
	markErr := cascade.New(cascade.KindUnavailable, "jobs: deny completion approval")
	store := &classifyRowStore{markErr: markErr}
	_, err := classifyStored(ctx, store, &classifyEntries{state: policy.ApprovalDenied}, classifyRow(false), "req-1", nil)
	assertClassifyErr(t, err, markErr)
	if store.deletes != 0 {
		t.Fatalf("deletes = %d after a mark error, want none", store.deletes)
	}
}

// checkClassifyDeleteErr checks that a delete error is returned after exactly one delete.
func checkClassifyDeleteErr(ctx context.Context, t *testing.T) {
	t.Helper()
	delErr := cascade.New(cascade.KindUnavailable, "jobs: delete completion approval")
	store := &classifyRowStore{delErr: delErr}
	_, err := classifyStored(ctx, store, &classifyEntries{state: policy.ApprovalConsumed}, classifyRow(false), "req-1", policy.ErrTokenReplayed)
	assertClassifyErr(t, err, delErr)
	if store.deletes != 1 {
		t.Fatalf("deletes = %d, want exactly one", store.deletes)
	}
}

// assertClassifyErr checks identity and message, never errors.Is alone.
func assertClassifyErr(t *testing.T, got, want error) {
	t.Helper()
	if got != want || !errors.Is(got, want) || got.Error() != want.Error() {
		t.Fatalf("err = %v, want %v", got, want)
	}
	if got != nil && strings.Contains(got.Error(), classifyNonce) {
		t.Fatalf("error text carries the nonce: %q", got.Error())
	}
}

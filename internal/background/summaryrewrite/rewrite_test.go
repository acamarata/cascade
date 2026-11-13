// Package summaryrewrite provides utilities for rewriting session summaries.
package summaryrewrite

import (
	"context"
	"errors"
	"testing"
)

// fakeRewriter implements Rewriter for testing.
type fakeRewriter struct {
	local   bool
	rewrite func(ctx context.Context, summary string) (string, error)
	calls   int
}

func (f *fakeRewriter) Local() bool {
	return f.local
}

func (f *fakeRewriter) Rewrite(ctx context.Context, summary string) (string, error) {
	f.calls++
	if f.rewrite != nil {
		return f.rewrite(ctx, summary)
	}
	return "", nil
}

// fakeStore implements Store for testing.
type fakeStore struct {
	put        func(ctx context.Context, id string, summary string) error
	putSummary map[string]string
	calls      int
}

func (f *fakeStore) PutSummary(ctx context.Context, id string, summary string) error {
	f.calls++
	if f.putSummary == nil {
		f.putSummary = make(map[string]string)
	}
	f.putSummary[id] = summary
	if f.put != nil {
		return f.put(ctx, id, summary)
	}
	return nil
}

func TestRewriteReplacesAfterCompletion(t *testing.T) {
	// Test that local fake returns "new text"
	// Map Store is called once with session ID and holds "new text" after nil return
	// Hosted non-private fake is allowed

	t.Run("local model replaces summary", func(t *testing.T) {
		rewriter := &fakeRewriter{
			local: true,
			rewrite: func(_ context.Context, _ string) (string, error) {
				return "new text", nil
			},
		}
		store := &fakeStore{}

		session := Session{
			ID:      "test-session-1",
			Summary: "old text",
			Private: false,
		}

		err := RewriteSummary(context.Background(), session, rewriter, store)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if rewriter.calls != 1 {
			t.Errorf("expected rewriter to be called once, got %d calls", rewriter.calls)
		}

		if store.calls != 1 {
			t.Errorf("expected store to be called once, got %d calls", store.calls)
		}

		if got := store.putSummary[session.ID]; got != "new text" {
			t.Errorf("expected store to hold 'new text', got %q", got)
		}
	})
}

func TestRewriteHostedNonPrivateAllowed(t *testing.T) {
	t.Run("hosted non-private model is allowed", func(t *testing.T) {
		rewriter := &fakeRewriter{
			local: false,
			rewrite: func(_ context.Context, _ string) (string, error) {
				return "new text", nil
			},
		}
		store := &fakeStore{}

		session := Session{
			ID:      "test-session-2",
			Summary: "old text",
			Private: false,
		}

		err := RewriteSummary(context.Background(), session, rewriter, store)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if rewriter.calls != 1 {
			t.Errorf("expected rewriter to be called once, got %d calls", rewriter.calls)
		}

		if store.calls != 1 {
			t.Errorf("expected store to be called once, got %d calls", store.calls)
		}
	})
}

func TestRewritePrivateSessionUsesLocalOnly(t *testing.T) {
	// Private+Local=false returns errors.Is(err, ErrHostedPrivate) with zero Rewrite/Store calls
	// Private+Local=true succeeds

	t.Run("private session with hosted model fails", func(t *testing.T) {
		rewriter := &fakeRewriter{
			local: false,
		}
		store := &fakeStore{}

		session := Session{
			ID:      "test-session-3",
			Summary: "private text",
			Private: true,
		}

		err := RewriteSummary(context.Background(), session, rewriter, store)
		if !errors.Is(err, ErrHostedPrivate) {
			t.Fatalf("expected ErrHostedPrivate, got %v", err)
		}

		if rewriter.calls != 0 {
			t.Errorf("expected rewriter to not be called, got %d calls", rewriter.calls)
		}

		if store.calls != 0 {
			t.Errorf("expected store to not be called, got %d calls", store.calls)
		}
	})
}

func TestRewritePrivateSessionLocalSucceeds(t *testing.T) {
	t.Run("private session with local model succeeds", func(t *testing.T) {
		rewriter := &fakeRewriter{
			local: true,
			rewrite: func(_ context.Context, _ string) (string, error) {
				return "rewritten private text", nil
			},
		}
		store := &fakeStore{}

		session := Session{
			ID:      "test-session-4",
			Summary: "private text",
			Private: true,
		}

		err := RewriteSummary(context.Background(), session, rewriter, store)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		if rewriter.calls != 1 {
			t.Errorf("expected rewriter to be called once, got %d calls", rewriter.calls)
		}

		if store.calls != 1 {
			t.Errorf("expected store to be called once, got %d calls", store.calls)
		}
	})
}

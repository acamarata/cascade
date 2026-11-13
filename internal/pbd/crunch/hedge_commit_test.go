package crunch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestHedgeCommitsOnce(t *testing.T) {
	t.Run("only selected commits", testHedgeOnlySelectedCommits)
	t.Run("nil commit is no-op", testHedgeNilCommit)
	t.Run("commit error", testHedgeCommitError)
	t.Run("parent cancellation before commit", testHedgeParentCancel)
}

func testHedgeOnlySelectedCommits(t *testing.T) {
	var commitsA, commitsB atomic.Int32
	run := func(_ context.Context, _ HedgeTicket, model string) (HedgeResult, error) {
		if model == "modelA" {
			return HedgeResult{
				Green: true,
				Commit: func() error {
					commitsA.Add(1)
					return nil
				},
			}, nil
		}
		return HedgeResult{
			Green: true,
			Commit: func() error {
				commitsB.Add(1)
				return nil
			},
		}, nil
	}
	res, err := Hedge(context.Background(), HedgeTicket{ID: "t1"}, "modelA", "modelB", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Green {
		t.Fatalf("expected green result")
	}
	if res.Commit != nil {
		t.Fatalf("expected Commit to be cleared")
	}
	totalCommits := commitsA.Load() + commitsB.Load()
	if totalCommits != 1 {
		t.Fatalf("expected exactly 1 commit, got %d (A=%d, B=%d)", totalCommits, commitsA.Load(), commitsB.Load())
	}
}

func testHedgeNilCommit(t *testing.T) {
	run := func(_ context.Context, _ HedgeTicket, _ string) (HedgeResult, error) {
		return HedgeResult{
			Green:  true,
			Commit: nil,
		}, nil
	}
	res, err := Hedge(context.Background(), HedgeTicket{ID: "t2"}, "modelA", "modelB", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Green {
		t.Fatalf("expected green result")
	}
	if res.Commit != nil {
		t.Fatalf("expected Commit to be cleared")
	}
}

func testHedgeCommitError(t *testing.T) {
	cause := errors.New("commit failure")
	run := func(_ context.Context, _ HedgeTicket, _ string) (HedgeResult, error) {
		return HedgeResult{
			Green: true,
			Commit: func() error {
				return cause
			},
		}, nil
	}
	res, err := Hedge(context.Background(), HedgeTicket{ID: "t3"}, "modelA", "modelB", run)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrHedgeCommit) {
		t.Fatalf("expected ErrHedgeCommit, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("expected original cause, got %v", err)
	}
	if res.Green {
		t.Fatalf("expected zero result on commit error")
	}
}

func testHedgeParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	run := func(_ context.Context, _ HedgeTicket, _ string) (HedgeResult, error) {
		return HedgeResult{
			Green:  true,
			Commit: func() error { return nil },
		}, nil
	}
	_, err := Hedge(ctx, HedgeTicket{ID: "t_cancel"}, "modelA", "modelB", run)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestHedgeBothFail(t *testing.T) {
	errA := errors.New("failure A")
	errB := errors.New("failure B")

	t.Run("both error", func(t *testing.T) {
		run := func(_ context.Context, _ HedgeTicket, model string) (HedgeResult, error) {
			if model == "modelA" {
				return HedgeResult{}, errA
			}
			return HedgeResult{}, errB
		}
		_, err := Hedge(context.Background(), HedgeTicket{ID: "t4"}, "modelA", "modelB", run)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, errA) {
			t.Fatalf("expected errA in joined error, got %v", err)
		}
		if !errors.Is(err, errB) {
			t.Fatalf("expected errB in joined error, got %v", err)
		}
	})

	t.Run("one red one error", func(t *testing.T) {
		var commitCalled atomic.Bool
		run := func(_ context.Context, _ HedgeTicket, model string) (HedgeResult, error) {
			if model == "modelA" {
				return HedgeResult{Green: false, Commit: func() error {
					commitCalled.Store(true)
					return nil
				}}, nil
			}
			return HedgeResult{}, errB
		}
		_, err := Hedge(context.Background(), HedgeTicket{ID: "t5"}, "modelA", "modelB", run)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, ErrHedgeRed) {
			t.Fatalf("expected ErrHedgeRed, got %v", err)
		}
		if !errors.Is(err, errB) {
			t.Fatalf("expected errB in joined error, got %v", err)
		}
		if commitCalled.Load() {
			t.Fatalf("commit should not be called for red result")
		}
	})
}

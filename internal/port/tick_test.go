package port

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTickNeverOverlaps verifies that a second Run returns ErrConflict.
func TestTickNeverOverlaps(t *testing.T) {
	block := make(chan struct{})
	var calls int32

	tk := &Tick{
		WorkBound:     10,
		LeaseDuration: time.Second,
		AcquireLease: func(ctx context.Context, d time.Duration) error {
			return nil
		},
		Journal: func(ctx context.Context) ([]string, error) {
			return []string{"a"}, nil
		},
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		_ = tk.Run(ctx, func(ctx context.Context, items []string) error {
			atomic.AddInt32(&calls, 1)
			<-block
			return nil
		})
	}()

	time.Sleep(50 * time.Millisecond)

	err := tk.Run(ctx, func(ctx context.Context, items []string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	close(block)
	wg.Wait()

	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Fatalf("fn called %d times, want 1", c)
	}
}

// TestTickBoundsWork verifies Tick respects work bound per pass.
func TestTickBoundsWork(t *testing.T) {
	tk := &Tick{
		WorkBound:     2,
		LeaseDuration: time.Second,
		AcquireLease: func(ctx context.Context, d time.Duration) error {
			return nil
		},
		Journal: func(ctx context.Context) ([]string, error) {
			return []string{"a", "b", "c", "d", "e"}, nil
		},
	}

	var got []string
	err := tk.Run(context.Background(), func(ctx context.Context, items []string) error {
		got = items
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
}

// TestTickResumesFromJournal verifies items come from journal callback.
func TestTickResumesFromJournal(t *testing.T) {
	tk := &Tick{
		WorkBound:     10,
		LeaseDuration: time.Second,
		AcquireLease: func(ctx context.Context, d time.Duration) error {
			return nil
		},
		Journal: func(ctx context.Context) ([]string, error) {
			return []string{"x", "y"}, nil
		},
	}

	var got []string
	err := tk.Run(context.Background(), func(ctx context.Context, items []string) error {
		got = items
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("unexpected items: %v", got)
	}
}

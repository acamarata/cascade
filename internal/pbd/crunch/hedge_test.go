package crunch

import (
	"context"
	"errors"
	"testing"
)

func TestHedgeFastestGreenWins(t *testing.T) {
	t.Run("A wins green before B", func(t *testing.T) {
		releaseA := make(chan struct{})
		releaseB := make(chan struct{})

		run := func(ctx context.Context, tk HedgeTicket, model string) (HedgeResult, error) {
			switch model {
			case "modelA":
				<-releaseA
				return HedgeResult{Green: true, Commit: nil}, nil
			case "modelB":
				select {
				case <-releaseB:
					return HedgeResult{Green: true, Commit: nil}, nil
				case <-ctx.Done():
					return HedgeResult{}, ctx.Err()
				}
			default:
				return HedgeResult{}, errors.New("unexpected model")
			}
		}

		ctx := context.Background()
		tk := HedgeTicket{ID: "t1"}

		done := make(chan struct{})
		var res HedgeResult
		var err error
		go func() {
			res, err = Hedge(ctx, tk, "modelA", "modelB", run)
			close(done)
		}()

		// Release A first so it wins.
		close(releaseA)
		<-done

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !res.Green {
			t.Fatal("expected green result")
		}
		if res.Commit != nil {
			t.Fatal("expected nil commit for winning result")
		}

		// Release B so its goroutine can exit cleanly.
		close(releaseB)
	})

	validationCases := []struct {
		name   string
		ctx    context.Context
		ticket HedgeTicket
		modA   string
		modB   string
		run    HedgeRunFunc
	}{
		{"nil ctx", nil, HedgeTicket{ID: "t1"}, "a", "b", func(context.Context, HedgeTicket, string) (HedgeResult, error) { return HedgeResult{}, nil }},
		{"nil run", context.Background(), HedgeTicket{ID: "t1"}, "a", "b", nil},
		{"empty ID", context.Background(), HedgeTicket{ID: ""}, "a", "b", func(context.Context, HedgeTicket, string) (HedgeResult, error) { return HedgeResult{}, nil }},
		{"empty modelA", context.Background(), HedgeTicket{ID: "t1"}, "", "b", func(context.Context, HedgeTicket, string) (HedgeResult, error) { return HedgeResult{}, nil }},
		{"empty modelB", context.Background(), HedgeTicket{ID: "t1"}, "a", "", func(context.Context, HedgeTicket, string) (HedgeResult, error) { return HedgeResult{}, nil }},
		{"duplicate models", context.Background(), HedgeTicket{ID: "t1"}, "same", "same", func(context.Context, HedgeTicket, string) (HedgeResult, error) { return HedgeResult{}, nil }},
	}

	for _, tc := range validationCases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			wrappedRun := tc.run
			if wrappedRun != nil {
				wrappedRun = func(ctx context.Context, tk HedgeTicket, model string) (HedgeResult, error) {
					calls++
					return tc.run(ctx, tk, model)
				}
			}
			res, err := Hedge(tc.ctx, tc.ticket, tc.modA, tc.modB, wrappedRun)
			if !errors.Is(err, ErrHedgeInput) {
				t.Fatalf("expected ErrHedgeInput, got %v", err)
			}
			if res.Green || res.Commit != nil {
				t.Fatal("expected zero HedgeResult")
			}
			if calls != 0 {
				t.Fatalf("expected zero calls, got %d", calls)
			}
		})
	}

	t.Run("cancelled ctx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		run := func(ctx context.Context, tk HedgeTicket, model string) (HedgeResult, error) {
			calls++
			return HedgeResult{}, nil
		}
		res, err := Hedge(ctx, HedgeTicket{ID: "t1"}, "a", "b", run)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if res.Green || res.Commit != nil {
			t.Fatal("expected zero HedgeResult")
		}
		if calls != 0 {
			t.Fatalf("expected zero calls, got %d", calls)
		}
	})
}

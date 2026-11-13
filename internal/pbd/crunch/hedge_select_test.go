package crunch

import (
	"context"
	"errors"
	"testing"
)

// TestHedgeSkipsFasterRed verifies that when A completes red before B green, B wins.
func TestHedgeSkipsFasterRed(t *testing.T) {
	aDone := make(chan struct{})
	bRelease := make(chan struct{})

	run := func(ctx context.Context, ticket HedgeTicket, model string) (HedgeResult, error) {
		switch model {
		case "A":
			close(aDone)
			return HedgeResult{Green: false}, nil
		case "B":
			<-aDone
			<-bRelease
			return HedgeResult{Green: true}, nil
		}
		return HedgeResult{}, errors.New("unknown model")
	}

	ctx := context.Background()
	ticket := HedgeTicket{ID: "t1"}

	go func() {
		close(bRelease)
	}()

	res, err := Hedge(ctx, ticket, "A", "B", run)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.Green {
		t.Fatal("expected green result from B")
	}
}

// TestHedgeCancelsLoser verifies that after A wins, B observes cancellation and is joined.
func TestHedgeCancelsLoser(t *testing.T) {
	bCancelled := make(chan struct{})

	run := func(ctx context.Context, _ HedgeTicket, model string) (HedgeResult, error) {
		switch model {
		case "A":
			return HedgeResult{Green: true}, nil
		case "B":
			<-ctx.Done()
			close(bCancelled)
			return HedgeResult{}, ctx.Err()
		}
		return HedgeResult{}, errors.New("unknown model")
	}

	ctx := context.Background()
	ticket := HedgeTicket{ID: "t2"}

	res, err := Hedge(ctx, ticket, "A", "B", run)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.Green {
		t.Fatal("expected green result from A")
	}

	select {
	case <-bCancelled:
	default:
		t.Fatal("B was not cancelled after A won")
	}

	// Also verify parent cancellation joins both and returns ctx.Err().
	parentCtx, parentCancel := context.WithCancel(context.Background())
	parentCancel()

	_, err = Hedge(parentCtx, ticket, "A", "B", run)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

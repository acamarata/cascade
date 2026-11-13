// Package crunch provides hedged execution helpers.
package crunch

import (
	"context"
	"errors"
	"sync"
)

// ErrHedgeInput is returned when hedge inputs are invalid.
var ErrHedgeInput = errors.New("invalid hedge input")

// ErrHedgeRed is returned when a hedge result is red.
var ErrHedgeRed = errors.New("hedge result is red")

// ErrHedgeCommit is returned when a hedge commit fails.
var ErrHedgeCommit = errors.New("hedge commit failed")

// HedgeTicket identifies a single hedge attempt.
type HedgeTicket struct {
	// ID is the unique identifier for this ticket.
	ID string
}

// HedgeResult holds the outcome of a hedge run.
type HedgeResult struct {
	// Green indicates whether the result passed validation.
	Green bool
	// Commit persists the result; may be nil if no action is needed.
	Commit func() error
}

// HedgeRunFunc executes a model run and returns its result.
type HedgeRunFunc func(context.Context, HedgeTicket, string) (HedgeResult, error)

// Hedge runs two models concurrently and returns the first green result.
// It validates inputs before launching any work. A nil context or run function,
// empty ticket ID, empty model name, or identical model names all return
// ErrHedgeInput with no side effects. An already-cancelled context returns
// ctx.Err() immediately.
func Hedge(ctx context.Context, t HedgeTicket, modelA, modelB string, run HedgeRunFunc) (HedgeResult, error) {
	if ctx == nil || run == nil || t.ID == "" || modelA == "" || modelB == "" || modelA == modelB {
		return HedgeResult{}, ErrHedgeInput
	}
	if err := ctx.Err(); err != nil {
		return HedgeResult{}, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		res HedgeResult
		err error
	}

	ch := make(chan outcome, 2)

	go func() {
		res, err := run(ctx, t, modelA)
		ch <- outcome{res, err}
	}()
	go func() {
		res, err := run(ctx, t, modelB)
		ch <- outcome{res, err}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()

	for i := 0; i < 2; i++ {
		select {
		case o := <-ch:
			if o.err == nil && o.res.Green {
				cancel()
				wg.Wait()
				return o.res, nil
			}
		case <-ctx.Done():
			wg.Wait()
			return HedgeResult{}, ctx.Err()
		}
	}

	wg.Wait()
	return HedgeResult{}, ErrHedgeRed
}

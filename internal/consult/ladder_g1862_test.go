package consult

import (
	"context"
	"errors"
	"testing"
)

func TestLadderStopsAtPM(t *testing.T) {
	pmCalls := 0
	baCalls := 0
	ceoCalls := 0

	pm := func(_ context.Context, question string) (bool, string) {
		pmCalls++
		return true, "yes"
	}
	ba := func(_ context.Context, question string) (bool, string) {
		baCalls++
		return true, "ba"
	}
	ceo := func(_ context.Context, question string) (bool, string) {
		ceoCalls++
		return true, "ceo"
	}

	res, err := RunLadder(context.Background(), "q", []Rung{pm, ba, ceo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Rung != "PM" {
		t.Errorf("expected Rung PM, got %s", res.Rung)
	}
	if res.Answer != "yes" {
		t.Errorf("expected Answer 'yes', got %s", res.Answer)
	}
	if pmCalls != 1 {
		t.Errorf("expected PM calls 1, got %d", pmCalls)
	}
	if baCalls != 0 {
		t.Errorf("expected BA calls 0, got %d", baCalls)
	}
	if ceoCalls != 0 {
		t.Errorf("expected CEO calls 0, got %d", ceoCalls)
	}
}

func TestLadderStopsAtBA(t *testing.T) {
	pmCalls := 0
	baCalls := 0
	ceoCalls := 0

	pm := func(ctx context.Context, question string) (bool, string) {
		pmCalls++
		return false, ""
	}
	ba := func(ctx context.Context, question string) (bool, string) {
		baCalls++
		return true, "ba answer"
	}
	ceo := func(ctx context.Context, question string) (bool, string) {
		ceoCalls++
		return true, "ceo"
	}

	res, err := RunLadder(context.Background(), "q", []Rung{pm, ba, ceo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Rung != "BA" {
		t.Errorf("expected Rung BA, got %s", res.Rung)
	}
	if res.Answer != "ba answer" {
		t.Errorf("expected Answer 'ba answer', got %s", res.Answer)
	}
	if pmCalls != 1 {
		t.Errorf("expected PM calls 1, got %d", pmCalls)
	}
	if baCalls != 1 {
		t.Errorf("expected BA calls 1, got %d", baCalls)
	}
	if ceoCalls != 0 {
		t.Errorf("expected CEO calls 0, got %d", ceoCalls)
	}
}

func TestLadderReachesCEO(t *testing.T) {
	var order []string
	pm := func(ctx context.Context, question string) (bool, string) {
		order = append(order, "PM")
		return false, ""
	}
	ba := func(ctx context.Context, question string) (bool, string) {
		order = append(order, "BA")
		return false, ""
	}
	ceo := func(ctx context.Context, question string) (bool, string) {
		order = append(order, "CEO")
		return true, "ceo answer"
	}

	res, err := RunLadder(context.Background(), "q", []Rung{pm, ba, ceo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Rung != "CEO" {
		t.Errorf("expected Rung CEO, got %s", res.Rung)
	}
	if res.Answer != "ceo answer" {
		t.Errorf("expected Answer 'ceo answer', got %s", res.Answer)
	}
	if len(order) != 3 || order[0] != "PM" || order[1] != "BA" || order[2] != "CEO" {
		t.Errorf("expected call order PM, BA, CEO, got %v", order)
	}
}

func TestLadderUnresolvedFailsClosed(t *testing.T) {
	pm := func(ctx context.Context, question string) (bool, string) { return false, "" }
	ba := func(ctx context.Context, question string) (bool, string) { return false, "" }
	ceo := func(ctx context.Context, question string) (bool, string) { return false, "" }

	_, err := RunLadder(context.Background(), "q", []Rung{pm, ba, ceo})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

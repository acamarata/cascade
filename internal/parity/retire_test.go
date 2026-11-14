package parity

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestRetirePlanRefusesRed(t *testing.T) {
	steps, err := RetirePlan(Verdict{Green: false})
	if !errors.Is(err, ErrGateRed) {
		t.Fatalf("expected ErrGateRed, got %v", err)
	}
	if steps != nil {
		t.Fatalf("expected nil steps, got %v", steps)
	}
}

func TestRetirePlanStepOrder(t *testing.T) {
	steps, err := RetirePlan(Verdict{Green: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(steps))
	}
	expected := []Step{
		{Order: 1, Action: "freeze capp writes"},
		{Order: 2, Action: "verify the data export"},
		{Order: 3, Action: "remove the capp shim references"},
		{Order: 4, Action: "archive the capp tree"},
	}
	if !reflect.DeepEqual(steps, expected) {
		t.Fatalf("expected %v, got %v", expected, steps)
	}
}

func TestRetirePlanPure(t *testing.T) {
	steps1, err1 := RetirePlan(Verdict{Green: true})
	if err1 != nil {
		t.Fatalf("unexpected error: %v", err1)
	}
	steps2, err2 := RetirePlan(Verdict{Green: true})
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if !reflect.DeepEqual(steps1, steps2) {
		t.Fatalf("slices not equal: %v vs %v", steps1, steps2)
	}

	tmp := t.TempDir()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir not empty: %d files", len(entries))
	}
}

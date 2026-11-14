package statesave_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/background/statesave"
)

func TestStateSaveSuccessContinues(t *testing.T) {
	called := 0
	save := func() error {
		called++
		return nil
	}

	state, question := statesave.Save(save)

	if called != 1 {
		t.Fatalf("save callback called %d times, expected 1", called)
	}
	if state != statesave.Running {
		t.Fatalf("expected state Running, got %v", state)
	}
	if question != nil {
		t.Fatalf("expected no question on success, got %+v", question)
	}
}

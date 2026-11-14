package background

import (
	"errors"
	"testing"
)

func TestAutoguardSilencesRaiseCard(t *testing.T) {
	called := false
	suppressed, err := Guard(ModeAutonomous, func() error {
		called = true
		return errors.New("raiseCard should not be executed")
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !suppressed {
		t.Fatalf("expected suppressed=true, got false")
	}
	if called {
		t.Fatalf("raiseCard was executed in autonomous mode")
	}
}

func TestAutoguardSilencesRequestNoAsk(t *testing.T) {
	called := false
	suppressed, err := Guard(ModeAutonomous, func() error {
		called = true
		return errors.New("Request.NoAsk should not be executed")
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !suppressed {
		t.Fatalf("expected suppressed=true, got false")
	}
	if called {
		t.Fatalf("Request.NoAsk was executed in autonomous mode")
	}
}

func TestAutoguardAllowsInteractiveMode(t *testing.T) {
	called := false
	suppressed, err := Guard(ModeInteractive, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if suppressed {
		t.Fatalf("expected suppressed=false, got true")
	}
	if !called {
		t.Fatalf("expected call to be executed in interactive mode")
	}
}

func TestAutoguardRejectsUnknownMode(t *testing.T) {
	called := false
	suppressed, err := Guard(Mode(""), func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrM1889) {
		t.Fatalf("expected ErrM1889, got %v", err)
	}
	if suppressed {
		t.Fatalf("expected suppressed=false")
	}
	if called {
		t.Fatalf("call was executed with unknown mode")
	}
}

package crunch

import (
	"errors"
	"testing"
)

func TestPurifyFailsWithoutRedact(t *testing.T) {
	// nil redact
	out, err := PurifyCW("transcript", nil, func(s string) (string, error) {
		t.Fatal("jev should not run when redact is nil")
		return s, nil
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for nil redact, got %q %v", out, err)
	}

	// redact error
	stageErr := errors.New("stage failure")
	jevRun := false
	out, err = PurifyCW("transcript", func(s string) (string, error) {
		return "", stageErr
	}, func(s string) (string, error) {
		jevRun = true
		return s, nil
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for redact error, got %q %v", out, err)
	}
	if err.Error() == stageErr.Error() || err.Error() == "stage failure" {
		t.Fatalf("raw stage error text exposed: %v", err)
	}
	if jevRun {
		t.Fatal("jev should not run when redact errors")
	}

	// blank redact output
	jevRun = false
	out, err = PurifyCW("transcript", func(s string) (string, error) {
		return "   ", nil
	}, func(s string) (string, error) {
		jevRun = true
		return s, nil
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for blank redact output, got %q %v", out, err)
	}
	if jevRun {
		t.Fatal("jev should not run when redact output is blank")
	}
}

func TestPurifyFailsWithoutJev(t *testing.T) {
	// nil jev
	redactRun := false
	out, err := PurifyCW("transcript", func(s string) (string, error) {
		redactRun = true
		return s, nil
	}, nil)
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for nil jev, got %q %v", out, err)
	}
	if redactRun {
		t.Fatal("redact should not run when jev is nil")
	}

	// jev error
	stageErr := errors.New("stage failure")
	out, err = PurifyCW("transcript", func(s string) (string, error) {
		return "redacted", nil
	}, func(s string) (string, error) {
		return "", stageErr
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for jev error, got %q %v", out, err)
	}
	if err.Error() == stageErr.Error() || err.Error() == "stage failure" {
		t.Fatalf("raw stage error text exposed: %v", err)
	}

	// blank jev output
	out, err = PurifyCW("transcript", func(s string) (string, error) {
		return "redacted", nil
	}, func(s string) (string, error) {
		return "   ", nil
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for blank jev output, got %q %v", out, err)
	}
}

func TestPurifyRejectsEmptyTranscript(t *testing.T) {
	// empty transcript
	redactRun := false
	jevRun := false
	out, err := PurifyCW("", func(s string) (string, error) {
		redactRun = true
		return s, nil
	}, func(s string) (string, error) {
		jevRun = true
		return s, nil
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for empty transcript, got %q %v", out, err)
	}
	if redactRun || jevRun {
		t.Fatal("stages should not run for empty transcript")
	}

	// whitespace-only transcript
	redactRun = false
	jevRun = false
	out, err = PurifyCW("   \n\t  ", func(s string) (string, error) {
		redactRun = true
		return s, nil
	}, func(s string) (string, error) {
		jevRun = true
		return s, nil
	})
	if out != "" || !errors.Is(err, ErrT0411) {
		t.Fatalf("expected empty and ErrT0411 for whitespace-only transcript, got %q %v", out, err)
	}
	if redactRun || jevRun {
		t.Fatal("stages should not run for whitespace-only transcript")
	}
}

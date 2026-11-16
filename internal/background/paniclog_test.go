package background

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger to a buffer while f runs and
// returns everything that was logged.
func captureLog(t *testing.T, f func()) string {
	t.Helper()

	var buf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()

	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	}()

	f()
	return buf.String()
}

func TestRecoverLogsPanic(t *testing.T) {
	out := captureLog(t, func() {
		Recover(func() any {
			panic("boom")
		})
	})

	if !strings.Contains(out, "boom") {
		t.Fatalf("expected log output to contain %q, got %q", "boom", out)
	}
}

func TestNoPanicRunsNormally(t *testing.T) {
	got := Recover(func() any {
		return 42
	})

	if got != 42 {
		t.Fatalf("expected 42, got %#v", got)
	}
}

func TestRecoverReturnsAfterPanic(t *testing.T) {
	got := Recover(func() any {
		panic("failure")
	})

	if got != nil {
		t.Fatalf("expected nil after panic, got %#v", got)
	}
}

func TestPanicValueIncludedInLog(t *testing.T) {
	out := captureLog(t, func() {
		Recover(func() any {
			panic(500)
		})
	})

	if !strings.Contains(out, "500") {
		t.Fatalf("expected log output to contain %q, got %q", "500", out)
	}
}

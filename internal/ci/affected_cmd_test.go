package ci

import (
	"context"
	"errors"
	"testing"
)

// TestAffectedTargets_AffectedCmdPresent proves a non-Go stack with a
// configured affected_cmd returns its stdout lines as Targets, via a
// real shell subprocess reading the changed-path list off stdin.
func TestAffectedTargets_AffectedCmdPresent(t *testing.T) {
	// sed rather than a sh loop: the command runs under cmd.exe on Windows, where
	// the CI runner provides sed alongside cat (TestAffectedCmd_StdinProtocol).
	cfg := Config{AffectedCmd: `sed "s/^/target:/"`}
	targets, err := affectedTargets(context.Background(), t.TempDir(), "rust", cfg, []string{"src/lib.rs", "src/main.rs"})
	if err != nil {
		t.Fatalf("affectedTargets: %v", err)
	}
	want := []Target{"target:src/lib.rs", "target:src/main.rs"}
	if len(targets) != len(want) || targets[0] != want[0] || targets[1] != want[1] {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
}

// TestAffectedTargets_AffectedCmdAbsentReturnsFull proves a non-Go stack
// with NO affected_cmd configured returns the TargetAll fallback.
func TestAffectedTargets_AffectedCmdAbsentReturnsFull(t *testing.T) {
	targets, err := affectedTargets(context.Background(), t.TempDir(), "rust", Config{}, []string{"src/lib.rs"})
	if err != nil {
		t.Fatalf("affectedTargets: %v", err)
	}
	if len(targets) != 1 || targets[0] != TargetAll {
		t.Fatalf("targets = %v, want [%s]", targets, TargetAll)
	}
}

// TestAffectedTargets_AffectedCmdNonZeroExit proves a non-zero exit is
// ErrAffectedCmdFailed, never a panic.
func TestAffectedTargets_AffectedCmdNonZeroExit(t *testing.T) {
	cfg := Config{AffectedCmd: "exit 3"}
	_, err := affectedTargets(context.Background(), t.TempDir(), "rust", cfg, []string{"src/lib.rs"})
	if !errors.Is(err, ErrAffectedCmdFailed) {
		t.Fatalf("error = %v, want ErrAffectedCmdFailed", err)
	}
}

// TestAffectedCmd_StdinProtocol proves the changed-path list is written
// to the command's stdin one path per line, in order.
func TestAffectedCmd_StdinProtocol(t *testing.T) {
	targets, err := affectedCmdTargets(context.Background(), t.TempDir(), "cat", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("affectedCmdTargets: %v", err)
	}
	want := []Target{"a", "b", "c"}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets[%d] = %q, want %q", i, targets[i], want[i])
		}
	}
}

// TestAffectedCmd_EmptyStdoutReturnsEmptyTargets proves a command that
// prints nothing yields []Target{}, not nil.
func TestAffectedCmd_EmptyStdoutReturnsEmptyTargets(t *testing.T) {
	targets, err := affectedCmdTargets(context.Background(), t.TempDir(), "true", []string{"a"})
	if err != nil {
		t.Fatalf("affectedCmdTargets: %v", err)
	}
	if targets == nil || len(targets) != 0 {
		t.Fatalf("targets = %v, want empty non-nil", targets)
	}
}

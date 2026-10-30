//go:build darwin

// Purpose: darwin clipboard backend tests over injected lookPath/runCmd: a
//
//	missing pbcopy is KindUnsupported at probe, a failing pbcopy is
//	KindUnavailable, the clear runs both steps, and the payload rides stdin
//	only. No test runs the real pbcopy or touches the real clipboard; the
//	real-tool leg is the ticket's QA on a source-built binary.
//
// SPORT: internal/secrets clipboard_darwin.go/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func darwinOps(look error, run func(stdin []byte) error) *darwinClipboardOps {
	return &darwinClipboardOps{bin: pbcopyBin,
		lookPath: func(string) (string, error) { return pbcopyBin, look },
		runCmd:   func(_ context.Context, _ string, stdin []byte) error { return run(stdin) }}
}

func TestClipboard_Darwin_NewOps(t *testing.T) {
	ops, err := newClipboardOps()
	do, ok := ops.(*darwinClipboardOps)
	if err != nil || !ok || do.bin != pbcopyBin || ops.platform() != "darwin" || do.lookPath == nil || do.runCmd == nil {
		t.Fatalf("newClipboardOps = %#v, %v", ops, err)
	}
}

func TestClipboard_Darwin_PbcopyAbsentIsUnsupported(t *testing.T) {
	ran := false
	ops := darwinOps(exec.ErrNotFound, func([]byte) error { ran = true; return nil })
	if err := ops.probe(); !cascade.HasKind(err, cascade.KindUnsupported) || ran {
		t.Fatalf("probe = %v (ran %v), want KindUnsupported without running pbcopy", err, ran)
	}
}

func TestClipboard_Darwin_WriteFailureIsUnavailable(t *testing.T) {
	ops := darwinOps(nil, func([]byte) error { return errors.New("exit status 1") })
	if err := ops.setValue(context.Background(), []byte("v")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("setValue = %v, want KindUnavailable", err)
	}
}

func TestClipboard_Darwin_ClearFailureBothSteps(t *testing.T) {
	var calls [][]byte
	ops := darwinOps(nil, func(stdin []byte) error {
		calls = append(calls, append([]byte(nil), stdin...))
		if len(calls) == 1 {
			return errors.New("exit status 1")
		}
		return nil
	})
	if err := ops.clearValue(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("clearValue = %v, want KindUnavailable", err)
	}
	if len(calls) != 2 || string(calls[0]) != " " || len(calls[1]) != 0 {
		t.Fatalf("clear steps = %q, want a space then an empty write", calls)
	}
}

func TestClipboard_Darwin_ArgsNeverCarryPayload(t *testing.T) {
	var got []byte
	ops := darwinOps(nil, func(stdin []byte) error { got = stdin; return nil })
	if err := ops.setValue(context.Background(), []byte("payload-xyz")); err != nil || string(got) != "payload-xyz" {
		t.Fatalf("setValue = %v, stdin = %q", err, got)
	}
	if cmd := exec.CommandContext(context.Background(), pbcopyBin); len(cmd.Args) != 1 {
		t.Fatalf("pbcopy invocation carries arguments: %v", cmd.Args)
	}
}

// TestClipboard_Darwin_ProbeAndClearSucceed: a present pbcopy probes clean
// and a clear whose two steps succeed reports nil; a failing second step is
// still KindUnavailable.
func TestClipboard_Darwin_ProbeAndClearSucceed(t *testing.T) {
	ops := darwinOps(nil, func([]byte) error { return nil })
	if err := ops.probe(); err != nil {
		t.Fatalf("probe = %v", err)
	}
	if err := ops.clearValue(context.Background()); err != nil {
		t.Fatalf("clearValue = %v", err)
	}
	n := 0
	ops = darwinOps(nil, func([]byte) error {
		n++
		if n == 2 {
			return errors.New("exit status 1")
		}
		return nil
	})
	if err := ops.clearValue(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("clearValue with a failing second step = %v", err)
	}
}

// TestClipboard_Darwin_RunPipedCommandFeedsStdin: the runner hands its input
// on stdin and reports a missing binary; /bin/cat stands in for pbcopy so
// the real clipboard is never touched.
func TestClipboard_Darwin_RunPipedCommandFeedsStdin(t *testing.T) {
	if err := runPipedCommand(context.Background(), "/bin/cat", []byte("x")); err != nil {
		t.Fatalf("runPipedCommand(/bin/cat) = %v", err)
	}
	if err := runPipedCommand(context.Background(), "/nonexistent/pbcopy", nil); err == nil {
		t.Fatal("a missing binary ran")
	}
}

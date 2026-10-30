//go:build linux

// Purpose: linux clipboard backend tests over injected lookPath/runCmd: a
//
//	missing xclip is KindUnsupported at probe, a failing xclip is
//	KindUnavailable, the clear runs both steps, and the payload rides stdin
//	only. No test runs the real xclip or touches the real clipboard.
//
// SPORT: internal/secrets clipboard_linux.go/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func linuxOps(look error, run func(stdin []byte) error) *linuxClipboardOps {
	return &linuxClipboardOps{bin: xclipBin,
		lookPath: func(string) (string, error) { return "/usr/bin/xclip", look },
		runCmd:   func(_ context.Context, _ string, stdin []byte) error { return run(stdin) }}
}

func TestClipboard_Linux_NewOps(t *testing.T) {
	ops, err := newClipboardOps()
	lo, ok := ops.(*linuxClipboardOps)
	if err != nil || !ok || lo.bin != xclipBin || ops.platform() != "linux" || lo.lookPath == nil || lo.runCmd == nil {
		t.Fatalf("newClipboardOps = %#v, %v", ops, err)
	}
}

func TestClipboard_Linux_XclipAbsentIsUnsupported(t *testing.T) {
	ran := false
	ops := linuxOps(exec.ErrNotFound, func([]byte) error { ran = true; return nil })
	if err := ops.probe(); !cascade.HasKind(err, cascade.KindUnsupported) || ran {
		t.Fatalf("probe = %v (ran %v), want KindUnsupported without running xclip", err, ran)
	}
}

func TestClipboard_Linux_NonZeroExitIsUnavailable(t *testing.T) {
	ops := linuxOps(nil, func([]byte) error { return errors.New("exit status 1") })
	if err := ops.probe(); err != nil {
		t.Fatalf("probe with xclip present = %v", err)
	}
	if err := ops.setValue(context.Background(), []byte("v")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("setValue = %v, want KindUnavailable", err)
	}
}

func TestClipboard_Linux_ClearRunsBothSteps(t *testing.T) {
	var calls [][]byte
	ops := linuxOps(nil, func(stdin []byte) error {
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

func TestClipboard_Linux_PayloadOnStdinOnly(t *testing.T) {
	var got []byte
	ops := linuxOps(nil, func(stdin []byte) error { got = stdin; return nil })
	if err := ops.setValue(context.Background(), []byte("payload-xyz")); err != nil || string(got) != "payload-xyz" {
		t.Fatalf("setValue = %v, stdin = %q", err, got)
	}
	cmd := exec.CommandContext(context.Background(), xclipBin, "-selection", "clipboard", "-i")
	for _, a := range cmd.Args {
		if a == "payload-xyz" {
			t.Fatal("payload in argv")
		}
	}
}

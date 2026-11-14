//go:build windows

// Purpose: proves on the Windows lane that `cascade run --agent` refuses with
// KindUnsupported: the daemon it delegates to does not exist on this platform.
// Constraints: runs the registered command through the real root command tree.
// SPORT: cmd/cascade/run (TEST).
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRunAgentWindowsUnsupported(t *testing.T) {
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"run", "--agent", "list files", "--timeout", "1s"})
	err := root.ExecuteContext(context.Background())
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("run --agent on windows: err = %v, want KindUnsupported", err)
	}
}

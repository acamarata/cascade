// Purpose: unit coverage for productionSoulEditor, the real soulEditorFunc
//
//	that shipped with no direct test of its own (only the injectable seam
//	is exercised elsewhere, per this file's own doc comment on why the
//	seam exists). Drives a real os/exec.CommandContext against `true` and
//	a deliberately missing binary -- no network, so the unit lane's
//	Art.7.2 gate is unaffected.
//
// SPORT: cmd.cascade.memory-soul/TEST (memory soul edit ceremony).
package main

import (
	"context"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func TestProductionSoulEditor_SuccessfulEditorRun(t *testing.T) {
	path := "/dev/null"
	cmd := &cobra.Command{}
	cmd.SetIn(os.Stdin)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	if err := productionSoulEditor(context.Background(), cmd, "true", path); err != nil {
		t.Fatalf("productionSoulEditor(true): %v", err)
	}
}

func TestProductionSoulEditor_MissingEditorRefuses(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(os.Stdin)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	err := productionSoulEditor(context.Background(), cmd, "cascade-no-such-editor-binary", "/dev/null")
	if err == nil {
		t.Fatal("productionSoulEditor with a nonexistent editor = nil error, want a run failure")
	}
}

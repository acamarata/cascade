package inventory

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestErrorKindCount(t *testing.T) {
	// The frozen R-14.3 taxonomy is exactly 14 members; this test asserts
	// the derivation function agrees with the number every doc comment
	// this ticket touched also states, so a future taxonomy amendment
	// that forgets this test fails loudly.
	if got := ErrorKindCount(); got != 14 {
		t.Fatalf("ErrorKindCount() = %d, want 14", got)
	}
}

func TestStorageDomainCount(t *testing.T) {
	if got := StorageDomainCount(); got != 11 {
		t.Fatalf("StorageDomainCount() = %d, want 11", got)
	}
}

func TestCountCLICommands_Nil(t *testing.T) {
	if got := CountCLICommands(nil); got != 0 {
		t.Fatalf("CountCLICommands(nil) = %d, want 0", got)
	}
}

func TestCountCLICommands_RootOnly(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	if got := CountCLICommands(root); got != 1 {
		t.Fatalf("CountCLICommands(root-only) = %d, want 1", got)
	}
}

func TestCountCLICommands_NestedTree(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	child := &cobra.Command{Use: "child"}
	grandchild := &cobra.Command{Use: "grandchild"}
	child.AddCommand(grandchild)
	root.AddCommand(child)
	sibling := &cobra.Command{Use: "sibling"}
	root.AddCommand(sibling)

	// root, child, grandchild, sibling = 4.
	if got := CountCLICommands(root); got != 4 {
		t.Fatalf("CountCLICommands(nested) = %d, want 4", got)
	}
}

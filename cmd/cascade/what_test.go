// Purpose: unit tests for the hidden `cascade what <query>` alias —
// reachability on the real root command, Hidden==true, flag-set parity
// with `cascade recall what`, and a static proof that the alias
// delegates to newRecallWhatCmd rather than a second implementation.
// Mirrors fleet.go/fleet_journal.go's own hidden-alias test pattern
// (fleet_test.go's TestFleetSessionsAliasHidden, fleet_journal_test.go's
// TestFleetJournalAlias_IdenticalConstruction). The byte-for-byte
// output/exit-code parity proof those two precedents have no positional
// query to need lives in what_parity_test.go, split out under the
// 300-line file cap.
//
// This file deliberately imports neither "net" nor "net/http" so it runs
// in the fast, no-network unit lane (Art.7.2).
//
// SPORT: cmd.cascade.cmd.what (ADD, P1-E22-W5-S47-T5).
package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestWhatAliasMountedOnRoot proves both `recall what` and the hidden
// top-level `what` resolve on the real root command tree — the
// reachability proof TestRecallWhatResolvesOnTheRealRootCommand already
// gives the primary surface, extended to the alias so a dropped
// mountWhatCmd(root) line in root.go fails this, not just goes unnoticed.
func TestWhatAliasMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{{"recall", "what"}, {"what"}} {
		found, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v is not mounted on the root command: %v", path, err)
		}
		if found.Name() != "what" {
			t.Fatalf("%v resolved to %q, want a command named %q", path, found.Name(), "what")
		}
		if found.RunE == nil {
			t.Fatalf("%v resolved but has no RunE", path)
		}
	}
}

// TestWhatAliasHidden proves the top-level `what` alias is hidden from
// --help and shell completions (cobra's IsAvailableCommand excludes any
// command with Hidden set — the same mechanism --help and completion
// generation both consult) while `recall what` stays visible.
func TestWhatAliasHidden(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()

	alias, _, err := root.Find([]string{"what"})
	if err != nil {
		t.Fatalf("what alias not found: %v", err)
	}
	if !alias.Hidden {
		t.Fatal("cascade what must be Hidden (07-CLI-COMMAND-TREE §note-2)")
	}
	if alias.IsAvailableCommand() {
		t.Fatal("cascade what is Hidden but IsAvailableCommand() reports it visible")
	}

	primary, _, err := root.Find([]string{"recall", "what"})
	if err != nil {
		t.Fatalf("recall what not found: %v", err)
	}
	if primary.Hidden {
		t.Fatal("cascade recall what must stay visible — only the alias is hidden")
	}
}

// TestWhatAliasFlagSetParity proves the alias and the primary command
// declare identical local flag sets (07-CLI-COMMAND-TREE §recall: no
// flag beyond the positional query on either surface) and the same Use
// string, mirroring fleet_journal_test.go's
// TestFleetJournalAlias_IdenticalConstruction.
func TestWhatAliasFlagSetParity(t *testing.T) {
	deps := recallDeps{
		Getwd: func() (string, error) { return "/tmp", nil },
		Call:  func(context.Context, string, string, any, any) error { return nil },
	}
	primary := newRecallWhatCmd(deps)
	alias := newWhatCmd(deps)

	if primary.Use != alias.Use {
		t.Errorf("Use differs: primary=%q alias=%q", primary.Use, alias.Use)
	}
	if got := whatFlagNames(primary); len(got) != 0 {
		t.Errorf("recall what unexpectedly has local flags: %v", got)
	}
	if got := whatFlagNames(alias); len(got) != 0 {
		t.Errorf("what alias unexpectedly has local flags: %v", got)
	}
}

func whatFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	sort.Strings(names)
	return names
}

// TestWhatDelegatesToNewRecallWhatCmd is a static proof that newWhatCmd's
// body calls newRecallWhatCmd — the exact function `recall what` itself
// calls — rather than re-implementing a RunE closure. A byte-identical-
// output test alone cannot rule out two independently written
// implementations that happen to agree on today's fixtures; this fails
// the moment someone inlines a second cobra.Command literal into
// newWhatCmd instead of calling the shared constructor.
func TestWhatDelegatesToNewRecallWhatCmd(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "what.go", nil, 0)
	if err != nil {
		t.Fatalf("parse what.go: %v", err)
	}
	var calls []string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newWhatCmd" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				calls = append(calls, ident.Name)
			}
			return true
		})
		return false
	})
	for _, name := range calls {
		if name == "newRecallWhatCmd" {
			return
		}
	}
	t.Fatalf("newWhatCmd does not call newRecallWhatCmd (calls seen: %v) — the alias must delegate to the shared command constructor, never a second implementation", calls)
}

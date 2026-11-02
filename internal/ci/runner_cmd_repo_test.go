package ci

import (
	"io"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

func runRepoCmd(t *testing.T, deps CmdDeps, verb string) error {
	t.Helper()
	cmd := NewCICmd(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"repo", verb})
	return cmd.Execute()
}

func TestCIRepoCommandsNeedTheDaemon(t *testing.T) {
	for _, verb := range []string{"register", "list"} {
		if err := runRepoCmd(t, CmdDeps{}, verb); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("ci repo %s without a daemon = %v", verb, err)
		}
	}
}

func TestCIRepoCommandsRunTheirHooks(t *testing.T) {
	var ran []string
	hook := func(name string) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error { ran = append(ran, name); return nil }
	}
	deps := CmdDeps{RepoRegister: hook("register"), RepoList: hook("list")}
	for _, verb := range []string{"register", "list"} {
		if err := runRepoCmd(t, deps, verb); err != nil {
			t.Fatalf("ci repo %s: %v", verb, err)
		}
	}
	if len(ran) != 2 || ran[0] != "register" || ran[1] != "list" {
		t.Fatalf("hooks ran %v", ran)
	}
}

func TestProductionCmdDepsAreWired(t *testing.T) {
	d := ProductionCmdDeps()
	if d.Getenv == nil || d.Environ == nil || d.Clock == nil {
		t.Fatalf("production deps incomplete: %+v", d)
	}
	if d.Paths != nil {
		t.Fatal("production deps resolved paths eagerly")
	}
}

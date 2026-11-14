package crunch

import (
	"errors"
	"strings"
	"testing"
)

type mockVerifyCollabArgs struct {
	invoked bool
	target  string
	err     error
}

func (m *mockVerifyCollabArgs) Verify(_ []string, _ []string) (string, error) {
	m.invoked = true
	return m.target, m.err
}

// verifyArgsCase is one invalid input to VerifyWithNodesArgs.
type verifyArgsCase struct {
	name     string
	args     []string
	commands []string
	nodeIDs  []string
	nilColl  bool
}

// invalidVerifyArgsCases lists inputs VerifyWithNodesArgs must refuse; unset fields take valid defaults.
func invalidVerifyArgsCases() []verifyArgsCase {
	flag := []string{"--verify-nodes"}
	cs := []verifyArgsCase{
		{name: "invalid flag value", args: []string{"--verify-nodes=invalid"}},
		{name: "duplicate args", args: []string{"--verify-nodes", "--verify-nodes"}},
		{name: "unknown args", args: []string{"--unknown"}},
		{name: "nil collaborator", nilColl: true},
		{name: "empty command", commands: []string{""}},
		{name: "blank commands list", commands: []string{}},
		{name: "duplicate commands", commands: []string{"cmd1", "cmd1"}},
		{name: "blank node ID", nodeIDs: []string{""}},
		{name: "blank node IDs list", nodeIDs: []string{}},
		{name: "duplicate node IDs", nodeIDs: []string{"n1", "n1"}},
	}
	for i := range cs {
		if cs[i].args == nil {
			cs[i].args = flag
		}
		if cs[i].commands == nil {
			cs[i].commands = []string{"cmd1"}
		}
		if cs[i].nodeIDs == nil {
			cs[i].nodeIDs = []string{"n1"}
		}
	}
	return cs
}

func TestVerifyNodesRejectsInvalidInput(t *testing.T) {
	for _, tt := range invalidVerifyArgsCases() {
		t.Run(tt.name, func(t *testing.T) {
			var c VerifyCollaboratorArgs
			coll := &mockVerifyCollabArgs{}
			if !tt.nilColl {
				c = coll
			}
			_, err := VerifyWithNodesArgs(tt.args, tt.commands, tt.nodeIDs, c)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrVerifyNodesArgs) {
				t.Errorf("expected ErrVerifyNodesArgs, got %v", err)
			}
			if !strings.Contains(err.Error(), "D2204") {
				t.Errorf("expected error to contain D2204, got %s", err.Error())
			}
			if coll.invoked {
				t.Error("invalid input must not invoke Verify")
			}
		})
	}
}

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

func (m *mockVerifyCollabArgs) Verify(commands []string, nodeIDs []string) (string, error) {
	m.invoked = true
	return m.target, m.err
}

func TestVerifyNodesRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		commands []string
		nodeIDs  []string
		collab   *mockVerifyCollabArgs
	}{
		{
			name:     "invalid flag value",
			args:     []string{"--verify-nodes=invalid"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{"n1"},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "duplicate args",
			args:     []string{"--verify-nodes", "--verify-nodes"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{"n1"},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "unknown args",
			args:     []string{"--unknown"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{"n1"},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "nil collaborator",
			args:     []string{"--verify-nodes"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{"n1"},
			collab:   nil,
		},
		{
			name:     "empty command",
			args:     []string{"--verify-nodes"},
			commands: []string{""},
			nodeIDs:  []string{"n1"},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "blank commands list",
			args:     []string{"--verify-nodes"},
			commands: []string{},
			nodeIDs:  []string{"n1"},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "duplicate commands",
			args:     []string{"--verify-nodes"},
			commands: []string{"cmd1", "cmd1"},
			nodeIDs:  []string{"n1"},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "blank node ID",
			args:     []string{"--verify-nodes"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{""},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "blank node IDs list",
			args:     []string{"--verify-nodes"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{},
			collab:   &mockVerifyCollabArgs{},
		},
		{
			name:     "duplicate node IDs",
			args:     []string{"--verify-nodes"},
			commands: []string{"cmd1"},
			nodeIDs:  []string{"n1", "n1"},
			collab:   &mockVerifyCollabArgs{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c VerifyCollaboratorArgs
			if tt.collab != nil {
				c = tt.collab
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
			if tt.collab != nil && tt.collab.invoked {
				t.Error("invalid input must not invoke Verify")
			}
		})
	}
}

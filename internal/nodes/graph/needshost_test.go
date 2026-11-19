package graph

import (
	"strings"
	"testing"
)

// TestNeedsHostDaemonFreeCommands checks that every command that can run
// entirely on the node reports that it does not need the capp daemon.
func TestNeedsHostDaemonFreeCommands(t *testing.T) {
	cases := []struct {
		cmd  string
		args []string
	}{
		{"pbd", []string{"check"}},
		{"ready", nil},
		{"status", nil},
		{"waves", nil},
		{"graph", nil},
		{"trace", nil},
		{"pack", nil},
		{"suggest-sections", nil},
		{"version", nil},
	}

	for _, tc := range cases {
		name := tc.cmd
		if len(tc.args) > 0 {
			name += " " + strings.Join(tc.args, " ")
		}
		t.Run(name, func(t *testing.T) {
			needs, err := NeedsHost(tc.cmd, tc.args)
			if err != nil {
				t.Fatalf("NeedsHost(%q, %v): unexpected error: %v", tc.cmd, tc.args, err)
			}
			if needs {
				t.Fatalf("NeedsHost(%q, %v) = true, want false (daemon-free command)", tc.cmd, tc.args)
			}
		})
	}
}

// TestNeedsHostRejectsWriteFlag checks that a write flag on a daemon-free
// command is rejected instead of being classified.
func TestNeedsHostRejectsWriteFlag(t *testing.T) {
	needs, err := NeedsHost("pbd", []string{"check", "--write"})
	if err == nil {
		t.Fatalf("NeedsHost(\"pbd\", [\"check\", \"--write\"]) error is nil, want non-nil")
	}
	if needs {
		t.Fatalf("NeedsHost(\"pbd\", [\"check\", \"--write\"]) = true, want false alongside the error")
	}
}

// TestNeedsHostRejectsMixedBatch checks that a batch that mixes daemon-free
// and daemon-requiring command lines is rejected.
func TestNeedsHostRejectsMixedBatch(t *testing.T) {
	needs, err := NeedsHost("batch", []string{"status", "frobnicate"})
	if err == nil {
		t.Fatalf("NeedsHost(\"batch\", [\"status\", \"frobnicate\"]) error is nil, want non-nil")
	}
	if needs {
		t.Fatalf("NeedsHost(\"batch\", mixed) = true, want false alongside the error")
	}
}

// TestNeedsHostUnknownCommandNeedsDaemon checks that unknown commands are
// conservatively routed to the daemon.
func TestNeedsHostUnknownCommandNeedsDaemon(t *testing.T) {
	needs, err := NeedsHost("frobnicate", nil)
	if err != nil {
		t.Fatalf("NeedsHost(\"frobnicate\", nil): unexpected error: %v", err)
	}
	if !needs {
		t.Fatalf("NeedsHost(\"frobnicate\", nil) = false, want true (unknown command)")
	}
}

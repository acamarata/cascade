// Purpose: unit tests for `cascade fleet jobs list|show|cancel|retry` -
//
//	mount reachability and the no-daemon actionable refusal, following
//	fleet_journal_test.go's exact established pattern. This file
//	deliberately imports neither "net" nor "net/http", so it runs in the
//	fast, no-network unit lane.
//
// SPORT: cmd/cascade/fleet-jobs-cli (ADD, P1-E29-W6-S60-T1).
package main

import (
	"strings"
	"testing"
)

func TestFleetJobsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{
		{"fleet", "jobs", "list"}, {"fleet", "jobs", "show"},
		{"fleet", "jobs", "cancel"}, {"fleet", "jobs", "retry"},
	} {
		found, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v is not mounted on the root command: %v", path, err)
		}
		wantName := path[len(path)-1]
		if !strings.HasPrefix(found.Name(), wantName) {
			t.Fatalf("%v resolved to %q, want a command named %q", path, found.Name(), wantName)
		}
	}
}

func TestFleetJobsList_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetJobsListCmd(fleetSessionsDeps{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet jobs list with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

func TestFleetJobsShow_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetJobsShowCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"job-1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet jobs show with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

func TestFleetJobsCancel_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetJobsCancelCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"job-1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet jobs cancel with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

func TestFleetJobsRetry_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetJobsRetryCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"job-1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet jobs retry with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

func TestFleetJobsShow_ExtraArgRefused(t *testing.T) {
	cmd := newFleetJobsShowCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"job-a", "job-b"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("fleet jobs show with two positional args: want an error, got nil")
	}
}

func TestJobRows_StringRendersTable(t *testing.T) {
	rows := jobRows{{ID: "j1", State: "pending", RiskClass: "normal", Priority: 1, CreatedAt: 10}}
	out := rows.String()
	if !strings.Contains(out, "j1") || !strings.Contains(out, "pending") {
		t.Fatalf("jobRows.String() = %q, missing expected fields", out)
	}
}

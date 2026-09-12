// Purpose: unit tests for `cascade fleet leases list|release` - mount
//
//	reachability and the no-daemon actionable refusal, mirroring
//	fleet_jobs_test.go's pattern.
//
// SPORT: cmd/cascade/fleet-leases-cli (ADD, P1-E29-W6-S60-T1).
package main

import (
	"strings"
	"testing"
)

func TestFleetLeasesMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{
		{"fleet", "leases", "list"}, {"fleet", "leases", "release"},
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

func TestFleetLeasesList_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetLeasesListCmd(fleetSessionsDeps{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet leases list with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

func TestFleetLeasesRelease_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetLeasesReleaseCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"repo1:/a"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet leases release with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

func TestFleetLeasesRelease_ExtraArgRefused(t *testing.T) {
	cmd := newFleetLeasesReleaseCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"lease-a", "lease-b"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("fleet leases release with two positional args: want an error, got nil")
	}
}

func TestLeaseRows_StringRendersTable(t *testing.T) {
	rows := leaseRows{{ID: "repo1:/a", RepoID: "repo1", ScopeGlob: "/a", Holder: "job-1", IssuedAt: 5, TTLSeconds: 60}}
	out := rows.String()
	if !strings.Contains(out, "repo1:/a") || !strings.Contains(out, "job-1") {
		t.Fatalf("leaseRows.String() = %q, missing expected fields", out)
	}
}

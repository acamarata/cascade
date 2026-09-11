// Purpose: asserts each of the seven typed records carries exactly its
//
//	DECIDED field set plus the HOW step 2b W9 columns, via a
//	compile-time + reflection field-count table (never a hand-eyeballed
//	read of model.go, which drifts silently).
//
// SPORT: jobs/domain-schema/ADD (P1-E29-W6-S59-T1).
package jobs

import (
	"reflect"
	"testing"
)

func TestRecordFieldSets(t *testing.T) {
	cases := []struct {
		name   string
		v      any
		fields []string
	}{
		{"Job", Job{}, []string{
			"ID", "State", "CreatedAt", "UpdatedAt", "Capabilities", "MutableScope",
			"RiskClass", "MinTaskClass", "NodeRequirements", "TimeoutSeconds",
			"CostCeiling", "Priority", "ConsecutiveFailedAttempts", "ConsequenceClass", "DataClass",
		}},
		{"TaskDependency", TaskDependency{}, []string{"JobID", "DependsOnJobID"}},
		{"Execution", Execution{}, []string{
			"ID", "JobID", "Attempt", "State", "StartedAt", "EndedAt", "PGID", "HeartbeatAt",
		}},
		{"ExecutionResult", ExecutionResult{}, []string{
			"ExecutionID", "OutputSummary", "ErrorKind", "ErrorMessage", "ArtifactRefs",
		}},
		{"Artifact", Artifact{}, []string{
			"ID", "JobID", "ExecutionID", "Kind", "BlobKey", "Path", "ConsequenceClass", "DataClass",
		}},
		{"ResourceLease", ResourceLease{}, []string{
			"RepoID", "ScopeGlob", "Holder", "IssuedAt", "TTLSeconds", "RenewCount",
			"JournalRef", "Epoch", "State",
		}},
		{"Worktree", Worktree{}, []string{
			"Path", "LeaseRepoID", "LeaseScopeGlob", "Repo", "Branch",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := reflect.TypeOf(c.v)
			if rt.NumField() != len(c.fields) {
				t.Fatalf("%s has %d fields, want %d (%v)", c.name, rt.NumField(), len(c.fields), c.fields)
			}
			for i, want := range c.fields {
				if got := rt.Field(i).Name; got != want {
					t.Errorf("%s field %d = %q, want %q", c.name, i, got, want)
				}
			}
		})
	}
}

func TestConsequenceClassDecode(t *testing.T) {
	for _, c := range []ConsequenceClass{ConsequenceTrivial, ConsequenceNormal, ConsequenceConsequential} {
		got, err := DecodeConsequenceClass(string(c))
		if err != nil || got != c {
			t.Errorf("DecodeConsequenceClass(%q) = (%q, %v), want (%q, nil)", c, got, err, c)
		}
	}
	if _, err := DecodeConsequenceClass("bogus"); err == nil {
		t.Error("DecodeConsequenceClass(bogus) = nil error, want typed error")
	}
	if _, err := DecodeConsequenceClass(""); err == nil {
		t.Error("DecodeConsequenceClass(\"\") = nil error, want typed error")
	}
}

func TestDataClassDecode(t *testing.T) {
	for _, c := range []DataClass{DataClassPublic, DataClassInternal, DataClassConfidential, DataClassSecret} {
		got, err := DecodeDataClass(string(c))
		if err != nil || got != c {
			t.Errorf("DecodeDataClass(%q) = (%q, %v), want (%q, nil)", c, got, err, c)
		}
	}
	if _, err := DecodeDataClass("bogus"); err == nil {
		t.Error("DecodeDataClass(bogus) = nil error, want typed error")
	}
}

func TestExecutionStateDecodeFailsClosed(t *testing.T) {
	for _, s := range []ExecutionState{ExecutionRunning, ExecutionSucceeded, ExecutionFailed, ExecutionAbandoned} {
		if got := DecodeExecutionState(string(s)); got != s {
			t.Errorf("DecodeExecutionState(%q) = %q, want %q", s, got, s)
		}
	}
	if got := DecodeExecutionState("bogus"); got != ExecutionFailed {
		t.Errorf("DecodeExecutionState(bogus) = %q, want failed", got)
	}
}

func TestLeaseStateDecode(t *testing.T) {
	for _, s := range []LeaseState{LeaseHeld, LeaseRenewing, LeaseExpiredUnconfirmed, LeaseExpiredOrphaned, LeaseReleased} {
		got, err := DecodeLeaseState(string(s))
		if err != nil || got != s {
			t.Errorf("DecodeLeaseState(%q) = (%q, %v), want (%q, nil)", s, got, err, s)
		}
	}
	if _, err := DecodeLeaseState("bogus"); err == nil {
		t.Error("DecodeLeaseState(bogus) = nil error, want typed error")
	}
}

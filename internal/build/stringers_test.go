package build

// Purpose: unit coverage for the seven violation-rendering functions a
//   failing gate uses to explain itself to a developer (BudgetViolation.
//   String, CountDriftViolation.String, LedgerIdentityViolation.String,
//   RPCMethodViolation.String, RPCMethodUnresolved.String,
//   FormatSecretScanHits, FormatSweepViolations). Each test asserts the
//   rendered text actually names the offending file/symbol and the
//   expected-vs-actual values a developer needs to act on the failure -
//   not merely that the string is non-empty.
// SPORT: internal/build (ADD, coverage-floor fix).

import (
	"strings"
	"testing"
)

func TestBudgetViolation_String_NamesMetricAndValues(t *testing.T) {
	v := BudgetViolation{Name: "bench_lane", Metric: "ns_per_op", Budget: 100.5, Actual: 250.25}
	got := v.String()
	for _, want := range []string{"bench_lane", "ns_per_op", "100.50", "250.25"} {
		if !strings.Contains(got, want) {
			t.Errorf("BudgetViolation.String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestCountDriftViolation_String_NamesFileLineAndCounts(t *testing.T) {
	v := CountDriftViolation{
		File: "docs/inventory.md", Line: 42, Noun: "provider",
		Stated: 5, Derived: 7, Text: "there are 5 provider(s)",
	}
	got := v.String()
	for _, want := range []string{"docs/inventory.md", "42", "provider", "5", "7", "there are 5 provider(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("CountDriftViolation.String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestLedgerIdentityViolation_String_Missing_NamesFileAndLine(t *testing.T) {
	v := LedgerIdentityViolation{File: "internal/repo/graph_store.go", Line: 74, Kind: "missing"}
	got := v.String()
	if !strings.Contains(got, "internal/repo/graph_store.go:74") {
		t.Errorf("LedgerIdentityViolation.String() (missing) = %q, want it to name the file:line", got)
	}
	if !strings.Contains(got, "no SetID") {
		t.Errorf("LedgerIdentityViolation.String() (missing) = %q, want it to explain the defect", got)
	}
}

func TestLedgerIdentityViolation_String_Duplicate_NamesBothClaimants(t *testing.T) {
	v := LedgerIdentityViolation{
		File: "internal/repo/ledger_store.go", Line: 33, Kind: "duplicate",
		SetID: "repo-graph", OtherFile: "internal/repo/graph_store.go", OtherLine: 45,
	}
	got := v.String()
	for _, want := range []string{"internal/repo/ledger_store.go:33", "repo-graph", "internal/repo/graph_store.go:45"} {
		if !strings.Contains(got, want) {
			t.Errorf("LedgerIdentityViolation.String() (duplicate) = %q, want it to contain %q", got, want)
		}
	}
}

func TestRPCMethodViolation_String_NamesFileLineAndMethod(t *testing.T) {
	v := RPCMethodViolation{File: "cmd/cascade/run_exec.go", Line: 132, Method: "conductor.execute"}
	got := v.String()
	for _, want := range []string{"cmd/cascade/run_exec.go:132", "conductor.execute", "nothing in the tracked tree registers"} {
		if !strings.Contains(got, want) {
			t.Errorf("RPCMethodViolation.String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestRPCMethodUnresolved_String_NamesFileAndLine(t *testing.T) {
	v := RPCMethodUnresolved{File: "cmd/cascade/memory.go", Line: 58}
	got := v.String()
	if !strings.Contains(got, "cmd/cascade/memory.go:58") {
		t.Errorf("RPCMethodUnresolved.String() = %q, want it to name the file:line", got)
	}
	if !strings.Contains(got, "not a string literal") {
		t.Errorf("RPCMethodUnresolved.String() = %q, want it to explain why it's unresolved", got)
	}
}

func TestFormatSecretScanHits_NamesEveryHit(t *testing.T) {
	hits := []SecretScanHit{
		{File: "internal/x/fixture.go", Class: "aws-access-key", Pattern: "aws-akid", Confidence: 0.95},
		{File: "internal/y/other.go", Class: "private-key", Pattern: "pem-block", Confidence: 1.0},
	}
	got := FormatSecretScanHits(hits)
	for _, want := range []string{"internal/x/fixture.go", "aws-access-key", "0.95", "internal/y/other.go", "private-key", "1.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatSecretScanHits() = %q, want it to contain %q", got, want)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("FormatSecretScanHits() = %q, want no trailing newline", got)
	}
}

func TestFormatSecretScanHits_Empty(t *testing.T) {
	if got := FormatSecretScanHits(nil); got != "" {
		t.Errorf("FormatSecretScanHits(nil) = %q, want empty", got)
	}
}

func TestFormatSweepViolations_NamesEveryViolation(t *testing.T) {
	violations := []SweepViolation{
		{Source: "internal/x/fixture.go", Line: 10, Pattern: "seamlessly", Snippet: "works seamlessly today"},
		{Source: "internal/y/other.go", Line: 21, Pattern: "robust", Snippet: "a robust solution"},
	}
	got := FormatSweepViolations(violations)
	for _, want := range []string{
		"internal/x/fixture.go:10", "seamlessly", "works seamlessly today",
		"internal/y/other.go:21", "robust", "a robust solution",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatSweepViolations() = %q, want it to contain %q", got, want)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("FormatSweepViolations() = %q, want no trailing newline", got)
	}
}

func TestFormatSweepViolations_Empty(t *testing.T) {
	if got := FormatSweepViolations(nil); got != "" {
		t.Errorf("FormatSweepViolations(nil) = %q, want empty", got)
	}
}

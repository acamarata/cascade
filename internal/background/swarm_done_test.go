package background

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmitDoneEvent verifies terminal events and successful escalation no-ops.
// D2596 D2597 D2602 D2644 are traceability identifiers.
func TestEmitDoneEvent(t *testing.T) {
	for _, detail := range []string{"", "failure"} {
		result, err := RunSwarmAttemptDone(Attempt{N: 1, Err: detail}, func() (string, error) {
			return "brief", nil
		})
		if err != nil {
			t.Fatalf("RunSwarmAttemptDone: %v", err)
		}
		if len(result.Events) == 0 || result.Events[len(result.Events)-1] != (Event{Stage: "done", Kind: "done"}) {
			t.Errorf("detail=%q: terminal event mismatch: %+v", detail, result.Events)
		}
		wantStatus := "done"
		if detail != "" {
			wantStatus = "needs-owner"
		}
		if result.Outcome.Status != wantStatus {
			t.Errorf("outcome=%+v, want status %q", result.Outcome, wantStatus)
		}
	}
	for _, existing := range []bool{false, true} {
		for _, detail := range []string{"", "ignored"} {
			path := filepath.Join(t.TempDir(), "escalate.jsonl")
			const original = "{\"status\":\"needs-owner\",\"detail\":\"failure\"}\n"
			if existing {
				if err := WriteEscalateNoop(path, Outcome{Status: "needs-owner", Detail: "failure"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := WriteEscalateNoop(path, Outcome{Status: "done", Detail: detail}); err != nil {
				t.Fatalf("WriteEscalateNoop done: %v", err)
			}
			data, err := os.ReadFile(path)
			if existing {
				if err != nil || string(data) != original {
					t.Errorf("existing=%t detail=%q: done escalation changed file: data=%q error=%v", existing, detail, data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Errorf("existing=%t detail=%q: done escalation created a file: data=%q error=%v", existing, detail, data, err)
			}
		}
	}
}

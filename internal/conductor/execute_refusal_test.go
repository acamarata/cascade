package conductor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failSelect makes the executor's router fail with err.
func failSelect(deps *readyDeps, err error) {
	deps.router.selectFn = func(context.Context, provider.ModelRequest, ...string) (provider.Selection, error) {
		return provider.Selection{}, err
	}
}

// externalLane is a removed candidate the lane table would classify as an
// external API lane, so the refusal hint names the permit key.
func externalLane(name string) laneCandidate {
	return laneCandidate{
		lane:     provider.LaneInfo{LaneName: name},
		provider: provider.ProviderInfo{BaseURL: "https://api.example.com"},
	}
}

// realLaneRefusal is what the lane table returns for an emptied restricted set.
func realLaneRefusal() error {
	return emptiedRefusal(provider.SensitivityRestricted, laneVerdict{removed: []laneCandidate{externalLane("cloud-a")}})
}

// realPrivacyRefusal is the thread-privacy refusal, which carries a thread id.
func realPrivacyRefusal(threadID string) error {
	return privacyRefusal(ThreadPrivacy{ThreadID: threadID}, provider.SensitivityRestricted, []laneCandidate{externalLane("cloud-a")})
}

type runner struct {
	name string
	run  func(*Executor) error
}

var runners = []runner{
	{"Execute", func(e *Executor) error { _, err := e.Execute(context.Background(), validReq()); return err }},
	{"ExecuteStream", func(e *Executor) error { _, _, err := e.ExecuteStream(context.Background(), validReq()); return err }},
}

// auditText is every audit row's explain payload joined, for leak checks.
func auditText(deps *readyDeps) string {
	var b strings.Builder
	for _, ev := range deps.audit.events {
		b.Write(ev.Explain)
	}
	return b.String()
}

// TestSelectRefusal_LaneTableRefusalNamesTheKey: the lane-table refusal reaches
// the caller and the audit row with its detail, and still wraps the sentinel.
func TestSelectRefusal_LaneTableRefusalNamesTheKey(t *testing.T) {
	for _, r := range runners {
		t.Run(r.name, func(t *testing.T) {
			exec, deps := newReadyExecutor(t)
			failSelect(deps, realLaneRefusal())
			err := r.run(exec)
			if !errors.Is(err, ErrSensitivityViolation) || !strings.Contains(err.Error(), RestrictedPermitsKey) {
				t.Fatalf("error = %v, want ErrSensitivityViolation naming %s", err, RestrictedPermitsKey)
			}
			if deps.audit.count() != 1 || !strings.Contains(auditText(deps), RestrictedPermitsKey) {
				t.Fatalf("audit rows = %d, text %q; want one row naming the key", deps.audit.count(), auditText(deps))
			}
		})
	}
}

// TestSelectRefusal_OuterWrapperTextIsDropped: text an outer wrapper adds around
// the lane refusal never reaches the caller or the audit row.
func TestSelectRefusal_OuterWrapperTextIsDropped(t *testing.T) {
	const marker = "secret-host.internal"
	exec, deps := newReadyExecutor(t)
	failSelect(deps, fmt.Errorf("wrapped by %s: %w", marker, realLaneRefusal()))
	err := runners[0].run(exec)
	if !errors.Is(err, ErrSensitivityViolation) || strings.Contains(err.Error(), marker) || strings.Contains(auditText(deps), marker) {
		t.Fatalf("error %v, audit %q: want the lane refusal without the outer wrapper text", err, auditText(deps))
	}
}

// TestSelectRefusal_EverythingElseIsTheBareNoLane: a privacy refusal (it names
// a thread id), an unrelated policy_denied error and a plain error all become
// the bare sentinel, and nothing from them reaches the caller or the audit row.
func TestSelectRefusal_EverythingElseIsTheBareNoLane(t *testing.T) {
	const marker = "secret-host.internal"
	cases := map[string]error{
		"privacy refusal":         realPrivacyRefusal("thread-" + marker),
		"unrelated policy denial": cascade.New(cascade.KindPolicyDenied, marker),
		"plain error":             errors.New("registry exploded: " + marker),
	}
	for name, selErr := range cases {
		for _, r := range runners {
			t.Run(name+"/"+r.name, func(t *testing.T) {
				exec, deps := newReadyExecutor(t)
				failSelect(deps, selErr)
				if err := r.run(exec); err != ErrNoLane {
					t.Fatalf("error = %v, want the bare ErrNoLane", err)
				}
				if deps.audit.count() != 1 || strings.Contains(auditText(deps), marker) {
					t.Fatalf("audit rows = %d, text %q; want one row without %q", deps.audit.count(), auditText(deps), marker)
				}
			})
		}
	}
}

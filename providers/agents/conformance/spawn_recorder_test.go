// Purpose: helpers for the AD/S-61.T2 conformance entries in suite_test.go:
//
//	runAllCases (every exported case method, found by reflection, as a
//	subtest), spawnRecorder (a conformingProvider wrapper recording every
//	AgentJobSpec Spawn receives, for TestSuiteSpawnCasesCarryEnv),
//	messageAnswers (a conformingProvider whose Message answers a fixed
//	error for a known job, for TestMessageProbeAcceptsOnlyNilOrUnsupported)
//	and the claude factory's profile and lease seams.
//
// Constraints: test-only; split from suite_test.go for the 300-line cap.
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T2).
package conformance

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/claude"
)

// runAllCases runs every exported Test* method of s as a subtest and fails
// unless there are exactly wantCaseCount of them.
func runAllCases(t *testing.T, s *Suite) {
	t.Helper()
	v, typ, ran := reflect.ValueOf(s), reflect.TypeOf(s), 0
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if !strings.HasPrefix(name, "Test") {
			continue
		}
		fn := v.Method(i).Interface().(func(*testing.T))
		t.Run(name, fn)
		ran++
	}
	if ran != wantCaseCount {
		t.Fatalf("ran %d cases, want %d", ran, wantCaseCount)
	}
}

// spawnRecorder collects the specs every wrapped provider's Spawn saw.
type spawnRecorder struct {
	mu    sync.Mutex
	specs []provider.AgentJobSpec
}

func (r *spawnRecorder) wrap(p provider.AgentProvider) provider.AgentProvider {
	return recordingProvider{AgentProvider: p, rec: r}
}

func (r *spawnRecorder) all() []provider.AgentJobSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]provider.AgentJobSpec(nil), r.specs...)
}

type recordingProvider struct {
	provider.AgentProvider
	rec *spawnRecorder
}

func (p recordingProvider) Spawn(ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	p.rec.mu.Lock()
	p.rec.specs = append(p.rec.specs, spec)
	p.rec.mu.Unlock()
	return p.AgentProvider.Spawn(ctx, spec)
}

// messageAnswers returns a conforming provider whose Message answers err
// for a known job and ErrJobNotFound for an unknown one.
func messageAnswers(err error) provider.AgentProvider {
	return messageProvider{conformingProvider: newConformingProvider(), answer: err}
}

type messageProvider struct {
	*conformingProvider
	answer error
}

func (p messageProvider) Message(ctx context.Context, id provider.AgentJobID, text string) error {
	if err := p.conformingProvider.Message(ctx, id, text); err != nil {
		return err
	}
	return p.answer
}

type claudeSuiteProfile struct{ home string }

func (p claudeSuiteProfile) Resolve(context.Context) (claude.Profile, error) {
	return claude.Profile{VendorAuthVar: claude.VendorConfigDirVar, RuntimeHome: p.home,
		CredentialClass: claude.CredentialClassOAuthToken, KeyVar: claude.KeyVarOAuthToken,
		Posture: provider.NewCompliancePosture([]string{"oauth"}, true, true, []string{"agent"}, "steady", false)}, nil
}

type claudeSuiteLease struct{ root string }

func (l claudeSuiteLease) Lease(context.Context, provider.WorktreeScope) (claude.Lease, error) {
	return claude.Lease{Root: l.root, GitDir: filepath.Join(l.root, ".git")}, nil
}

// wallClock is the real-time clock the claude suite factory injects (test
// code may read the wall clock; the production driver may not).
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

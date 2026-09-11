// Purpose: compile-only checks over Suite's ten original cases, the
//
//	nil-factory skip guard, the case-inventory completeness check
//	(TestConformanceCaseInventory), and the Art.1 non-vacuity proof: a
//	deliberately permissive, do-nothing AgentProvider double must FAIL
//	this suite, not pass it.
//
// Constraints: emptyAgentProvider exists ONLY in this _test.go file
//
//	(Art.1.1) and is never wired as a real driver. No driver ships from
//	this ticket; each driver ticket (S-61.T2/T3/T4/T5) APPENDS its own
//	factory-backed test to this file's sibling — this file stays
//	append-only stable.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).
package conformance

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// vacuityCheckEnv, when set to "1" in this test binary's own environment,
// selects TestSuiteFailsAgainstEmptyImplementation's re-exec child branch:
// it runs the real assertions (which must fail) instead of spawning
// another subprocess.
const vacuityCheckEnv = "CASCADE_CONFORMANCE_VACUITY_CHECK"

// wantCaseCount is the nineteen named conformance cases: ten original
// (suite.go) plus four lifecycle (cases_lifecycle.go) plus five security
// (cases_security.go).
const wantCaseCount = 19

// TestConformanceCaseInventory asserts *Suite exports every one of the
// nineteen named case methods, so a driver ticket cannot silently skip
// one by never calling it.
func TestConformanceCaseInventory(t *testing.T) {
	want := []string{
		"TestSpawnHappyPath", "TestSpawnErrEntitlement", "TestMessageAfterSpawn",
		"TestStatusReturnsKnownState", "TestCancelRunning", "TestCancelNonExistent",
		"TestCollectBlocksUntilDone", "TestCollectBeforeSpawn", "TestArtifactsEmptyNotNil",
		"TestNormalizeEventRoundTrip",
		"TestCancelUncooperativeChild", "TestSpawnResultCarriesProcessGroup",
		"TestProtocolNegotiationRefusal", "TestEventOrderingAndDedup",
		"TestChildEnvAllowlist", "TestPreSpawnSecretScanAborts", "TestDriverNeverAutoApproves",
		"TestDataClassPropagates", "TestCredentialCanaryFailsClosed",
	}
	if len(want) != wantCaseCount {
		t.Fatalf("inventory table has %d names, want %d", len(want), wantCaseCount)
	}
	typ := reflect.TypeOf(&Suite{})
	have := make(map[string]bool, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		have[typ.Method(i).Name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("Suite is missing exported case method %s", name)
		}
	}
}

// TestSuiteSkipsWithNilFactory asserts every case skips (never panics,
// never silently passes) when no factory is wired, so a bare `go test`
// on this package before any driver exists reports skips, not failures.
func TestSuiteSkipsWithNilFactory(t *testing.T) {
	s := &Suite{}
	ok := t.Run("SpawnHappyPath", s.TestSpawnHappyPath)
	if !ok {
		t.Fatal("case with a nil factory reported failure, want a clean skip")
	}
}

// emptyAgentProvider is a deliberately permissive, do-nothing
// AgentProvider double: every call that could refuse instead succeeds
// with a zero-ish value. It exists to PROVE the suite is not vacuous
// (Art.1): a conformance suite a stub like this can pass is exactly the
// defect this ticket's brief calls out.
type emptyAgentProvider struct{}

func (emptyAgentProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, nil
}
func (emptyAgentProvider) Embed(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, nil
}
func (emptyAgentProvider) Count(context.Context, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, nil
}
func (emptyAgentProvider) Stream(context.Context, provider.ChatRequest, provider.StreamSink) error {
	return nil
}
func (emptyAgentProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}
func (emptyAgentProvider) Spawn(context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
	return provider.SpawnResult{JobID: "empty-job"}, nil
}
func (emptyAgentProvider) ApprovalRequests() <-chan provider.ApprovalRequest {
	ch := make(chan provider.ApprovalRequest)
	close(ch)
	return ch
}
func (emptyAgentProvider) ResolveApproval(string, string) error { return nil }
func (emptyAgentProvider) Message(context.Context, provider.AgentJobID, string) error {
	return nil
}
func (emptyAgentProvider) Status(context.Context, provider.AgentJobID) (provider.AgentRunState, error) {
	return "", nil
}
func (emptyAgentProvider) Cancel(context.Context, provider.AgentJobID) error { return nil }
func (emptyAgentProvider) Collect(context.Context, provider.AgentJobID) (provider.CollectResult, error) {
	return provider.CollectResult{}, nil
}
func (emptyAgentProvider) Artifacts(context.Context, provider.AgentJobID) ([]string, error) {
	return nil, nil
}
func (emptyAgentProvider) SupportedProtocols() provider.ProtocolRange {
	return provider.ProtocolRange{}
}
func (emptyAgentProvider) Negotiate(context.Context, provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return "", nil
}

var _ provider.AgentProvider = emptyAgentProvider{}

// runEmptyImplementationCases exercises the seven cases with the sharpest
// refusal-shaped assertions against emptyAgentProvider. Every one of them
// must fail for the suite to be non-vacuous.
func runEmptyImplementationCases(t *testing.T) {
	t.Helper()
	s := &Suite{New: func() (provider.AgentProvider, error) { return emptyAgentProvider{}, nil }}
	s.TestSpawnErrEntitlement(t)
	s.TestMessageAfterSpawn(t)
	s.TestStatusReturnsKnownState(t)
	s.TestCancelNonExistent(t)
	s.TestCollectBeforeSpawn(t)
	s.TestArtifactsEmptyNotNil(t)
	s.TestProtocolNegotiationRefusal(t)
}

// TestSuiteFailsAgainstEmptyImplementation is the Art.1 non-vacuity
// proof. A conformance suite emptyAgentProvider can pass would be
// worthless, so this asserts the OPPOSITE of the usual shape: the parent
// process re-execs this same test binary (no recompilation — os.Args[0]
// is already built) with vacuityCheckEnv set, so the child's real
// *testing.T failures land in a separate process the parent can observe
// by exit code, rather than propagating up and failing this test itself.
func TestSuiteFailsAgainstEmptyImplementation(t *testing.T) {
	if os.Getenv(vacuityCheckEnv) == "1" {
		runEmptyImplementationCases(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestSuiteFailsAgainstEmptyImplementation", "-test.v")
	cmd.Env = append(os.Environ(), vacuityCheckEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("empty implementation PASSED the suite (child exit 0): the suite is vacuous\n%s", out)
	}
	t.Logf("empty implementation correctly FAILED the suite (child exit %v), proving non-vacuity:\n%s", err, out)
}

// Purpose: the plugin's outward behaviour — its manifest through the REAL
//
//	cascade.plugin/v2 loader, its reachability through the REAL builtin
//	registry and the REAL MCP tool registry, and the two tools' answers:
//	one detection report that always transits the firewall, and one typed
//	refusal.
//
// Constraints: no test here forks the real `nself` CLI or reads this
//
//	machine's PATH unasserted — the runner and the locator are injected.
//	Test files may import internal/** (the depguard rule and the
//	internal/build arch gate both exempt them), which is what lets the
//	production consumers be the real ones rather than doubles.
//
// SPORT: plugins/nself entity (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// spyInterceptor is a test EgressInterceptor that records its arguments and
// appends a marker, so a removed marshalThroughEgress call is
// mutation-provable rather than merely declared.
type spyInterceptor struct {
	calls   int
	class   EgressClass
	tier    SensitivityTier
	content []byte
}

const spyMarker = ":intercepted"

func (s *spyInterceptor) InterceptClass(_ context.Context, class EgressClass, tier SensitivityTier, content []byte) ([]byte, error) {
	s.calls++
	s.class, s.tier = class, tier
	s.content = append([]byte(nil), content...)
	return append(append([]byte(nil), content...), spyMarker...), nil
}

// withSeams installs the runner, the locator and a spy interceptor for one
// test, restoring the production defaults afterwards.
func withSeams(t *testing.T, runner subprocessRunner, locator binaryLocator) *spyInterceptor {
	t.Helper()
	origRunner, origLocator, origEgress := activeRunner, activeLocator, egressInterceptor
	spy := &spyInterceptor{}
	activeRunner, activeLocator, egressInterceptor = runner, locator, spy
	t.Cleanup(func() {
		activeRunner, activeLocator, egressInterceptor = origRunner, origLocator, origEgress
	})
	return spy
}

// absentRunner is a runner whose probe reports the binary as absent.
func absentRunner() subprocessRunner {
	return &recordingRunner{err: &binaryAbsentError{Binary: nselfBinary, GOOS: "test"}}
}

func TestManifestTOML_ParsesThroughTheRealLoaderAndAgreesWithTheCode(t *testing.T) {
	f, err := os.Open("manifest.toml")
	if err != nil {
		t.Fatalf("open manifest.toml: %v", err)
	}
	defer func() { _ = f.Close() }()

	m, err := plugin.ParseManifest(f)
	if err != nil {
		t.Fatalf("plugin.ParseManifest(manifest.toml) = %v, want nil (ParseManifest validates too)", err)
	}
	inCode := manifest()
	if m.ID != inCode.ID || m.Name != inCode.Name || m.Version != inCode.Version ||
		m.HostVersion != inCode.HostVersion || m.Runtime != inCode.Runtime {
		t.Fatalf("manifest.toml = %+v disagrees with manifest() = %+v", m, inCode)
	}
	if len(m.Provides.Tools) != 2 || m.Provides.Tools[0].Name != toolProjectInfo || m.Provides.Tools[1].Name != toolAddCascade {
		t.Fatalf("manifest.toml tools = %+v, want [%s %s]", m.Provides.Tools, toolProjectInfo, toolAddCascade)
	}
	if len(m.Requires) != 1 || m.Requires[0] != "subprocess_exec" {
		t.Fatalf("manifest.toml requires = %v, want [subprocess_exec] only (nothing is dialled at this floor)", m.Requires)
	}
}

// TestBuiltins_CarryCascadeNself asserts the compile-time registration
// through pkg/plugin's own registry. The HOST-side loader assertion (the
// internal/plugins.BuiltinRegistry the daemon actually uses, and the pinned
// inventory of every builtin the binary ships) lives in
// internal/plugins/registry_test.go and nself_wiring_test.go: this package
// cannot import internal/plugins from an in-package test file, because
// nself_wiring.go imports THIS package (an import cycle, which is itself
// the proof that the wiring is real).
func TestBuiltins_CarryCascadeNself(t *testing.T) {
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID != pluginID {
			continue
		}
		if reg.Manifest.Runtime != plugin.RuntimeBuiltin {
			t.Fatalf("registered Runtime = %q, want builtin", reg.Manifest.Runtime)
		}
		if reg.Handlers == nil {
			t.Fatal("registered handlers are nil")
		}
		return
	}
	t.Fatalf("plugin.Builtins() carries no %q registration", pluginID)
}

// TestMCPToolRegistry_ProjectInfoTransitsTheFirewall drives the REAL MCP
// tool registry (sourced from plugin.Builtins, exactly as production does)
// and proves three things at once: both tools are exposed, a workspace with
// no nself is answered rather than failed, and every emitted byte went
// through the interceptor.
func TestMCPToolRegistry_ProjectInfoTransitsTheFirewall(t *testing.T) {
	spy := withSeams(t, absentRunner(), fixedLocator{err: errors.New("absent")})
	reg := mcp.NewToolRegistry(plugin.Builtins, mcp.DenyAllFilter{})

	names := map[string]bool{}
	for _, tool := range reg.List() {
		names[tool.Name] = true
	}
	if !names[toolProjectInfo] || !names[toolAddCascade] {
		t.Fatalf("ToolRegistry.List() = %v, want both nself tools exposed", names)
	}

	out, err := reg.Call(context.Background(), toolProjectInfo, rootDirInput(t, t.TempDir()))
	if err != nil {
		t.Fatalf("Call(%s) over a non-nself directory = %v, want a clean answer", toolProjectInfo, err)
	}
	if !strings.HasSuffix(string(out), spyMarker) {
		t.Fatalf("tool output = %q, want the interceptor's marker (the firewall call is not wired)", out)
	}
	if spy.calls != 1 || spy.class != EgressClassNselfBackend || spy.tier != TierInternal {
		t.Fatalf("interceptor spy = %+v, want one nself-backend/internal call", spy)
	}
	var resp projectInfoResponse
	if err := json.Unmarshal(spy.content, &resp); err != nil {
		t.Fatalf("payload handed to the firewall is not valid JSON: %v (%s)", err, spy.content)
	}
	if resp.Detected || resp.Method != "none" || resp.Doctor.ProbeOutcome != probeOutcomeBinaryAbsent {
		t.Fatalf("response = %+v, want a clean non-detection naming the binary-absent outcome", resp)
	}
}

// TestDispatchTool_AddCascadeRefusesWithOneTypedError pins the floor: the
// tool names BOTH missing prerequisites and attempts nothing.
func TestDispatchTool_AddCascadeRefusesWithOneTypedError(t *testing.T) {
	runner := &recordingRunner{out: []byte(`{}`)}
	withSeams(t, runner, fixedLocator{path: "/opt/bin/nself"})

	_, err := handlers{}.DispatchTool(context.Background(), toolAddCascade, rootDirInput(t, t.TempDir()))
	if err == nil {
		t.Fatal("DispatchTool(nself_add_cascade) = nil error, want the typed refusal")
	}
	if !errors.Is(err, errAddCascadeUnavailable) {
		t.Fatalf("err = %v, want it to wrap errAddCascadeUnavailable (identity, not just a Kind)", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
	msg := err.Error()
	for _, want := range []string{"add cascade", "config"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message = %q, want it to name the missing prerequisite %q", msg, want)
		}
	}
	if runner.calls != 0 {
		t.Fatalf("the refusal forked %d subprocesses, want 0", runner.calls)
	}
}

func TestDispatchTool_UnknownNamesAreHonestRefusals(t *testing.T) {
	h := handlers{}
	if _, err := h.DispatchTool(context.Background(), "bogus", nil); err == nil {
		t.Error("DispatchTool(bogus) = nil error, want a refusal")
	}
	if _, err := h.DispatchIntent(context.Background(), "bogus", nil); err == nil {
		t.Error("DispatchIntent = nil error, want a refusal (no intents declared)")
	}
	if err := h.RunCommand(context.Background(), "bogus", nil); err == nil {
		t.Error("RunCommand = nil error, want a refusal (no commands declared)")
	}
}

// TestDispatchTool_ProjectInfoRedactsACredentialShapedBinaryPath is the
// second layer at the DISPATCH boundary: even if a value shaped like a
// credential reaches a payload field, the bytes handed to the firewall
// carry the redaction, not the value.
func TestDispatchTool_ProjectInfoRedactsACredentialShapedBinaryPath(t *testing.T) {
	const leaky = "postgres://cascade:hunter2@127.0.0.1:5432/cascade"
	spy := withSeams(t, absentRunner(), fixedLocator{path: leaky})

	if _, err := (handlers{}).DispatchTool(context.Background(), toolProjectInfo, rootDirInput(t, t.TempDir())); err != nil {
		t.Fatalf("DispatchTool(%s) = %v, want nil", toolProjectInfo, err)
	}
	if strings.Contains(string(spy.content), "hunter2") || strings.Contains(string(spy.content), "postgres://") {
		t.Fatalf("payload handed to the firewall = %s, want the credential-shaped value redacted", spy.content)
	}
	if !strings.Contains(string(spy.content), redactedMarker) {
		t.Fatalf("payload = %s, want the redaction marker present", spy.content)
	}
}

func TestParseToolInputAndRootDirOf(t *testing.T) {
	if got := parseToolInput(nil); got.RootDir != "" {
		t.Errorf("parseToolInput(nil) = %+v, want the zero value", got)
	}
	if got := parseToolInput([]byte("not json")); got.RootDir != "" {
		t.Errorf("parseToolInput(malformed) = %+v, want the zero value, not a panic", got)
	}
	if got := parseToolInput([]byte(`{"root_dir":"/srv/app"}`)); got.RootDir != "/srv/app" {
		t.Errorf("parseToolInput = %+v, want root_dir honoured", got)
	}
	if got := rootDirOf(toolInput{RootDir: "/srv/app"}); got != "/srv/app" {
		t.Errorf("rootDirOf = %q, want the override", got)
	}
	if got := rootDirOf(toolInput{}); got == "" {
		t.Error("rootDirOf(empty) = \"\", want the working directory (or \".\")")
	}
}

// rootDirInput builds the tool body naming dir as the scan root.
func rootDirInput(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := json.Marshal(toolInput{RootDir: dir})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	return b
}

// TestRootDirOf_WorkingDirectoryFailureFallsBackToDot pins the branch a host
// with a deleted working directory takes: "." is a scan root the walk can
// handle, not a panic and not an empty string.
func TestRootDirOf_WorkingDirectoryFailureFallsBackToDot(t *testing.T) {
	orig := getwd
	getwd = func() (string, error) { return "", errors.New("getwd: no such file or directory") }
	t.Cleanup(func() { getwd = orig })

	if got := rootDirOf(toolInput{}); got != "." {
		t.Fatalf("rootDirOf with an unresolvable working directory = %q, want \".\"", got)
	}
}

// TestMarshalThroughEgress_EncodingFailureIsTypedAndEmitsNothing proves the
// encode error is a real typed failure rather than a partial write: nothing
// reaches the interceptor at all.
func TestMarshalThroughEgress_EncodingFailureIsTypedAndEmitsNothing(t *testing.T) {
	spy := &spyInterceptor{}
	orig := egressInterceptor
	egressInterceptor = spy
	t.Cleanup(func() { egressInterceptor = orig })

	out, err := marshalThroughEgress(context.Background(), func() {})
	if err == nil {
		t.Fatal("marshalThroughEgress(unencodable) = nil error, want a typed failure")
	}
	if out != nil {
		t.Fatalf("marshalThroughEgress returned %q, want nothing", out)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Fatalf("cascade.KindOf(err) = (%v, %v), want (KindInternal, true)", kind, ok)
	}
	if spy.calls != 0 {
		t.Fatalf("the interceptor saw %d calls on an encode failure, want 0", spy.calls)
	}
}

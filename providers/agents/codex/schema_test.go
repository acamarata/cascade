package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// schemaDir is the committed revision-1 schema.
const schemaDir = "protocol/1/schema"

// serverRequestMethods reads the ServerRequest method enum.
func serverRequestMethods(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(testdataPath(t, filepath.Join(schemaDir, "ServerRequest.json")))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		OneOf []struct {
			Properties struct {
				Method struct {
					Enum []string `json:"enum"`
				} `json:"method"`
			} `json:"properties"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range s.OneOf {
		out = append(out, v.Properties.Method.Enum...)
	}
	return out
}

// validate checks result against a committed response schema: required
// keys present, enum-constrained string properties in their enum.
func validate(t *testing.T, file string, result json.RawMessage) error {
	t.Helper()
	b, err := os.ReadFile(testdataPath(t, filepath.Join(schemaDir, file)))
	if err != nil {
		return err
	}
	var s struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(result, &got); err != nil {
		return err
	}
	for _, k := range s.Required {
		if got[k] == nil {
			return fmt.Errorf("%s: missing %s", file, k)
		}
	}
	for k, v := range got {
		p, ok := s.Properties[k]
		if !ok {
			return fmt.Errorf("%s: unknown property %s", file, k)
		}
		var str string
		if len(p.Enum) > 0 && (json.Unmarshal(v, &str) != nil || !slices.Contains(p.Enum, str)) {
			return fmt.Errorf("%s: %s=%s outside its enum", file, k, v)
		}
	}
	return nil
}

// answerTable maps each schema server request to its response schema; ""
// means a JSON-RPC error response.
var answerTable = map[string]string{
	"item/commandExecution/requestApproval": "CommandExecutionRequestApprovalResponse.json",
	"item/fileChange/requestApproval":       "FileChangeRequestApprovalResponse.json",
	"item/permissions/requestApproval":      "PermissionsRequestApprovalResponse.json",
	"execCommandApproval":                   "ExecCommandApprovalResponse.json",
	"applyPatchApproval":                    "ApplyPatchApprovalResponse.json",
	"item/tool/requestUserInput":            "",
	"mcpServer/elicitation/request":         "",
	"item/tool/call":                        "",
	"account/chatgptAuthTokens/refresh":     "",
	"attestation/generate":                  "",
}

const permParams = `{"threadId":"t","turnId":"0","itemId":"i","permissions":{"fileSystem":{"write":["/w/out"]},"network":{"hosts":["example.com"]}}}`

func TestCodexEveryServerRequestAnswered(t *testing.T) {
	methods := serverRequestMethods(t)
	if len(methods) != 10 {
		t.Fatalf("schema names %d server requests, want 10", len(methods))
	}
	for _, m := range methods {
		if _, ok := answerTable[m]; !ok {
			t.Errorf("server request %s has no row in the answer table", m)
		}
	}
	k := newKit(t)
	k.useSession(beforeTurnCompleted(loadSession(t), requestLines(methods)...))
	d := k.driver()
	go func() {
		for req := range d.ApprovalRequests() {
			_ = d.ResolveApproval(req.ID, "bridge-token")
		}
	}()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collect(testCtx(t), res.JobID); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for i, m := range append(slices.Clone(methods), "x/unknown/request") {
		ans := k.answersTo(fmt.Sprint(100 + i))
		if len(ans) != 1 {
			t.Errorf("%s answered %d times, want once", m, len(ans))
			continue
		}
		file, known := answerTable[m]
		if !known || file == "" {
			if ans[0]["error"] == nil {
				t.Errorf("%s: want a JSON-RPC error, got %s", m, ans[0]["result"])
			}
			continue
		}
		if err := validate(t, file, ans[0]["result"]); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	evs, _ := d.Events(res.JobID)
	if !slices.ContainsFunc(evs, func(e provider.AgentEvent) bool { return e.Native.Type == "x/unknown/request" }) {
		t.Fatal("the unknown server request was not surfaced as a NativeEvent")
	}
}

// requestLines raises every method (plus one unknown) as a server request
// with rpc ids from 100.
func requestLines(methods []string) []sessionLine {
	var extra []sessionLine
	for i, m := range append(slices.Clone(methods), "x/unknown/request") {
		params := `{"threadId":"t","turnId":"0","itemId":"i"}`
		if m == "item/permissions/requestApproval" {
			params = permParams
		}
		extra = append(extra, approvalLine(350, fmt.Sprint(100+i), m, params))
	}
	return extra
}

func TestCodexPermissionsResponseShape(t *testing.T) {
	method := "item/permissions/requestApproval"
	file := "PermissionsRequestApprovalResponse.json"
	for _, c := range []struct{ decision, want string }{
		{decisionAccept, `{"permissions":{"fileSystem":{"write":["/w/out"]},"network":{"hosts":["example.com"]}},"scope":"turn"}`},
		{decisionDecline, `{"permissions":{},"scope":"turn"}`},
		{decisionCancel, `{"permissions":{},"scope":"turn"}`},
	} {
		b, _ := json.Marshal(responseFor(method, json.RawMessage(permParams), c.decision))
		if string(b) != c.want {
			t.Errorf("%s: %s, want %s", c.decision, b, c.want)
		}
		if err := validate(t, file, b); err != nil {
			t.Error(err)
		}
	}
	b, _ := json.Marshal(responseFor(method, json.RawMessage(`{"permissions":"all"}`), decisionAccept))
	if string(b) != `{"permissions":{},"scope":"turn"}` {
		t.Fatalf("a non-object request granted %s", b)
	}
	g := grantOf(json.RawMessage(permParams))
	if !slices.Equal(g.Paths, []string{"/w/out"}) || !slices.Equal(g.Hosts, []string{"example.com"}) {
		t.Fatalf("grant = %+v", g)
	}
}

func TestCodexApprovalDecisionsClosed(t *testing.T) {
	closed := []string{"accept", "decline", "cancel", "approved", "denied", "abort"}
	forbidden := []string{"acceptForSession", "acceptWithExecpolicyAmendment", "approved_for_session", "applyNetworkPolicyAmendment", `"session"`}
	for m := range approvalKinds {
		for _, dec := range []string{decisionAccept, decisionDecline, decisionCancel} {
			b, _ := json.Marshal(responseFor(m, json.RawMessage(permParams), dec))
			for _, f := range forbidden {
				if strings.Contains(string(b), f) {
					t.Errorf("%s/%s wrote %s", m, dec, b)
				}
			}
			var r struct{ Decision *string }
			_ = json.Unmarshal(b, &r)
			if r.Decision != nil && !slices.Contains(closed, *r.Decision) {
				t.Errorf("%s/%s decision %q outside the closed set", m, dec, *r.Decision)
			}
			if file := answerTable[m]; file != "" {
				if err := validate(t, file, b); err != nil {
					t.Error(err)
				}
			}
		}
	}
}

func TestCodexSandboxAndApprovalParams(t *testing.T) {
	golden := map[string]string{
		"":                          `{"approvalPolicy":"on-request","approvalsReviewer":"user","cwd":"/w","sandbox":"read-only"}`,
		provider.ToolPolicyNone:     `{"approvalPolicy":"on-request","approvalsReviewer":"user","cwd":"/w","sandbox":"read-only"}`,
		provider.ToolPolicyReadonly: `{"approvalPolicy":"on-request","approvalsReviewer":"user","cwd":"/w","sandbox":"read-only"}`,
		provider.ToolPolicyAll:      `{"approvalPolicy":"on-request","approvalsReviewer":"user","cwd":"/w","sandbox":"workspace-write"}`,
	}
	for policy, want := range golden {
		b, _ := json.Marshal(threadStartParams(provider.AgentJobSpec{WorktreeRoot: "/w", ToolPolicy: policy}))
		if string(b) != want {
			t.Errorf("policy %q: %s, want %s", policy, b, want)
		}
		if strings.Contains(string(b), "danger-full-access") || strings.Contains(string(b), `"never"`) {
			t.Errorf("policy %q reached a forbidden value", policy)
		}
	}
	k := newKit(t)
	for _, p := range []string{"danger-full-access", "never", "write"} {
		spec := k.spec()
		spec.ToolPolicy = p
		if _, err := k.driver().Spawn(testCtx(t), spec); err != provider.ErrInvalidToolPolicy {
			t.Errorf("ToolPolicy %q = %v, want ErrInvalidToolPolicy", p, err)
		}
	}
	spec := k.spec()
	spec.ToolPolicy = provider.ToolPolicyReadonly
	if _, _, err := k.run(spec); err != nil {
		t.Fatal(err)
	}
	for _, m := range k.written() {
		if string(m["method"]) == `"thread/start"` && !strings.Contains(string(m["params"]), `"sandbox":"read-only"`) {
			t.Fatalf("thread/start wrote %s", m["params"])
		}
	}
}

func TestCodexModelEffortParams(t *testing.T) {
	if _, ok := threadStartParams(provider.AgentJobSpec{})["model"]; ok {
		t.Fatal("an empty model was sent")
	}
	if _, ok := turnStartParams(provider.AgentJobSpec{}, "t")["effort"]; ok {
		t.Fatal("an empty effort was sent")
	}
	k := newKit(t)
	spec := k.spec()
	spec.Model, spec.Effort = "gpt-5.5-codex", "high"
	if _, _, err := k.run(spec); err != nil {
		t.Fatal(err)
	}
	var sawModel, sawEffort bool
	for _, m := range k.written() {
		switch string(m["method"]) {
		case `"thread/start"`:
			sawModel = strings.Contains(string(m["params"]), `"model":"gpt-5.5-codex"`)
		case `"turn/start"`:
			sawEffort = strings.Contains(string(m["params"]), `"effort":"high"`)
		}
	}
	if !sawModel || !sawEffort {
		t.Fatalf("model sent %v, effort sent %v", sawModel, sawEffort)
	}
	spec.Effort = "turbo"
	if _, err := k.driver().Spawn(testCtx(t), spec); err != ErrInvalidEffort {
		t.Fatalf("Effort turbo = %v", err)
	}
}

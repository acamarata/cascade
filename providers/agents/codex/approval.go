package codex

// Purpose: answer every App Server server request exactly once: approvals
// are surfaced and wait for ResolveApproval, a timeout or a cancel; the
// rest get a JSON-RPC error.
// Inputs: server request frames; ResolveApproval calls.
// Outputs: ApprovalRequests; one response frame per request.
// Constraints: the driver never answers an approval on its own; the only
// decisions written are accept, decline and cancel (approved, denied and
// abort in the legacy ReviewDecision shape); a permissions refusal is an
// empty profile; nothing persists a network or exec policy.
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	decisionAccept  = "accept"
	decisionDecline = "decline"
	decisionCancel  = "cancel"
	approvalBuffer  = 64
	scopeTurn       = "turn"
	codeRefused     = -32000
	codeNoMethod    = -32601
)

// approvalKinds maps each approval server request to its ApprovalRequest kind.
var approvalKinds = map[string]string{
	"item/commandExecution/requestApproval": provider.ApprovalKindCommand,
	"execCommandApproval":                   provider.ApprovalKindCommand,
	"item/fileChange/requestApproval":       provider.ApprovalKindFileChange,
	"applyPatchApproval":                    provider.ApprovalKindFileChange,
	"item/permissions/requestApproval":      provider.ApprovalKindPermissions,
}

// refusedRequests are answered with a JSON-RPC error: no user input, no
// dynamic tools, never a credential or an attestation.
var refusedRequests = map[string]string{
	"item/tool/requestUserInput":        "user input is not available to this client",
	"mcpServer/elicitation/request":     "elicitation is not available to this client",
	"item/tool/call":                    "dynamic tools are not available to this client",
	"account/chatgptAuthTokens/refresh": "this client never supplies credentials",
	"attestation/generate":              "this client never supplies attestation",
}

// legacyDecisions maps a decision to the ReviewDecision value of the
// legacy execCommandApproval and applyPatchApproval responses.
var legacyDecisions = map[string]string{decisionAccept: "approved", decisionDecline: "denied", decisionCancel: "abort"}

// pendingApproval is one surfaced, unanswered approval.
type pendingApproval struct {
	key    string
	job    *job
	rpcID  json.RawMessage
	method string
	params json.RawMessage
	timer  *time.Timer
}

// serverRequest records the request as a NativeEvent and answers or
// surfaces it.
func (d *Driver) serverRequest(j *job, f frame, filtered []byte) error {
	if err := j.native(provider.DriverEventStatus, f.Method, "", filtered); err != nil {
		return err
	}
	if kind, ok := approvalKinds[f.Method]; ok {
		return d.surface(j, f, filtered, kind)
	}
	code, msg := codeNoMethod, "method not supported by this client"
	if m, ok := refusedRequests[f.Method]; ok {
		code, msg = codeRefused, m
	}
	line, err := encodeError(f.ID, code, msg)
	if err != nil {
		return err
	}
	return j.write(line)
}

// surface registers the approval, arms its timeout and publishes it. A
// full channel is a denial: the approval is never left unanswered.
func (d *Driver) surface(j *job, f frame, filtered []byte, kind string) error {
	key := string(j.id) + ":" + string(f.ID)
	pa := &pendingApproval{key: key, job: j, rpcID: f.ID, method: f.Method, params: f.Params}
	d.mu.Lock()
	if _, dup := d.pend[key]; dup {
		d.mu.Unlock()
		return malformed("server request id collides with a pending approval")
	}
	d.pend[key] = pa
	pa.timer = time.AfterFunc(d.cfg.Defaults.ApprovalTimeout, func() { _ = d.settle(key, false, ErrApprovalTimeout) })
	d.mu.Unlock()
	req := provider.ApprovalRequest{ID: key, JobID: j.id, Prompt: promptOf(f.Method, filtered), DataClass: j.tier, Kind: kind}
	if kind == provider.ApprovalKindPermissions {
		req.Grant = grantOf(f.Params)
	}
	select {
	case d.approve <- req:
	default:
		go func() { _ = d.settle(key, false, ErrApprovalDenied) }()
	}
	return nil
}

// promptOf is the method and the filtered params, for the human reviewer.
func promptOf(method string, filtered []byte) string {
	var w struct {
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(filtered, &w) != nil || len(w.Params) == 0 {
		return method
	}
	return method + " " + string(w.Params)
}

// grantOf reads the requested permission profile into a PermissionGrant.
func grantOf(params json.RawMessage) *provider.PermissionGrant {
	var p struct {
		Permissions struct {
			FileSystem map[string]json.RawMessage `json:"fileSystem"`
			Network    struct {
				Hosts []string `json:"hosts"`
			} `json:"network"`
		} `json:"permissions"`
	}
	_ = json.Unmarshal(params, &p)
	g := &provider.PermissionGrant{Paths: []string{}, Hosts: append([]string{}, p.Permissions.Network.Hosts...)}
	for _, raw := range p.Permissions.FileSystem {
		var paths []string
		if json.Unmarshal(raw, &paths) == nil {
			g.Paths = append(g.Paths, paths...)
		}
	}
	return g
}

// ResolveApproval answers accept for a non-empty token passed by the
// approval bridge; an empty token is a denial: decline, interrupt, cancel.
func (d *Driver) ResolveApproval(id string, token string) error {
	if token == "" {
		return d.settle(id, false, ErrApprovalDenied)
	}
	return d.settle(id, true, nil)
}

// settle answers one pending approval once; a refusal stops the job.
func (d *Driver) settle(key string, accept bool, cause error) error {
	d.mu.Lock()
	pa, ok := d.pend[key]
	if ok {
		delete(d.pend, key)
		pa.timer.Stop()
	}
	d.mu.Unlock()
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "codex: no pending approval %q", key)
	}
	if accept {
		return pa.answer(decisionAccept)
	}
	err := pa.answer(decisionDecline)
	d.stop(pa.job, cause, decisionCancel)
	return err
}

// answerPending answers every pending approval of j with decision.
func (d *Driver) answerPending(j *job, decision string) {
	for _, pa := range d.takePending(j) {
		_ = pa.answer(decision)
	}
}

// dropPending forgets j's pending approvals once the child is gone.
func (d *Driver) dropPending(j *job) { d.takePending(j) }

func (d *Driver) takePending(j *job) []*pendingApproval {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*pendingApproval
	for k, pa := range d.pend {
		if pa.job == j {
			pa.timer.Stop()
			delete(d.pend, k)
			out = append(out, pa)
		}
	}
	return out
}

// answer writes the one response for this approval.
func (pa *pendingApproval) answer(decision string) error {
	line, err := encodeResult(pa.rpcID, responseFor(pa.method, pa.params, decision))
	if err != nil {
		return err
	}
	return pa.job.write(line)
}

// responseFor is the schema response for method with decision.
func responseFor(method string, params json.RawMessage, decision string) any {
	switch method {
	case "item/permissions/requestApproval":
		granted := json.RawMessage(`{}`)
		if decision == decisionAccept {
			var p struct {
				Permissions json.RawMessage `json:"permissions"`
			}
			if json.Unmarshal(params, &p) == nil && len(p.Permissions) > 0 && p.Permissions[0] == '{' {
				granted = p.Permissions
			}
		}
		return map[string]any{"permissions": granted, "scope": scopeTurn}
	case "execCommandApproval", "applyPatchApproval":
		return map[string]any{"decision": legacyDecisions[decision]}
	}
	return map[string]any{"decision": decision}
}

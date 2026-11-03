package codex

// Purpose: route one decoded frame (response, notification or server
// request) and build the thread/start and turn/start params.
// Inputs: a decoded frame and its filtered bytes.
// Outputs: job state updates, NativeEvents, answers to server requests.
// Constraints: every notification becomes a NativeEvent, mapped or not; a
// response to an id this driver never sent fails ErrForeignFrame.
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	sandboxReadOnly       = "read-only"
	sandboxWorkspaceWrite = "workspace-write"
	approvalOnRequest     = "on-request"
	reviewerUser          = "user"
)

// sandboxFor maps a ToolPolicy: all is workspace-write, anything else is
// read-only. No policy reaches a wider sandbox.
func sandboxFor(policy string) string {
	if policy == provider.ToolPolicyAll {
		return sandboxWorkspaceWrite
	}
	return sandboxReadOnly
}

// threadStartParams is the thread/start request for spec.
func threadStartParams(spec provider.AgentJobSpec) map[string]any {
	p := map[string]any{
		"cwd":               spec.WorktreeRoot,
		"sandbox":           sandboxFor(spec.ToolPolicy),
		"approvalPolicy":    approvalOnRequest,
		"approvalsReviewer": reviewerUser,
	}
	if spec.Model != "" {
		p["model"] = spec.Model
	}
	return p
}

// turnStartParams is the turn/start request for spec on thread.
func turnStartParams(spec provider.AgentJobSpec, thread string) map[string]any {
	p := map[string]any{
		"threadId": thread,
		"input":    []map[string]any{{"type": "text", "text": spec.Prompt}},
	}
	if spec.Effort != "" {
		p["effort"] = spec.Effort
	}
	return p
}

// dispatch routes one frame; done reports the awaited response (want) or,
// with want "", turn/completed.
func (d *Driver) dispatch(j *job, f frame, filtered []byte, want string, res *json.RawMessage) (bool, error) {
	switch f.Kind {
	case frameResponse:
		return j.response(f, want, res)
	case frameRequest:
		return false, d.serverRequest(j, f, filtered)
	case frameNotification:
	}
	return j.notification(f, filtered, want)
}

func (j *job) response(f frame, want string, res *json.RawMessage) (bool, error) {
	key := string(f.ID)
	j.mu.Lock()
	known := j.outstanding[key]
	delete(j.outstanding, key)
	j.mu.Unlock()
	if !known {
		return false, ErrForeignFrame
	}
	if key != want {
		return false, nil // a turn/interrupt answer
	}
	if f.Error != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, ErrServerError, "codex: app server error code %d", f.Error.Code)
	}
	if res != nil {
		*res = f.Result
	}
	return true, nil
}

// notifParams is the subset of notification params the driver maps.
type notifParams struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
	Item struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

func (j *job) notification(f frame, filtered []byte, want string) (bool, error) {
	var p notifParams
	_ = json.Unmarshal(f.Params, &p) // tolerant: unknown shapes map to nothing
	kind, dedup := provider.DriverEventStatus, ""
	switch f.Method {
	case "turn/started":
		if p.Turn.ID != "" {
			j.setTurn(p.Turn.ID)
		}
	case "item/completed":
		dedup = "item/completed:" + p.Item.ID
		if p.Item.ID == "" {
			dedup = ""
		}
		if p.Item.Type == "agentMessage" {
			kind = provider.DriverEventStdout
			j.appendOutput(filtered)
		}
	case "turn/completed":
		kind = provider.DriverEventDone
		j.mu.Lock()
		j.turnStatus = p.Turn.Status
		j.mu.Unlock()
	}
	if err := j.native(kind, f.Method, dedup, filtered); err != nil {
		return false, err
	}
	return f.Method == "turn/completed" && want == "", nil
}

// appendOutput adds the filtered agent message text to the output.
func (j *job) appendOutput(filtered []byte) {
	var w struct {
		Params notifParams `json:"params"`
	}
	if json.Unmarshal(filtered, &w) != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.output.Len() > 0 {
		j.output.WriteByte('\n')
	}
	j.output.WriteString(w.Params.Item.Text)
}

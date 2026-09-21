// Purpose: recall.what's RPC boundary -- WhatParams (the wire shape) and
// RecallWhatHandler, which decodes it and calls RecallWhatService.Query.
//
// Inputs: raw JSON params (recall.what's request), decoded into
// WhatParams then converted to a RecallWhatRequest.
// Outputs: a RecallWhatResponse (WhatResult), or a taxonomy error.
//
// Constraints: WhatParams carries the SAME raw session signals
// context.scope.show's ShowParams does (rpc.go) -- scope is resolved
// server-side from these (recallwhat_scope.go), never trusted from a
// caller-supplied `scope` string alone. There is no tier field on the
// wire at all: D5's fix items 2 and 7 deleted it, so nothing here decodes
// or forwards one.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

package retrieval

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RecallWhatHandler serves recall.what over a Service.
type RecallWhatHandler struct{ svc *RecallWhatService }

// NewRecallWhatHandler returns a Handler serving svc.
func NewRecallWhatHandler(svc *RecallWhatService) *RecallWhatHandler {
	return &RecallWhatHandler{svc: svc}
}

// Register binds recall.what on r (R-21.273).
func (h *RecallWhatHandler) Register(r *rpc.Registry) { r.Register(MethodWhat, h.Query) }

var _ rpc.HandlerFunc = (*RecallWhatHandler)(nil).Query

// WhatParams is recall.what's wire input. Cwd/User/Machine/Branch/Task/
// Session/ExplicitOverrides are exactly context/scope.ResolveInput's own
// fields (scope/rpc.go's ShowParams carries the identical set for
// context.scope.show, D5.1's chosen mechanism): the composition root
// resolves the caller's SessionScope from these, and Scope (when set) is
// checked against the resolution, never honoured on its own.
type WhatParams struct {
	Query             string `json:"query"`
	K                 int    `json:"k,omitempty"`
	Scope             string `json:"scope,omitempty"`
	Entitlement       string `json:"entitlement,omitempty"`
	Cwd               string `json:"cwd,omitempty"`
	User              string `json:"user,omitempty"`
	Machine           string `json:"machine,omitempty"`
	Branch            string `json:"branch,omitempty"`
	Task              string `json:"task,omitempty"`
	Session           string `json:"session,omitempty"`
	ExplicitOverrides string `json:"explicit_overrides,omitempty"`
}

// toRequest converts the wire shape to the service's own request shape.
func (p WhatParams) toRequest() RecallWhatRequest {
	return RecallWhatRequest{
		Query: p.Query, K: p.K, Scope: p.Scope, Entitlement: p.Entitlement,
		ResolveIn: scope.ResolveInput{
			Cwd: p.Cwd, User: p.User, Machine: p.Machine, Branch: p.Branch,
			Task: p.Task, Session: p.Session, ExplicitOverrides: p.ExplicitOverrides,
		},
	}
}

// WhatResult is recall.what's wire output: RecallWhatResponse's own shape.
type WhatResult = RecallWhatResponse

// Query serves recall.what. No vaulted value reaches this boundary: every
// outbound field transited recallwhat_redact.go's egress substitution
// pass before this function's caller ever marshals the result (H/S-16.T1).
func (h *RecallWhatHandler) Query(ctx context.Context, params json.RawMessage) (any, error) {
	if h.svc == nil {
		return nil, cascade.New(cascade.KindUnavailable, "recall.what: no service is configured on this daemon")
	}
	var p WhatParams
	if trimmed := strings.TrimSpace(string(params)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: malformed params", MethodWhat)
		}
	}
	resp, err := h.svc.Query(ctx, p.toRequest())
	if err != nil {
		return nil, err
	}
	return nonNilWhatResult(resp), nil
}

// nonNilWhatResult makes --json emit [] rather than null for empty slices.
func nonNilWhatResult(resp RecallWhatResponse) RecallWhatResponse {
	if resp.Results == nil {
		resp.Results = []RecallWhatResult{}
	}
	if resp.Citations == nil {
		resp.Citations = []citations.Citation{}
	}
	if resp.Legs == nil {
		resp.Legs = []string{}
	}
	return resp
}

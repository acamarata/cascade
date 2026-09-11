// Purpose: the SDK-level model.execute types (R-21.264): ModelRequest,
//   ModelResponse, Selection, SensitivityTier, JobID and the ModelExecutor
//   interface that internal/conductor implements. These are the types that
//   cross the daemon's JSON-RPC wire via client.go's ModelExecute wrapper;
//   ModelProvider (types.go) is the separate, in-process driver contract a
//   concrete provider (anthropic, openai-compat, gemini, ollama) implements
//   underneath the router.
// Inputs: none at this layer - these are data shapes, not behavior.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2).
//   SensitivityTier's zero value MUST read as SensitivityRestricted
//   (R-21.264; 06-FORGE-SPEC.md §5.16's fail-closed rule: "unset, unknown,
//   or unresolvable inherit => restricted") even though the normative
//   ordering local-only > restricted > internal > public ranks
//   SensitivityLocalOnly as textually more restrictive - local-only names
//   an explicit, narrow placement (controller machine only) that must
//   never be a silent default, so it is declared after the zero value.
//   `type JobID string` is declared exactly once, here; internal/conductor
//   aliases it (`type JobID = provider.JobID`, R-21.281) rather than
//   redeclaring it.
// SPORT: pkg.provider.modelprovider-contract/ADD (P1-E10-W3-S19-T1).

package provider

import "context"

// JobID identifies one model.execute job for the lifetime of its dispatch,
// streaming, and eventual job.cancel. Declared once here (R-21.281);
// internal/conductor aliases this type rather than redeclaring it.
type JobID string

// SensitivityTier is the normative data-sensitivity enum a ModelRequest
// carries (06-FORGE-SPEC.md §5.16, ordered most to least restrictive:
// local-only > restricted > internal > public). The zero value is
// SensitivityRestricted, not SensitivityLocalOnly, so an unset field fails
// closed to "restricted" rather than silently to the even-narrower
// local-only placement (see this file's header comment).
type SensitivityTier uint8

// The four SensitivityTier members. Declaration order fixes each member's
// numeric value; sensitivityRank (below) carries the separate normative
// restrictiveness ordering, which does not follow this declaration order.
const (
	// SensitivityRestricted is the zero value and the fail-closed default.
	SensitivityRestricted SensitivityTier = iota
	// SensitivityLocalOnly never leaves the controller machine: no
	// external lane, no bridge, no node dispatch, no sync beyond the
	// owning device. Textually the most restrictive member, but never the
	// default (it requires an explicit caller opt-in).
	SensitivityLocalOnly
	// SensitivityInternal permits normal routing across configured lanes
	// with no public exposure.
	SensitivityInternal
	// SensitivityPublic carries no confidentiality constraint.
	SensitivityPublic
)

// sensitivityNames is indexed by SensitivityTier value; String uses it in
// place of a switch so the exhaustive linter never applies here.
var sensitivityNames = [...]string{"restricted", "local-only", "internal", "public"}

// sensitivityRank carries the normative restrictiveness ordering (local-only
// > restricted > internal > public), independent of each member's
// declaration-order numeric value. Higher ranks are more restrictive.
var sensitivityRank = [...]int{2, 3, 1, 0}

// Valid reports whether t is one of the four declared SensitivityTier
// members.
func (t SensitivityTier) Valid() bool {
	return t <= SensitivityPublic
}

// String returns the tier's stable lowercase-hyphenated name, or
// "invalid-sensitivity-tier" for a value outside the declared set.
func (t SensitivityTier) String() string {
	if !t.Valid() {
		return "invalid-sensitivity-tier"
	}
	return sensitivityNames[t]
}

// MoreRestrictiveThan reports whether t is strictly more restrictive than
// other under the normative ordering local-only > restricted > internal >
// public - the ranking a caller uses to decide whether resolving "inherit"
// or narrowing a tier is ever a widening (a LOOSENING per §5.14, forbidden
// outside an elevation flow). An invalid tier ranks as maximally
// restrictive, so a corrupt value never silently reads as permissive.
func (t SensitivityTier) MoreRestrictiveThan(other SensitivityTier) bool {
	return t.rank() > other.rank()
}

// rank returns t's normative restrictiveness rank (higher = more
// restrictive), defaulting invalid values to the most restrictive rank.
func (t SensitivityTier) rank() int {
	if !t.Valid() {
		return len(sensitivityRank)
	}
	return sensitivityRank[t]
}

// Requirements is the model.execute requirements triple (02-TARGET-
// STRUCTURE.md §key contracts): reasoning depth, minimum context window,
// and whether structured output is required. The task-class -> defaults
// mapping (06-FORGE-SPEC.md §5.16's table) is K/S-22.T4's to own; this
// type only carries the resolved values a caller (or the class table)
// produced.
type Requirements struct {
	// Reasoning names the required reasoning depth: "low", "medium",
	// "high", or "max", per the task-class table. Kept as a plain string
	// rather than a closed Go enum because the table's exact membership is
	// K/S-22.T4's contract, not this ticket's.
	Reasoning string `json:"reasoning"`
	// Context is the minimum context window, in tokens, the selected lane
	// must offer.
	Context int `json:"context"`
	// Structured reports whether the caller requires structured (e.g.
	// JSON-schema-constrained) output.
	Structured bool `json:"structured"`
}

// Policy is the model.execute policy block (02-TARGET-STRUCTURE.md §key
// contracts): the caller-declared constraints the router enforces before
// any lane is chosen.
type Policy struct {
	// ExternalAllowed reports whether a lane outside the controller
	// machine may be selected at all. false forces local-only placement
	// regardless of Sensitivity.
	ExternalAllowed bool `json:"external_allowed"`
}

// Usage reports token counts consumed by one exchange. Either field may be
// zero when a driver cannot report it; zero is never distinguished from
// "not reported" at this layer.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Selection is the router's resolved pick for one ModelRequest: which lane,
// which provider kind, and which model served it. It describes what was
// chosen; it never influences the choice (that is K/S-22.T2's Router).
type Selection struct {
	// LaneID is the router-selected lane identifier.
	LaneID string `json:"lane_id"`
	// Provider is the vendor-neutral provider-kind identifier the lane
	// belongs to (e.g. a registry-defined string such as "anthropic" or
	// "openai-compat") - never a provider-specific Go type.
	Provider string `json:"provider"`
	// Model is the resolved model identifier within that lane.
	Model string `json:"model"`
	// ReasonFlags carries the router's own filter-by-filter provenance
	// tags for this pick (e.g. "cost:cheapest-selected"), in filter
	// order. Added (T0 decision, P1-E11-W3-S22-T2 unblock) so the frozen
	// Router.Select path - not only the SelectExplain companion - can
	// carry an explanation on the value it already returns; a router
	// that does not populate it leaves the zero value (nil), which is a
	// valid "no explanation given" state, never an error.
	ReasonFlags []string `json:"reason_flags,omitempty"`
}

// ModelRequest is the single job-dispatch shape every model.execute caller
// (plugins/pbd, internal/fleet, cmd/cascade's run command) sends over
// pkg/provider/client.go's typed wrapper. It carries no ctx - ctx is always
// the crossing function's first parameter, never a struct field.
type ModelRequest struct {
	// TaskID identifies the caller's job for correlation and journaling.
	TaskID string `json:"task_id"`
	// TaskClass names the 06-FORGE-SPEC.md §5.16 task class (e.g. "chat",
	// "code", "review"). K/S-22.T4 owns the closed set; this type keeps it
	// a plain string rather than redeclaring that enum.
	TaskClass string `json:"task_class"`
	// Inputs is the ordered conversation the model executes over.
	Inputs []ChatMessage `json:"inputs"`
	// Requirements is the requirements triple the router matches against
	// candidate lanes' Capabilities.
	Requirements Requirements `json:"requirements"`
	// Sensitivity is the explicit sensitivity tier for this request. The
	// zero value resolves to SensitivityRestricted, never a permissive
	// default.
	Sensitivity SensitivityTier `json:"sensitivity"`
	// Policy carries the caller-declared routing constraints.
	Policy Policy `json:"policy"`
	// RequiredCapabilities mirrors the R-14.88 tool-capability dimensions
	// this job needs; the router excludes any lane whose Capabilities does
	// not satisfy every field set here.
	RequiredCapabilities RequiredCapabilities `json:"required_capabilities"`
	// FanOut is the leg count for R-21.214 concurrent N-way dispatch: 0
	// or 1 means a single, ordinary dispatch. A caller never sets this
	// above 1 on a leg request itself - internal/conductor's FanOut
	// primitive resets every dispatched leg's copy to exactly 1 (R-21.214
	// "each leg clears ReservationID" and resets FanOut), so a driver
	// that only ever sees FanOut in {0,1} is not a defect.
	FanOut int `json:"fan_out,omitempty"`
	// ReservationID names the AO/S-79.T4 reservation this request was
	// admitted under, when one exists. internal/conductor's FanOut
	// primitive clears this field on every per-leg copy before dispatch
	// (R-21.214), since each leg takes its own reservation rather than
	// inheriting the parent's.
	ReservationID string `json:"reservation_id,omitempty"`
}

// ModelResponse is what a completed (or terminally failed) ModelRequest
// resolves to: the job identity, the router's Selection, the model's
// output, and usage.
type ModelResponse struct {
	// JobID identifies the job for a later JobCancel or streaming
	// correlation.
	JobID JobID `json:"job_id"`
	// Selection is the router's resolved pick for this request.
	Selection Selection `json:"selection"`
	// Output is the model's final text output.
	Output string `json:"output"`
	// Usage reports token counts for this exchange. On a fan-out parent
	// response (Legs non-empty), Usage is the sum of every leg's own
	// Usage (R-21.214 "summed cost") rather than a separately-typed cost
	// record - Usage already carries per-exchange token accounting, and
	// this ticket adds no second, competing cost type for the same
	// concept.
	Usage Usage `json:"usage"`
	// Legs holds one ModelResponse per R-21.214 fan-out leg, in leg-index
	// order, when this response is a fan-out parent. A non-fan-out
	// response leaves Legs nil. A parent response's own Output is always
	// empty and its own JobID identifies the fan-out dispatch itself, not
	// any one leg.
	Legs []ModelResponse `json:"legs,omitempty"`
}

// ModelExecutor executes ModelRequest jobs through the daemon's model.execute
// door. internal/conductor/model.go implements it with real execution
// plumbing; plugins/pbd and internal/fleet never import internal/conductor
// to reach it - they call pkg/provider/client.go's (*Client).ModelExecute,
// the D/S-07.T3 client's typed wrapper for the same JSON-RPC method.
type ModelExecutor interface {
	// Execute runs req to completion (or a terminal error) and returns its
	// ModelResponse. ctx governs cancellation and deadline for the whole
	// job, not just the initial dispatch.
	Execute(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

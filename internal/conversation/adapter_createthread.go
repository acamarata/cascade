package conversation

// Purpose (this file): chat.create_thread's wire boundary. The daemon mints
//   the thread id (NewThreadID) and Store.CreateThread writes the thread
//   row, its §5.16 marker and the creating caller's attribution in ONE
//   transaction, so a new conversation has a stored, marked and attributed
//   destination before its first turn. There is no client-side mint and no
//   chat.set_privacy.
// Inputs: {"privacy_mode"}: REQUIRED, exactly one of local-only, restricted,
//   internal, public. Absent, empty, any other spelling or an extra field
//   is KindInvalidInput and nothing is written. The caller comes only from
//   the transport-verified Caller on ctx, never from params.
// Outputs: {"thread_id"}: the newly minted id, or a typed refusal.
// Constraints: access class owner-view (internal/rpc's conversation access
//   table): the owner CLI creates threads attributed "owner", a principal
//   creates threads attributed to its own project scope, and a call with no
//   Caller is refused before anything is minted. No content transits this
//   method, so there is nothing for the substitution pass to run over.
// SPORT: internal.conversation.adapter (chat.create_thread).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// MethodCreateThread mints a thread id daemon-side and writes the thread
// row, its required marker and its attribution in one transaction.
const MethodCreateThread = "chat.create_thread"

// errCreateNoCaller refuses a create_thread that carries no transport
// identity: there is no scope to attribute the thread to, and absence is
// never an owner caller (internal/principal.CallerFrom).
var errCreateNoCaller = cascade.New(cascade.KindPermissionDenied,
	"conversation: chat.create_thread needs a transport-verified caller")

// createThreadParams is chat.create_thread's wire request shape.
type createThreadParams struct {
	PrivacyMode string `json:"privacy_mode"`
}

// createThreadResult is chat.create_thread's wire response shape.
type createThreadResult struct {
	ThreadID string `json:"thread_id"`
}

// handleCreateThread is chat.create_thread. The caller and the tier are
// both checked before an id is minted, so a refused request writes nothing.
func (a *Adapter) handleCreateThread(ctx context.Context, raw json.RawMessage) (any, error) {
	attribution, err := createAttribution(ctx)
	if err != nil {
		return nil, err
	}
	tier, err := decodeCreateThreadParams(raw)
	if err != nil {
		return nil, err
	}
	if _, err := scopeDB(ctx, a.store, a.clock); err != nil {
		return nil, err
	}
	threadID, err := NewThreadID()
	if err != nil {
		return nil, err
	}
	if err := a.store.CreateThread(ctx, threadID, tier, attribution, a.clock.Now().Unix()); err != nil {
		return nil, err
	}
	return createThreadResult{ThreadID: threadID}, nil
}

// createAttribution resolves the scope a new thread is attributed to from
// the Caller alone: a principal's own project scope, "owner" for the owner
// CLI. A principal without a project scope, and a call with no Caller (or a
// Caller that is neither), is refused.
func createAttribution(ctx context.Context) (string, error) {
	c, ok := principal.CallerFrom(ctx)
	switch {
	case !ok:
		return "", errCreateNoCaller
	case c.Principal != nil:
		if !projectScope(c.Principal.Scope) {
			return "", errScopeDenied
		}
		return c.Principal.Scope, nil
	case c.OwnerCLI:
		return ownerScope, nil
	}
	return "", errCreateNoCaller
}

// decodeCreateThreadParams decodes raw strictly (unknown fields and
// trailing data refused) and parses privacy_mode through the closed
// provider.ParseSensitivityTier. The empty name is refused here rather
// than read as restricted: the mode is required.
func decodeCreateThreadParams(raw json.RawMessage) (provider.SensitivityTier, error) {
	var p createThreadParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, errUnknownTierName("")
	}
	if err := dec.Decode(&p); err != nil {
		return 0, cascade.Wrap(cascade.KindInvalidInput, err, "conversation: chat.create_thread: malformed params")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, cascade.New(cascade.KindInvalidInput, "conversation: chat.create_thread: trailing data after params")
	}
	if p.PrivacyMode == "" {
		return 0, errUnknownTierName("")
	}
	tier, err := provider.ParseSensitivityTier(p.PrivacyMode)
	if err != nil {
		return 0, errUnknownTierName(p.PrivacyMode)
	}
	return tier, nil
}

// errUnknownTierName refuses a privacy_mode outside the four §5.16 names,
// including the empty name where a mode is required.
func errUnknownTierName(name string) error {
	if name == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"%s: privacy_mode is required; expected one of local-only, restricted, internal, public", MethodCreateThread)
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"%s: %q is not a sensitivity tier; expected one of local-only, restricted, internal, public",
		MethodCreateThread, name)
}

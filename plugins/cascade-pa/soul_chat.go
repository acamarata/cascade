package cascadepa

// Purpose: route (c) of the SOUL store's three sanctioned edit routes —
//
//	the chat-mediated `/soul edit <field> <value>` in-session command.
//	G/S-14.T2 shipped the audited store and its memory.soul.show/edit RPC
//	methods; this file is cascade-pa's presenter over that same RPC, never
//	a fourth write path.
//
// Inputs: the raw text a chat turn carries, and the SoulChatClient seam a
//
//	composition root injects.
//
// Outputs: HandleSoulChatCommand's (reply, matched, error) triple, or a
//
//	typed pkg/cascade error for every refusal path.
//
// Constraints: plugins/** may import pkg/** only, never internal/**
//
//	(Art.10.2) — the wire shapes below are this file's own, duplicated
//	from internal/memory's SoulShowResult/SoulEditParams/SoulEditResult
//	rather than imported, matching internal/plugins/cascadepa_wiring.go's
//	documented practice for the identical boundary.
//
// SPORT: plugins/cascade-pa:soul-chat (ADD) — P1-E22-W5-S47-T3.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The two fields a chat-mediated soul edit may target. The SOUL document
// itself has exactly two fields (body, schema — internal/memory/soul.go's
// SoulDocument), so the allowlist is closed at two, not open-ended.
const (
	soulFieldBody   = "body"
	soulFieldSchema = "schema"
)

// soulEditTrigger is the in-chat command's exact leading grammar. A line
// not starting with this text is not this command at all — it is passed
// through to the ordinary chat turn.
const soulEditTrigger = "/soul edit"

// SoulChatDocument is the plugin-local mirror of internal/memory's
// SoulDocument: the two-field shape a chat-mediated edit writes.
type SoulChatDocument struct {
	Body   string
	Schema string
}

// SoulChatView is the plugin-local mirror of SoulShowResult: the document
// as read before an edit, including whether it has diverged (route-b
// conflict) since the last reconcile.
type SoulChatView struct {
	Body     string
	Schema   string
	Version  int
	Diverged bool
}

// SoulChatEditResult is the plugin-local mirror of SoulEditResult: the
// version an edit produced.
type SoulChatEditResult struct {
	Version int
}

// SoulChatClient is the seam cascade-pa's soul-chat handler calls through.
// It never reaches internal/memory directly (Art.10.2); a composition root
// injects a real internal/client-backed implementation via
// SetSoulChatClient, exactly as plugins/cascade-pa/cmd.Client is injected
// for the ordinary chat turn.
type SoulChatClient interface {
	// Show returns the current document, or a pkg/cascade KindNotFound
	// error when none has ever been written.
	Show(ctx context.Context) (SoulChatView, error)
	// Edit applies doc through route (c), the chat-mediated API, over the
	// real memory.soul.edit RPC — the same method route (a) uses; no
	// fourth write path exists.
	Edit(ctx context.Context, doc SoulChatDocument) (SoulChatEditResult, error)
}

// errSoulChatUnconfigured is the no-profile-context refusal: this build
// has never wired a SoulChatClient, so an in-chat soul-edit trigger cannot
// reach the store at all. This mirrors plugins/cascade-pa/cmd/chat.go's
// unconfiguredClient contract exactly — a real, deliberate refusal, never
// a stub standing in for missing behavior.
var errSoulChatUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade chat: /soul edit has no memory store wired into this session; "+
		"start the daemon with `cascade daemon run` and ensure cascade-pa's "+
		"soul-chat client wiring is configured")

// unconfiguredSoulChatClient is the default SoulChatClient before any
// composition root has called SetSoulChatClient.
type unconfiguredSoulChatClient struct{}

func (unconfiguredSoulChatClient) Show(context.Context) (SoulChatView, error) {
	return SoulChatView{}, errSoulChatUnconfigured
}

func (unconfiguredSoulChatClient) Edit(context.Context, SoulChatDocument) (SoulChatEditResult, error) {
	return SoulChatEditResult{}, errSoulChatUnconfigured
}

// soulChatState guards the package-level SoulChatClient seam.
var soulChatState struct {
	mu sync.RWMutex
	c  SoulChatClient
}

// SetSoulChatClient injects the real SoulChatClient. Called exactly once,
// by the composition root (internal/plugins), before any in-chat
// `/soul edit` trigger is handled. Tests call it directly to inject a
// stub.
func SetSoulChatClient(c SoulChatClient) {
	soulChatState.mu.Lock()
	soulChatState.c = c
	soulChatState.mu.Unlock()
}

// activeSoulChatClient returns the configured client, or the unconfigured
// default when none has been set.
func activeSoulChatClient() SoulChatClient {
	soulChatState.mu.RLock()
	defer soulChatState.mu.RUnlock()
	if soulChatState.c == nil {
		return unconfiguredSoulChatClient{}
	}
	return soulChatState.c
}

// errSoulChatParse is the parse-failure refusal: the line began with the
// trigger but did not carry a well-formed "<field> <value>" tail.
var errSoulChatParse = cascade.New(cascade.KindInvalidInput,
	"usage: /soul edit <field> <value>, where <field> is body or schema")

// errSoulChatUnknownField is the unknown-field refusal.
var errSoulChatUnknownField = cascade.New(cascade.KindInvalidInput,
	"unknown soul field, want body or schema")

// parseSoulEditTrigger parses one chat line against the `/soul edit`
// grammar. matched is false when the line does not begin with the
// trigger at all — the caller must treat it as an ordinary chat prompt,
// never as a failed command. matched is true with a non-nil err for a
// line that named this command but was malformed, so the two refusal
// shapes (not-this-command vs malformed-invocation) are never confused.
func parseSoulEditTrigger(text string) (field, value string, matched bool, err error) {
	if !strings.HasPrefix(text, soulEditTrigger) {
		return "", "", false, nil
	}
	rest := text[len(soulEditTrigger):]
	// A near-miss like "/soul editorial ..." shares the trigger's text as
	// a plain prefix; requiring the very next rune to be whitespace (or
	// the string to end here) is what tells "edit" the command from
	// "editorial" the word.
	if rest != "" && !strings.HasPrefix(rest, " ") {
		return "", "", false, nil
	}
	rest = strings.TrimPrefix(rest, " ")
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", true, errSoulChatParse
	}
	return parts[0], parts[1], true, nil
}

// HandleSoulChatCommand is the in-chat `/soul edit` command's whole
// behavior: parse, validate, dispatch to the real memory.soul.edit RPC via
// the injected SoulChatClient, and format the reply. matched is false only
// when text is not this command at all.
func HandleSoulChatCommand(ctx context.Context, text string) (reply string, matched bool, err error) {
	field, value, matched, err := parseSoulEditTrigger(text)
	if !matched {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	if field != soulFieldBody && field != soulFieldSchema {
		return "", true, errSoulChatUnknownField
	}

	client := activeSoulChatClient()
	view, err := client.Show(ctx)
	if err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
		return "", true, err
	}
	doc := SoulChatDocument{Body: view.Body, Schema: view.Schema}
	if field == soulFieldBody {
		doc.Body = value
	} else {
		doc.Schema = value
	}

	result, err := client.Edit(ctx, doc)
	if err != nil {
		return "", true, err
	}
	return formatSoulEditReply(field, result, view.Diverged), true, nil
}

// formatSoulEditReply builds the one-line confirmation, appending an
// inline doctor note when Show reported an unresolved divergence — the
// version-conflict acceptance path. The write still lands (SoulStore's
// own reconcileBeforeWrite records the conflict rather than blocking the
// edit), so the note is informational, not a refusal.
func formatSoulEditReply(field string, result SoulChatEditResult, diverged bool) string {
	reply := fmt.Sprintf("soul %s updated — now version %d", field, result.Version)
	if diverged {
		reply += "\ndoctor note: this document had diverged from an out-of-store edit before this change; both are now recorded in the audit log"
	}
	return reply
}

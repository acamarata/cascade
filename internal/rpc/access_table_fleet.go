package rpc

// Session-addressed fleet verbs are owner-view: a caller principal sees and
// acts on only its own session and its descendants at every depth
// (internal/fleet/sessions ScopeMiddleware and Visibility); hook_event stays
// ingest, bound to the same subtree when a principal sends it.
// fleet.sessions.completion_check stays owner-only: its job_id is not yet
// bound to any session, so a session subtree cannot scope it.
var _ = DeclareAccess("fleet.attention.ack", AccessOwnerView)
var _ = DeclareAccess("fleet.attention.get", AccessOwnerView)
var _ = DeclareAccess("fleet.attention.list", AccessOwnerView)
var _ = DeclareAccess("fleet.journal_replay", AccessOwnerOnly)
var _ = DeclareAccess("fleet.journal_show", AccessOwnerOnly)
var _ = DeclareAccess("fleet.mode.set", AccessOwnerOnly)
var _ = DeclareAccess("fleet.mode.show", AccessOwnerOnly)
var _ = DeclareAccess("fleet.quota.snapshot", AccessOwnerOnly)
var _ = DeclareAccess("fleet.sessions.completion_check", AccessOwnerOnly)
var _ = DeclareAccess("fleet.sessions.hook_event", AccessIngest)
var _ = DeclareAccess("fleet.sessions.list", AccessOwnerView)
var _ = DeclareTopicAccess("fleet.sessions", AccessOwnerView)

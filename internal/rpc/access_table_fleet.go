package rpc

// Interim read classes remain owner-only until their handlers filter by scope.
var _ = DeclareAccess("fleet.attention.ack", AccessOwnerOnly)
var _ = DeclareAccess("fleet.attention.get", AccessOwnerOnly)
var _ = DeclareAccess("fleet.attention.list", AccessOwnerOnly)
var _ = DeclareAccess("fleet.journal_replay", AccessOwnerOnly)
var _ = DeclareAccess("fleet.journal_show", AccessOwnerOnly)
var _ = DeclareAccess("fleet.mode.set", AccessOwnerOnly)
var _ = DeclareAccess("fleet.mode.show", AccessOwnerOnly)
var _ = DeclareAccess("fleet.quota.snapshot", AccessOwnerOnly)
var _ = DeclareAccess("fleet.sessions.completion_check", AccessOwnerOnly)
var _ = DeclareAccess("fleet.sessions.hook_event", AccessIngest)
var _ = DeclareAccess("fleet.sessions.list", AccessOwnerOnly)
var _ = DeclareTopicAccess("fleet.sessions", AccessOwnerOnly)

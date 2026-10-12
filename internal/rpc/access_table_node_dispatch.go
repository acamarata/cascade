package rpc

// Interim read classes remain owner-only until their handlers filter by scope.
var _ = DeclareAccess("node.dispatch.claim", AccessOwnerOnly)
var _ = DeclareAccess("node.dispatch.journal", AccessOwnerOnly)
var _ = DeclareAccess("node.dispatch.report", AccessOwnerOnly)

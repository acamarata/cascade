package rpc

// Memory verbs a caller principal may reach are owner-view: the handlers
// confine a principal to records attributed to its project scope and never
// return the personal kinds (internal/memory/scope_attr.go). The scope is
// written once at memory.remember. Every other memory verb is owner-only.
var _ = DeclareAccess("memory.consolidate", AccessOwnerOnly)
var _ = DeclareAccess("memory.forget", AccessOwnerView)
var _ = DeclareAccess("memory.list", AccessOwnerView)
var _ = DeclareAccess("memory.recall", AccessOwnerView)
var _ = DeclareAccess("memory.remember", AccessOwnerView)
var _ = DeclareAccess("memory.review.act", AccessOwnerOnly)
var _ = DeclareAccess("memory.review.list", AccessOwnerOnly)
var _ = DeclareAccess("memory.soul.edit", AccessOwnerOnly)
var _ = DeclareAccess("memory.soul.export", AccessOwnerOnly)
var _ = DeclareAccess("memory.soul.show", AccessOwnerOnly)

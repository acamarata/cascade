package rpc

// Interim read classes remain owner-only until their handlers filter by scope.
var _ = DeclareAccess("memory.consolidate", AccessOwnerOnly)
var _ = DeclareAccess("memory.forget", AccessOwnerOnly)
var _ = DeclareAccess("memory.list", AccessOwnerOnly)
var _ = DeclareAccess("memory.recall", AccessOwnerOnly)
var _ = DeclareAccess("memory.remember", AccessOwnerOnly)
var _ = DeclareAccess("memory.review.act", AccessOwnerOnly)
var _ = DeclareAccess("memory.review.list", AccessOwnerOnly)
var _ = DeclareAccess("memory.soul.edit", AccessOwnerOnly)
var _ = DeclareAccess("memory.soul.export", AccessOwnerOnly)
var _ = DeclareAccess("memory.soul.show", AccessOwnerOnly)

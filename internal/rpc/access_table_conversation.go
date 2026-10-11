package rpc

// Interim read classes remain owner-only until their handlers filter by scope.
var _ = DeclareAccess("chat.append_turn", AccessOwnerOnly)
var _ = DeclareAccess("chat.get_thread", AccessOwnerOnly)
var _ = DeclareAccess("chat.list_threads", AccessOwnerOnly)
var _ = DeclareAccess("chat.search", AccessOwnerOnly)
var _ = DeclareAccess("chat.threads_list", AccessOwnerOnly)
var _ = DeclareAccess("chat.topics_list", AccessOwnerOnly)

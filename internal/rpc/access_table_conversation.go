package rpc

// Chat verbs are owner-view: the owner reads and writes every thread; a
// principal reads and appends only threads attributed to its own scope
// (internal/conversation/scope_attr.go). chat.create_thread is declared here
// once, ahead of its handler, which must write the attribution with
// attributeThread(tx, ...) inside its own creating transaction.
var _ = DeclareAccess("chat.append_turn", AccessOwnerView)
var _ = DeclareAccess("chat.create_thread", AccessOwnerView)
var _ = DeclareAccess("chat.get_thread", AccessOwnerView)
var _ = DeclareAccess("chat.list_threads", AccessOwnerView)
var _ = DeclareAccess("chat.search", AccessOwnerView)
var _ = DeclareAccess("chat.threads_list", AccessOwnerView)
var _ = DeclareAccess("chat.topics_list", AccessOwnerView)

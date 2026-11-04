package learn

import "github.com/acamarata/cascade/internal/rpc"

var (
	_ = rpc.DeclareAccess("learn.status", rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess("learn.explain", rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess("learn.reset", rpc.AccessOwnerOnly)
)

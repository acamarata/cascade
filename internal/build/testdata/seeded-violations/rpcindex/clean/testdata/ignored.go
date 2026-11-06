package ignored

import rpc "github.com/acamarata/cascade/internal/rpc"

var _ = rpc.DeclareAccess("fixture.bare", rpc.AccessOwnerOnly)

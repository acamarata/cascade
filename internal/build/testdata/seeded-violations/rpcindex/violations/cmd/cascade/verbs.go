package main

import rpc "github.com/acamarata/cascade/internal/rpc"

var class = rpc.AccessOpen
var name = "fixture.good"
var _ = rpc.DeclareAccess("fixture.variable", rpc.AccessOpen)
var _ = rpc.DeclareAccess("fixture.good", rpc.AccessOpen)
var _ = rpc.DeclareAccess("fixture.variable", class)

func wrapper(method string) { rpc.DeclareAccess(method, rpc.AccessOpen) }
func elevated()             { rpc.RegisterElevatedVerb(name, rpc.ElevationRule{Always: true}) }

var _ = rpc.DeclareAccess("fixture.undocumented", rpc.AccessOpen)
var _ = rpc.DeclareAccess("fixture.conflict", rpc.AccessOpen)
var _ = rpc.DeclareAccess("fixture.conflict", rpc.AccessScoped)

func register() { registry.Register("fixture.noaccess", handler) }

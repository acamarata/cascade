//go:build wasip1

// Purpose: the wasip1-only WASM export surface for example-agent-provider.
// This file is compiled ONLY under GOOS=wasip1 (see the build constraint
// above): the raw linear-memory pointer arithmetic below is meaningful
// exclusively inside a wasip1 guest's own address space, and `go vet`'s
// unsafeptr check correctly flags it as suspicious on every host platform
// that lacks that guarantee (darwin/linux/windows). Splitting it out of
// main.go, rather than silencing vet tree-wide, keeps the dispatcher and
// handler logic in main.go compiling and testable on the host while this
// file's real guest ABI still builds under GOOS=wasip1 GOARCH=wasm.
//
// Inputs: ptr/length naming a JSON InvokeEnvelope already written into this
//
//	module's own linear memory by the host.
//
// Outputs: the byte length of the JSON InvokeResult written back at
//
//	respOffset, per the host-ABI runtime's calling convention.
//
// Constraints: wasip1-only; ptr must never be 0 (a zero offset converts to
//
//	a nil Go pointer and panics on dereference — proven by this ticket's own
//	wazero round-trip spike, see the journal for P1-E15-W4-S33-T2).
//
// SPORT: plugins/examples/example-agent-provider (ADD) — P1-E15-W4-S33-T2,
// split out under the vet-portability fix journaled at
// FIX-wasm-example-vet-portability.
package main

import (
	"context"
	"unsafe"
)

// respOffset is the fixed linear-memory address plugin_invoke writes its
// response bytes at, mirroring the host-ABI runtime's own scratch-region
// convention (internal/plugins/wasm/doc.go's wasmRespOffset) so a real host
// integration reusing that convention needs no per-plugin special case.
// reqOffset is never referenced here: the caller supplies the request
// pointer directly as plugin_invoke's argument, wherever it chose to write
// it (never address 0 — see this file's Constraints doc above).
const respOffset = uint32(65536)

// pluginInvoke is the single WASM export the manifest's provides.tools
// entries all route through (R-14.50): ptr/length name the JSON
// InvokeEnvelope already written into this module's own linear memory by
// the host; the response is written back at respOffset and its length
// returned, matching the host-ABI runtime's own calling convention. ptr
// must never be 0 (see respOffset's doc comment) — the host, never this
// guest, chooses where to place the request.
//
// unsafe.Pointer use here is real linear-memory access, not a portability
// bug: a wasip1 guest's own address space IS its linear memory, and this
// file is compiled only under GOOS=wasip1 where that guarantee holds.
//
//go:wasmexport plugin_invoke
func pluginInvoke(ptr, length uint32) uint32 {
	req := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	out := dispatcher.Dispatch(context.Background(), req)
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(respOffset))), len(out))
	copy(dst, out)
	return uint32(len(out))
}

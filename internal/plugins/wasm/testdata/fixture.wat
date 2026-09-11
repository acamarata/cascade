;; fixture.wat -- ABI v1 conformance fixture for internal/plugins/wasm
;; (P1-E15-W4-S32-T1). Hand-authored WebAssembly Text, compiled with the
;; real WABT toolchain (wat2wasm), not self-authored bytes. Imports the
;; seven host functions under module name "env" and re-exports one
;; wrapper per function ("call_<name>"), plus an exported i32 global
;; "cascade_abi_version" carrying the host-ABI version this module was
;; built against, read by loader.go's version handshake before dispatch.
;; Calling convention: (i32 ptr, i32 len) -> i32 (responseLen); caller
;; writes a request envelope at [ptr, ptr+len) before calling, callee
;; writes its response envelope at the runtime-owned response offset.
(module
  (import "env" "host_http" (func $host_http (param i32 i32) (result i32)))
  (import "env" "host_storage" (func $host_storage (param i32 i32) (result i32)))
  (import "env" "host_log" (func $host_log (param i32 i32) (result i32)))
  (import "env" "host_stream" (func $host_stream (param i32 i32) (result i32)))
  (import "env" "host_secretref" (func $host_secretref (param i32 i32) (result i32)))
  (import "env" "host_eventemit" (func $host_eventemit (param i32 i32) (result i32)))
  (import "env" "host_toolregister" (func $host_toolregister (param i32 i32) (result i32)))

  (memory (export "memory") 2)
  (global (export "cascade_abi_version") i32 (i32.const 1))

  (func (export "call_host_http") (param $ptr i32) (param $len i32) (result i32)
    (call $host_http (local.get $ptr) (local.get $len)))
  (func (export "call_host_storage") (param $ptr i32) (param $len i32) (result i32)
    (call $host_storage (local.get $ptr) (local.get $len)))
  (func (export "call_host_log") (param $ptr i32) (param $len i32) (result i32)
    (call $host_log (local.get $ptr) (local.get $len)))
  (func (export "call_host_stream") (param $ptr i32) (param $len i32) (result i32)
    (call $host_stream (local.get $ptr) (local.get $len)))
  (func (export "call_host_secretref") (param $ptr i32) (param $len i32) (result i32)
    (call $host_secretref (local.get $ptr) (local.get $len)))
  (func (export "call_host_eventemit") (param $ptr i32) (param $len i32) (result i32)
    (call $host_eventemit (local.get $ptr) (local.get $len)))
  (func (export "call_host_toolregister") (param $ptr i32) (param $len i32) (result i32)
    (call $host_toolregister (local.get $ptr) (local.get $len)))
)

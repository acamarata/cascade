;; test.wat -- ABI v1 host-function conformance module for the wazero spike
;; adapter (P1-E14-W3-S30-T6). Imports the seven host functions under
;; module name "env" and re-exports one wrapper per function under
;; "call_<name>", so a Go caller can invoke each host function through a
;; real WebAssembly call boundary. Every import and export uses the same
;; (i32 ptr, i32 len) -> i32 (responseLen) calling convention: the caller
;; writes a request envelope into linear memory at [ptr, ptr+len) before
;; calling, and the host function writes its response envelope into the
;; module's memory at the fixed response offset the Go adapter and this
;; module both agree on (see conformance_test.go), returning the response
;; byte length.
(module
  (import "env" "host_http" (func $host_http (param i32 i32) (result i32)))
  (import "env" "host_storage" (func $host_storage (param i32 i32) (result i32)))
  (import "env" "host_log" (func $host_log (param i32 i32) (result i32)))
  (import "env" "host_stream" (func $host_stream (param i32 i32) (result i32)))
  (import "env" "host_secretref" (func $host_secretref (param i32 i32) (result i32)))
  (import "env" "host_eventemit" (func $host_eventemit (param i32 i32) (result i32)))
  (import "env" "host_toolregister" (func $host_toolregister (param i32 i32) (result i32)))

  (memory (export "memory") 2)

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

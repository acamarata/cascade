;; abi_mismatch.wat -- same shape as fixture.wat but with a deliberately
;; wrong "cascade_abi_version" (999), for TestABIVersionMismatch_HardError.
(module
  (import "env" "host_http" (func $host_http (param i32 i32) (result i32)))
  (import "env" "host_storage" (func $host_storage (param i32 i32) (result i32)))
  (import "env" "host_log" (func $host_log (param i32 i32) (result i32)))
  (import "env" "host_stream" (func $host_stream (param i32 i32) (result i32)))
  (import "env" "host_secretref" (func $host_secretref (param i32 i32) (result i32)))
  (import "env" "host_eventemit" (func $host_eventemit (param i32 i32) (result i32)))
  (import "env" "host_toolregister" (func $host_toolregister (param i32 i32) (result i32)))

  (memory (export "memory") 2)
  (global (export "cascade_abi_version") i32 (i32.const 999))

  (func (export "call_host_log") (param $ptr i32) (param $len i32) (result i32)
    (call $host_log (local.get $ptr) (local.get $len)))
)

;; hello.wat — minimal WAT fixture for sandbox tests.
;;
;; Purpose: verify that WasmPlugin can compile and call a trivial export.
;; Exports:
;;   hello() -> i32   returns 42
;;   alloc(size: i32) -> i32   stub allocator (returns 0; tests don't use memory ABI)
;; Constraints: no WASI imports; bare core module so tests pass without WASI linker.

(module
  (memory (export "memory") 1)

  ;; Return the answer to life, the universe and everything.
  (func (export "hello") (result i32)
    i32.const 42
  )

  ;; Minimal bump allocator stub — always returns offset 0 for test purposes.
  ;; Real plugins provide a real allocator; this fixture only tests the call path.
  (func (export "alloc") (param i32) (result i32)
    i32.const 0
  )
)

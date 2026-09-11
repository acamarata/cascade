;; resource.wat -- exercises ResourceLimits: memory.grow (memory ceiling),
;; a host-call loop (fuel budget), and an infinite loop (wall-clock cap).
;; Real WABT-compiled binary, not self-authored bytes.
(module
  (import "env" "host_log" (func $host_log (param i32 i32) (result i32)))
  (memory (export "memory") 2 4096)
  (global (export "cascade_abi_version") i32 (i32.const 1))

  (func (export "grow_memory") (param $delta i32) (result i32)
    (memory.grow (local.get $delta)))

  (func (export "spin_fuel") (param $n i32)
    (local $i i32)
    (local.set $i (i32.const 0))
    (block $done
      (loop $loop
        (br_if $done (i32.ge_s (local.get $i) (local.get $n)))
        (drop (call $host_log (i32.const 0) (i32.const 0)))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $loop))))

  (func (export "spin_forever")
    (loop $loop
      (br $loop)))
)

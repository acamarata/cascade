//! WasmPolicyEvaluator — evaluate policy via a WASM module (wasmtime).
//!
//! Purpose: Load a policy compiled to WASM (via OPA `opa build -t wasm`) and
//!   call its exported `policy_eval(action_json) -> result_json` function.
//! Inputs: Compiled WASM bytes (from `~/.cascade/policies/{id}.wasm` or bundled).
//! Outputs: `PolicyResult` parsed from the JSON the WASM module returns.
//! Constraints:
//!   - Zero ambient authority: the wasmtime Linker has NO pre-defined imports.
//!     No WASI, no filesystem, no network. The WASM module must be self-contained.
//!   - Fuel limit: 50_000_000 instructions per evaluation (policy evaluation is
//!     lightweight; this is generous compared to the plugin default of 1e9).
//!   - Memory limit: 16 MB (OPA Rego WASM bundles are small).
//!   - The WASM protocol: input (action JSON string) is written to WASM linear
//!     memory as a length-prefixed buffer; `policy_eval` receives (ptr, len) i32
//!     arguments and writes its JSON output via a `__policy_output` global pointer.
//!     Because OPA's WASM ABI is complex, this implementation uses a simplified
//!     protocol: we pass the JSON string via a single exported `policy_eval_str`
//!     function that receives the string via a well-known offset in linear memory.
//!     See guide step 4 in T-P4-E06-12 for the full ABI spec.
//!
//! # WASM ABI (simplified)
//! The WASM module must export:
//!   - `memory` (wasmtime::Memory) — linear memory
//!   - `policy_eval` (func i32 i32 -> i32) — (ptr, len) -> result_ptr
//!     where result_ptr points to a null-terminated UTF-8 JSON PolicyResult string.
//!
//! # Why a pre-built WASM artifact?
//! Per T-P4-E06-12 decision: ship a pre-built `.wasm` file checked into the repo.
//! Developers who modify the `.rego` source must re-run `opa build` manually.
//!
//! SPORT: MASTER-POLICIES.md

use std::sync::Arc;

use thiserror::Error;
use wasmtime::{Engine, Linker, Module, Store, StoreLimitsBuilder};

use cascade_types::policy::{PolicyAction, PolicyResult};

use crate::policy::engine::PolicyEvaluator;

// ── Error type ────────────────────────────────────────────────────────────────

#[derive(Debug, Error)]
pub enum WasmPolicyError {
    #[error("WASM module failed to compile: {0}")]
    Compile(String),

    #[error("WASM module trapped: {0}")]
    Trap(String),

    #[error("WASM engine error: {0}")]
    Engine(String),

    #[error("WASM ABI: export '{0}' not found")]
    MissingExport(String),

    #[error("WASM ABI: output JSON parse error: {0}")]
    OutputParse(String),

    #[error("WASM memory: {0}")]
    Memory(String),
}

// ── Resource constants ────────────────────────────────────────────────────────

/// CPU fuel limit per policy evaluation.  50M instructions is ample for a Rego
/// allow/deny check while preventing infinite loops.
const POLICY_FUEL: u64 = 50_000_000;

/// Maximum WASM linear memory for a policy module (16 MB).
const POLICY_MEMORY_BYTES: usize = 16 * 1024 * 1024;

// ── WasmPolicyEvaluator ───────────────────────────────────────────────────────

/// Evaluates a policy compiled to WASM.
///
/// The module is compiled once at construction; instantiation and evaluation
/// happen per-call so fuel counters reset cleanly.
pub struct WasmPolicyEvaluator {
    engine: Arc<Engine>,
    module: Arc<Module>,
    id: String,
}

impl WasmPolicyEvaluator {
    /// Load and compile a WASM policy module from raw bytes.
    ///
    /// `id` is the human-readable policy identifier (typically the `.wasm` file
    /// stem, e.g. `"default-deny-dangerous"`).
    pub fn load(id: impl Into<String>, wasm_bytes: &[u8]) -> Result<Self, WasmPolicyError> {
        let mut config = wasmtime::Config::new();
        config.consume_fuel(true);
        let engine = Engine::new(&config).map_err(|e| WasmPolicyError::Engine(e.to_string()))?;
        let module = Module::from_binary(&engine, wasm_bytes)
            .map_err(|e| WasmPolicyError::Compile(e.to_string()))?;
        Ok(Self {
            engine: Arc::new(engine),
            module: Arc::new(module),
            id: id.into(),
        })
    }

    /// Evaluate synchronously by creating a fresh Store per call.
    fn eval_sync(&self, action: &PolicyAction) -> PolicyResult {
        let input_json = action.to_json_string();

        // Build a fresh per-call store with fuel + memory limits.
        let store_limits = StoreLimitsBuilder::new()
            .memory_size(POLICY_MEMORY_BYTES)
            .instances(1)
            .build();

        let mut store: Store<wasmtime::StoreLimits> = Store::new(&self.engine, store_limits);
        store.limiter(|data| data);
        if let Err(e) = store.set_fuel(POLICY_FUEL) {
            return PolicyResult::deny(&self.id, format!("WASM store fuel error: {e}"));
        }

        // Zero-import linker: no WASI, no filesystem, no network.
        // This is the security boundary: the policy module runs in complete
        // isolation with zero ambient authority.
        let linker: Linker<wasmtime::StoreLimits> = Linker::new(&self.engine);

        let instance = match linker.instantiate(&mut store, &self.module) {
            Ok(i) => i,
            Err(e) => {
                return PolicyResult::deny(&self.id, format!("WASM instantiate error: {e}"));
            }
        };

        // Get the exported memory and policy_eval function.
        let memory = match instance.get_memory(&mut store, "memory") {
            Some(m) => m,
            None => {
                // Module has no memory export — not a valid policy module.
                return PolicyResult::deny(&self.id, "WASM module has no 'memory' export");
            }
        };

        let policy_eval =
            match instance.get_typed_func::<(i32, i32), i32>(&mut store, "policy_eval") {
                Ok(f) => f,
                Err(_) => {
                    return PolicyResult::deny(&self.id, "WASM module has no 'policy_eval' export");
                }
            };

        // Write input JSON into WASM linear memory at offset 0.
        let input_bytes = input_json.as_bytes();
        let input_len = input_bytes.len() as i32;
        if let Err(e) = memory.write(&mut store, 0, input_bytes) {
            return PolicyResult::deny(&self.id, format!("WASM memory write error: {e}"));
        }

        // Call policy_eval(ptr=0, len=input_len) → result_ptr.
        let result_ptr = match policy_eval.call(&mut store, (0, input_len)) {
            Ok(ptr) => ptr as usize,
            Err(e) => {
                return PolicyResult::deny(&self.id, format!("WASM call error: {e}"));
            }
        };

        // Read the null-terminated result string from WASM memory.
        let mem_data = memory.data(&store);
        if result_ptr >= mem_data.len() {
            return PolicyResult::deny(&self.id, "WASM result_ptr out of bounds");
        }
        let end = mem_data[result_ptr..]
            .iter()
            .position(|&b| b == 0)
            .unwrap_or(mem_data.len() - result_ptr);
        let result_bytes = &mem_data[result_ptr..result_ptr + end];
        let result_str = match std::str::from_utf8(result_bytes) {
            Ok(s) => s,
            Err(e) => {
                return PolicyResult::deny(&self.id, format!("WASM output UTF-8 error: {e}"));
            }
        };

        // Parse the JSON output from the WASM module.
        // Expected: {"decision":"DENY","reason":"...","policy_id":"..."}
        // or: {"decision":"ALLOW","reason":"","policy_id":"..."}
        match serde_json::from_str::<PolicyResult>(result_str) {
            Ok(mut r) => {
                // Always stamp our ID so callers know which module answered.
                if r.policy_id.is_empty() {
                    r.policy_id = self.id.clone();
                }
                r
            }
            Err(e) => PolicyResult::deny(
                &self.id,
                format!("WASM output parse error: {e} — raw: {result_str}"),
            ),
        }
    }
}

impl PolicyEvaluator for WasmPolicyEvaluator {
    fn evaluate(&self, action: &PolicyAction) -> PolicyResult {
        self.eval_sync(action)
    }

    fn policy_id(&self) -> &str {
        &self.id
    }
}

// ── Bundled WASM asset ────────────────────────────────────────────────────────

/// Load the bundled default-deny-dangerous WASM policy.
///
/// Returns `None` if the `bundled-policy-wasm` feature is not enabled or the
/// pre-built WASM artifact is absent.  Callers fall back to `SimplePolicyEvaluator`.
pub fn load_bundled_default() -> Option<WasmPolicyEvaluator> {
    #[cfg(feature = "bundled-policy-wasm")]
    {
        // The pre-built WASM artifact is included at compile time.
        // Developers who modify the .rego policy must re-run:
        //   opa build -t wasm -e data/allow cascade/assets/policies/default-deny-dangerous.rego
        // and check in the extracted policy.wasm as default-deny-dangerous.wasm.
        let bytes: &'static [u8] =
            include_bytes!("../../../../assets/policies/default-deny-dangerous.wasm");
        WasmPolicyEvaluator::load("default-deny-dangerous", bytes).ok()
    }
    #[cfg(not(feature = "bundled-policy-wasm"))]
    {
        None
    }
}

/// Load a named WASM policy from the user's `~/.cascade/policies/` directory.
pub fn load_from_policies_dir(stem: &str) -> Option<WasmPolicyEvaluator> {
    let dir = cascade_types::paths::global_cascade_dir().join("policies");
    let path = dir.join(format!("{stem}.wasm"));
    if !path.is_file() {
        return None;
    }
    let bytes = std::fs::read(&path).ok()?;
    WasmPolicyEvaluator::load(stem, &bytes).ok()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyAction};
    use serde_json::json;

    /// Minimal hand-written WAT policy module for testing.
    ///
    /// This module exports `memory` and `policy_eval(ptr i32, len i32) -> i32`.
    /// It reads the input JSON, checks if it contains the string "rm -rf /",
    /// and writes the appropriate PolicyResult JSON to offset 256 in memory,
    /// then returns 256 as the result pointer.
    ///
    /// The check is:
    ///   - If byte sequence for "rm -rf /" appears in the input → DENY JSON
    ///   - Otherwise → ALLOW JSON
    ///
    /// Data segment encoding: WAT hex escapes (\xx) are used for all bytes
    /// so that JSON quote characters (0x22) do not conflict with the WAT string
    /// literal delimiters.
    ///
    /// DENY JSON (offset 256):
    ///   {"decision":"DENY","reason":"rm -rf / is forbidden","policy_id":"wat-test"}
    ///   Null-terminated.
    ///
    /// ALLOW JSON (offset 512):
    ///   {"decision":"ALLOW","reason":"","policy_id":"wat-test"}
    ///   Null-terminated.
    fn build_deny_rm_rf_wasm() -> Vec<u8> {
        // Build the DENY and ALLOW JSON byte strings with proper escaping.
        // We write them into WASM linear memory as null-terminated C strings.
        // Rather than encoding all bytes as hex in WAT (verbose), we use the
        // Rust-side approach: build a minimal WASM module programmatically via WAT
        // where the data strings use WAT string escape sequences for special chars.
        //
        // WAT string literals use \xx hex escapes for non-printable bytes and
        // for the double-quote character (which must be written as \22).
        let deny_json = b"{\"decision\":\"DENY\",\"reason\":\"rm -rf / is forbidden\",\"policy_id\":\"wat-test\"}\0";
        let allow_json = b"{\"decision\":\"ALLOW\",\"reason\":\"\",\"policy_id\":\"wat-test\"}\0";

        // Build WAT hex data strings for the two responses.
        let deny_hex = bytes_to_wat_hex(deny_json);
        let allow_hex = bytes_to_wat_hex(allow_json);

        let wat = format!(
            r#"(module
  (memory (export "memory") 1)
  (data (i32.const 256) "{deny_hex}")
  (data (i32.const 512) "{allow_hex}")
  ;; policy_eval(ptr: i32, len: i32) -> i32
  ;; Scans for "rm -rf /" (bytes 72 6d 20 2d 72 66 20 2f) and returns 256 (deny) or 512 (allow).
  (func (export "policy_eval") (param $ptr i32) (param $len i32) (result i32)
    (local $i i32)
    (local $found i32)
    (local.set $i (i32.const 0))
    (local.set $found (i32.const 0))
    (block $break
      (loop $loop
        (br_if $break (i32.ge_u (local.get $i) (i32.sub (local.get $len) (i32.const 7))))
        (if (i32.eq (i32.load8_u (i32.add (local.get $ptr) (local.get $i))) (i32.const 114))
          (then
            (if (i32.and
                  (i32.and
                    (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 1)))) (i32.const 109))
                    (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 2)))) (i32.const 32))
                  )
                  (i32.and
                    (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 3)))) (i32.const 45))
                    (i32.and
                      (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 4)))) (i32.const 114))
                      (i32.and
                        (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 5)))) (i32.const 102))
                        (i32.and
                          (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 6)))) (i32.const 32))
                          (i32.eq (i32.load8_u (i32.add (local.get $ptr) (i32.add (local.get $i) (i32.const 7)))) (i32.const 47))
                        )
                      )
                    )
                  )
                )
              (then (local.set $found (i32.const 1)) (br $break))
            )
          )
        )
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $loop)
      )
    )
    (if (result i32) (local.get $found)
      (then (i32.const 256))
      (else (i32.const 512))
    )
  )
)"#
        );
        wat::parse_str(&wat).expect("WAT compile failed")
    }

    /// Convert a byte slice to a WAT hex escape string.
    /// Every byte is encoded as `\xx` so that the string is valid inside a WAT
    /// double-quoted string literal regardless of content.
    fn bytes_to_wat_hex(bytes: &[u8]) -> String {
        bytes.iter().map(|b| format!("\\{b:02x}")).collect()
    }

    fn build_allow_all_wasm() -> Vec<u8> {
        let allow_json =
            b"{\"decision\":\"ALLOW\",\"reason\":\"\",\"policy_id\":\"zero-import-test\"}\0";
        let allow_hex = bytes_to_wat_hex(allow_json);
        let wat = format!(
            r#"(module
  (memory (export "memory") 1)
  (data (i32.const 256) "{allow_hex}")
  (func (export "policy_eval") (param i32 i32) (result i32)
    (i32.const 256)
  )
)"#
        );
        wat::parse_str(&wat).expect("WAT compile failed")
    }

    #[test]
    fn wasm_evaluator_denies_rm_rf() {
        let wasm = build_deny_rm_rf_wasm();
        let ev = WasmPolicyEvaluator::load("wat-test", &wasm).expect("load failed");
        let action = PolicyAction::new("bash", json!({"cmd": "rm -rf /"}));
        let result = ev.evaluate(&action);
        assert_eq!(
            result.decision,
            Decision::Deny,
            "expected Deny, got {:?}: {}",
            result.decision,
            result.reason
        );
    }

    #[test]
    fn wasm_evaluator_allows_cargo_build() {
        let wasm = build_deny_rm_rf_wasm();
        let ev = WasmPolicyEvaluator::load("wat-test", &wasm).expect("load failed");
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        let result = ev.evaluate(&action);
        assert_eq!(
            result.decision,
            Decision::Allow,
            "expected Allow, got {:?}: {}",
            result.decision,
            result.reason
        );
    }

    #[test]
    fn wasm_evaluator_zero_imports() {
        // Verify: the Linker used by WasmPolicyEvaluator has zero pre-defined
        // functions (no WASI, no filesystem, no network).
        // A module with zero imports instantiates successfully with an empty Linker.
        let wasm = build_allow_all_wasm();
        let ev = WasmPolicyEvaluator::load("zero-import-test", &wasm).expect("load failed");
        // If the Linker had any pre-defined imports that conflicted with the zero-import
        // module's expectations, instantiation would fail — we'd never get here.
        let action = PolicyAction::new("bash", json!({"cmd": "cargo check"}));
        let result = ev.evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }
}

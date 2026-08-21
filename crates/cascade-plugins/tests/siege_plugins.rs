//! SIEGE security vector tests for the cascade-plugins WASM sandbox.
//!
//! Purpose: adversarial verification of 7 security vectors per T-P4-E03-21 spec.
//! Vectors:
//!   1. Path traversal via manifest entry_wasm or fs preopen dirs
//!   2. Fuel exhaustion: infinite-loop WAT → trapped, host healthy
//!   3. Memory bomb: huge alloc → limited by StoreLimits
//!   4. Malicious manifest: permission escalation, oversized fields, duplicate IDs
//!   5. ABI abuse: bad ptr/len from guest → error not UB
//!   6. Hot-reload race: swap during call
//!   7. Plugin output injection: oversized/malformed JSON result → bounded typed error
//!
//! Constraints:
//!   - No external toolchain: all WASM built from inline WAT.
//!   - Tests must be deterministic (no sleeps on fast paths).
//!
//! SPORT: cascade-plugins / siege tests (T-P4-E03-21)

use cascade_plugins::{
    manifest::{JsonPermissions, ManifestError, PluginJsonManifest, PluginType},
    runtime::{PluginError, PluginSandbox, PLUGIN_FUEL_LIMIT},
};
use wasmtime::Val;

// ── WAT helpers ───────────────────────────────────────────────────────────────

/// Tight infinite-counting loop — guaranteed to exhaust any finite fuel.
fn infinite_loop_wat() -> Vec<u8> {
    wat::parse_str(
        r#"(module
          (func (export "spin") (result i32)
            (local $i i32)
            (loop $L
              local.get $i
              i32.const 1
              i32.add
              local.set $i
              i32.const 1
              br_if $L
            )
            local.get $i
          )
        )"#,
    )
    .expect("infinite_loop WAT")
}

/// Module that tries to grow memory to 20_000 pages (1.25 GB) — exceeds sandbox cap.
fn memory_bomb_wat() -> Vec<u8> {
    wat::parse_str(
        r#"(module
          (memory 1)
          (func (export "bomb") (result i32)
            i32.const 20000
            memory.grow
          )
        )"#,
    )
    .expect("memory_bomb WAT")
}

/// Module that returns an out-of-bounds output pointer via the packed i64 ABI.
fn bad_ptr_wat() -> Vec<u8> {
    wat::parse_str(
        r#"(module
          (memory (export "memory") 1)
          (func (export "alloc") (param i32) (result i32) i32.const 0)
          ;; cascade_plugin_call: ignore input, return ptr=0xFFFF_FFFF, len=0xFFFF_FFFF
          (func (export "cascade_plugin_call") (param i32 i32) (result i64)
            i64.const 0xFFFFFFFFFFFFFFFF
          )
        )"#,
    )
    .expect("bad_ptr WAT")
}

/// Module that returns valid-looking but huge len via the packed i64 ABI.
fn oversized_output_wat() -> Vec<u8> {
    wat::parse_str(
        r#"(module
          (memory (export "memory") 1)
          (func (export "alloc") (param i32) (result i32) i32.const 0)
          ;; cascade_plugin_call: return ptr=0, len=huge (beyond memory size)
          (func (export "cascade_plugin_call") (param i32 i32) (result i64)
            ;; ptr=0, len=0x0FFF_FFFF (268 MB, way beyond 1-page = 64 KB memory)
            i64.const 0x000000000FFFFFFF
          )
        )"#,
    )
    .expect("oversized_output WAT")
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 1 — Path traversal via manifest entry_wasm
// ═══════════════════════════════════════════════════════════════════════════════

/// Manifest with `entry_wasm = "../../etc/passwd"` must be rejected at validation time.
/// This is a path-traversal attempt — the relative `..` would escape the plugin directory
/// when joined in the loader (plugin_dir.join("../../etc/passwd")).
#[test]
fn siege_v1_entry_wasm_path_traversal_rejected() {
    let json = r#"{
        "id": "com.evil.traversal",
        "name": "Traversal",
        "version": "1.0.0",
        "description": "path traversal attempt",
        "author": "attacker",
        "license": "MIT",
        "entry_wasm": "../../etc/passwd",
        "kind": "ChatTool",
        "min_cascade_version": ">=0.1.0"
    }"#;

    let err =
        PluginJsonManifest::from_json_str(json).expect_err("entry_wasm with .. must be rejected");

    assert!(
        matches!(err, ManifestError::PathTraversal { .. }),
        "expected PathTraversal error, got: {err:?}"
    );
    let msg = err.to_string();
    assert!(
        msg.contains("path-traversal"),
        "error message must mention path-traversal: {msg}"
    );
}

/// Manifest with `entry_wasm = "../sibling/plugin.wasm"` (single level up) must also be rejected.
#[test]
fn siege_v1_entry_wasm_single_parent_traversal_rejected() {
    let json = r#"{
        "id": "com.evil.sibling",
        "name": "SiblingTraversal",
        "version": "1.0.0",
        "description": "single parent dir traversal",
        "author": "attacker",
        "license": "MIT",
        "entry_wasm": "../other-plugin/plugin.wasm",
        "kind": "Agent",
        "min_cascade_version": ">=0.1.0"
    }"#;

    let err = PluginJsonManifest::from_json_str(json)
        .expect_err("entry_wasm with single .. must be rejected");

    assert!(
        matches!(err, ManifestError::PathTraversal { .. }),
        "expected PathTraversal, got: {err:?}"
    );
}

/// Manifest with `fs.path = "../../etc"` must be rejected — preopen path traversal.
#[test]
fn siege_v1_fs_preopen_path_traversal_rejected() {
    let json = r#"{
        "id": "com.evil.fsesc",
        "name": "FsEscape",
        "version": "1.0.0",
        "description": "fs preopen traversal",
        "author": "attacker",
        "license": "MIT",
        "entry_wasm": "plugin.wasm",
        "kind": "DataSource",
        "min_cascade_version": ">=0.1.0",
        "permissions": {
            "fs": [{"path": "../../etc", "read": true, "write": false}]
        }
    }"#;

    let err =
        PluginJsonManifest::from_json_str(json).expect_err("fs path with .. must be rejected");

    assert!(
        matches!(err, ManifestError::PathTraversal { .. }),
        "expected PathTraversal, got: {err:?}"
    );
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 2 — Fuel exhaustion: infinite loop is trapped, host stays healthy
// ═══════════════════════════════════════════════════════════════════════════════

/// An infinite-loop WASM module with fuel=1 must return ResourceExhausted, not hang the host.
#[tokio::test]
async fn siege_v2_fuel_exhaustion_trapped_host_healthy() {
    let wasm = infinite_loop_wat();

    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("sandbox");
    let plugin = sandbox
        .load_with_permissions(&wasm, "evil-loop", JsonPermissions::default(), Some(1))
        .expect("compile");

    let result = plugin.call("spin", &[]).await;
    assert!(result.is_err(), "infinite loop with fuel=1 must fail");

    let err = result.unwrap_err();
    assert!(
        matches!(err, PluginError::ResourceExhausted { .. }),
        "must be ResourceExhausted, not a host panic: {err:?}"
    );

    // Host is healthy: can create another sandbox and call again without issue.
    let hello_wasm = wat::parse_str("(module (func (export \"hi\") (result i32) i32.const 1))")
        .expect("hello WAT");
    let sb2 = PluginSandbox::new(PluginType::Chunker).expect("sandbox 2");
    let p2 = sb2
        .load_with_permissions(
            &hello_wasm,
            "healthy-guest",
            JsonPermissions::default(),
            None,
        )
        .expect("load healthy");
    let ok = p2.call("hi", &[]).await.expect("healthy call");
    assert_eq!(ok.len(), 1);
    match &ok[0] {
        Val::I32(1) => { /* expected */ }
        other => panic!("host is healthy — expected i32(1), got {other:?}"),
    }
}

/// Infinite loop with default fuel (1e9) must also eventually be caught
/// (the loop is actually bounded by the i32 counter wrapping — but the WAT
/// is designed to run forever, so 1e9 instructions is reached before the
/// integer wraps). Verify ResourceExhausted is returned.
#[tokio::test]
async fn siege_v2_infinite_loop_exhausts_default_fuel() {
    let wasm = infinite_loop_wat();
    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("sandbox");
    let plugin = sandbox
        .load_with_permissions(
            &wasm,
            "loop-default",
            JsonPermissions::default(),
            Some(PLUGIN_FUEL_LIMIT),
        )
        .expect("compile");

    let result = plugin.call("spin", &[]).await;
    assert!(
        matches!(result, Err(PluginError::ResourceExhausted { .. })),
        "infinite loop must exhaust default fuel: {result:?}"
    );
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 3 — Memory bomb: huge alloc is limited by StoreLimits
// ═══════════════════════════════════════════════════════════════════════════════

/// A module that attempts to grow memory to 1.25 GB must be caught by the
/// sandbox StoreLimits (default small=64 MB). The call must return an error,
/// not crash the host process.
#[tokio::test]
async fn siege_v3_memory_bomb_limited_not_host_crash() {
    let wasm = memory_bomb_wat();
    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("sandbox");
    let plugin = sandbox
        .load_with_permissions(&wasm, "mem-bomb", JsonPermissions::default(), None)
        .expect("compile");

    let result = plugin.call("bomb", &[]).await;
    // memory.grow failing returns -1 (i32) — the call itself succeeds but the
    // grow is denied. Either the call returns Ok([Val::I32(-1)]) or an error.
    match result {
        Ok(vals) => {
            // memory.grow returned -1 meaning the grow was denied by the limiter.
            assert_eq!(vals.len(), 1);
            match &vals[0] {
                Val::I32(-1) => { /* expected: grow denied */ }
                other => panic!(
                    "memory.grow must return -1 (denied) when limiter blocks it, got {other:?}"
                ),
            }
        }
        Err(e) => {
            // Also acceptable: sandbox error on OOM.
            let msg = e.to_string();
            assert!(
                !msg.contains("SIGSEGV") && !msg.contains("illegal instruction"),
                "memory bomb must not crash the host: {msg}"
            );
        }
    }
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 4 — Malicious manifest: permission escalation, oversized fields
// ═══════════════════════════════════════════════════════════════════════════════

/// Permission escalation: unknown capability token "net:*" must be rejected.
#[test]
fn siege_v4_capability_escalation_net_star_rejected() {
    let json = r#"{
        "id": "com.evil.netstar",
        "name": "NetStar",
        "version": "1.0.0",
        "description": "escalation attempt",
        "author": "attacker",
        "license": "MIT",
        "entry_wasm": "plugin.wasm",
        "kind": "Agent",
        "min_cascade_version": ">=0.1.0",
        "capabilities": ["net:*"]
    }"#;

    let err =
        PluginJsonManifest::from_json_str(json).expect_err("net:* capability must be rejected");

    assert!(
        matches!(err, ManifestError::UnknownCapability { .. }),
        "expected UnknownCapability, got: {err:?}"
    );
    let msg = err.to_string();
    assert!(
        msg.contains("net:*"),
        "error must name the bad token: {msg}"
    );
}

/// Permission escalation: "fs:/" capability must be rejected.
#[test]
fn siege_v4_capability_escalation_fs_root_rejected() {
    let json = r#"{
        "id": "com.evil.fsroot",
        "name": "FsRoot",
        "version": "1.0.0",
        "description": "fs escalation",
        "author": "attacker",
        "license": "MIT",
        "entry_wasm": "plugin.wasm",
        "kind": "Provider",
        "min_cascade_version": ">=0.1.0",
        "capabilities": ["fs:/"]
    }"#;

    let err =
        PluginJsonManifest::from_json_str(json).expect_err("fs:/ capability must be rejected");

    assert!(matches!(err, ManifestError::UnknownCapability { .. }));
}

/// Net permission with wildcard host `"*"` must be rejected.
#[test]
fn siege_v4_net_wildcard_host_rejected() {
    let json = r#"{
        "id": "com.evil.wildcard",
        "name": "Wildcard",
        "version": "1.0.0",
        "description": "wildcard net",
        "author": "attacker",
        "license": "MIT",
        "entry_wasm": "plugin.wasm",
        "kind": "ChatTool",
        "min_cascade_version": ">=0.1.0",
        "permissions": {
            "net": [{"host": "*"}]
        }
    }"#;

    let err =
        PluginJsonManifest::from_json_str(json).expect_err("wildcard net host must be rejected");

    assert!(
        matches!(err, ManifestError::Validation { .. }),
        "expected Validation error, got: {err:?}"
    );
}

/// Duplicate plugin IDs: registering the same ID twice replaces the old slot
/// (no silent collision — the registry atomically replaces and drains).
#[test]
fn siege_v4_duplicate_plugin_id_replaces_not_collides() {
    use cascade_plugins::loader::{PluginLoader, VerificationPolicy};
    use cascade_plugins::plugin_registry::PluginRegistry;
    use std::fs;
    use tempfile::TempDir;

    let tmp = TempDir::new().unwrap();

    fn make_plugin(dir: &std::path::Path, id: &str) {
        let pdir = dir.join(id);
        fs::create_dir_all(&pdir).unwrap();
        let manifest = format!(
            r#"{{"id":"{id}","name":"x","version":"1.0.0","description":"x",
            "author":"a","license":"MIT","entry_wasm":"{id}.wasm",
            "kind":"ChatTool","min_cascade_version":">=0.1.0"}}"#
        );
        fs::write(pdir.join("plugin.json"), manifest).unwrap();
        let wasm = wat::parse_str("(module)").unwrap();
        fs::write(pdir.join(format!("{id}.wasm")), wasm).unwrap();
    }

    make_plugin(tmp.path(), "com.example.dup");

    let registry = PluginRegistry::new();
    let (mut loaded, _) = PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());
    assert_eq!(loaded.len(), 1);
    registry.register(loaded.remove(0));
    assert_eq!(registry.len(), 1);

    // Register again with same ID — must replace, not grow the registry.
    let (mut loaded2, _) = PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());
    registry.register(loaded2.remove(0));
    assert_eq!(
        registry.len(),
        1,
        "duplicate ID must replace, registry stays at 1"
    );
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 5 — ABI abuse: bad ptr/len from guest → error not UB
// ═══════════════════════════════════════════════════════════════════════════════

/// Guest returns packed ptr=0xFFFF_FFFF, len=0xFFFF_FFFF in the i64 ABI.
/// Host must return a typed PluginError::Trap (out-of-bounds), NOT a host crash.
#[tokio::test]
async fn siege_v5_bad_ptr_out_of_bounds_returns_error_not_ub() {
    let wasm = bad_ptr_wat();
    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("sandbox");
    let plugin = sandbox
        .load_with_permissions(&wasm, "bad-ptr-plugin", JsonPermissions::default(), None)
        .expect("compile");

    let result = plugin
        .call_export_json("cascade_plugin_call", &serde_json::json!({}))
        .await;

    // Must be an error — out-of-bounds memory access.
    assert!(
        result.is_err(),
        "bad ptr must produce an error, not a panic: {result:?}"
    );

    let err = result.unwrap_err();
    let msg = err.to_string();
    // Must be a typed error (Trap or Serialize), never a host crash.
    assert!(
        matches!(err, PluginError::Trap(_) | PluginError::Serialize(_)),
        "must be a typed plugin error, not a host fault: {err:?} msg={msg}"
    );
    assert!(
        !msg.contains("SIGSEGV") && !msg.contains("illegal instruction"),
        "must not crash host: {msg}"
    );
}

/// Guest returns a valid ptr (0) but len way beyond linear memory size.
/// Host must detect the out-of-bounds slice and return a typed error.
#[tokio::test]
async fn siege_v5_oversized_len_returns_error_not_ub() {
    let wasm = oversized_output_wat();
    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("sandbox");
    let plugin = sandbox
        .load_with_permissions(
            &wasm,
            "oversized-len-plugin",
            JsonPermissions::default(),
            None,
        )
        .expect("compile");

    let result = plugin
        .call_export_json("cascade_plugin_call", &serde_json::json!({}))
        .await;

    assert!(
        result.is_err(),
        "oversized len must produce an error: {result:?}"
    );
    let err = result.unwrap_err();
    assert!(
        matches!(err, PluginError::Trap(_) | PluginError::Serialize(_)),
        "must be a typed plugin error: {err:?}"
    );
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 6 — Hot-reload race: swap during call
// ═══════════════════════════════════════════════════════════════════════════════

/// Simulate a hot-reload swap while a previous call's Arc is still held.
/// The drain_arc helper must time out gracefully (not deadlock) when there
/// are live clones, and the registry must end up in the new state.
#[test]
fn siege_v6_hot_reload_drain_arc_timeout_safe() {
    use cascade_plugins::hot_reload::drain_arc;
    use std::sync::Arc;
    use std::time::Duration;

    let data = Arc::new(42u64);

    // Simulate in-flight call holding a clone.
    let _in_flight = Arc::clone(&data);

    // drain_arc with a very short timeout must time out, not deadlock.
    let completed = drain_arc(&data, Duration::from_millis(50));
    assert!(
        !completed,
        "drain must time out when there is a live clone (simulated in-flight call)"
    );

    // After releasing the in-flight clone, a new drain must succeed.
    drop(_in_flight);
    let completed_after = drain_arc(&data, Duration::from_millis(100));
    assert!(
        completed_after,
        "drain must succeed once the in-flight clone is released"
    );
}

/// Reload replaces the registry slot atomically; concurrent list() during swap
/// must not panic or return a corrupted entry.
#[test]
fn siege_v6_registry_reload_atomic_no_panic() {
    use cascade_plugins::loader::{PluginLoader, VerificationPolicy};
    use cascade_plugins::plugin_registry::PluginRegistry;
    use std::fs;
    use std::sync::Arc;
    use tempfile::TempDir;

    let tmp = TempDir::new().unwrap();
    let plugin_dir = tmp.path().join("com.example.reload");
    fs::create_dir_all(&plugin_dir).unwrap();
    let manifest = r#"{"id":"com.example.reload","name":"x","version":"1.0.0",
        "description":"x","author":"a","license":"MIT","entry_wasm":"com.example.reload.wasm",
        "kind":"ChatTool","min_cascade_version":">=0.1.0"}"#;
    fs::write(plugin_dir.join("plugin.json"), manifest).unwrap();
    let wasm = wat::parse_str("(module)").unwrap();
    fs::write(plugin_dir.join("com.example.reload.wasm"), &wasm).unwrap();

    let registry = Arc::new(PluginRegistry::new());
    let (mut loaded, _) = PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());
    registry.register(loaded.remove(0));

    // Concurrently call list() while performing a reload — must not panic.
    let registry_clone = Arc::clone(&registry);
    let list_thread = std::thread::spawn(move || {
        for _ in 0..100 {
            let _ = registry_clone.list();
        }
    });

    // Reload the same plugin 5 times.
    for _ in 0..5 {
        let (mut reloaded, _) =
            PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());
        if let Some(p) = reloaded.pop() {
            registry.reload_plugin(p);
        }
    }

    list_thread.join().expect("list thread must not panic");
    assert_eq!(
        registry.len(),
        1,
        "registry must have exactly one slot after reloads"
    );
}

// ═══════════════════════════════════════════════════════════════════════════════
// SIEGE VECTOR 7 — Plugin output injection: malformed/oversized JSON → bounded typed error
// ═══════════════════════════════════════════════════════════════════════════════

/// A guest that returns valid ptr/len pointing to non-UTF-8 / non-JSON bytes
/// must produce a typed Serialize error, not a host crash.
#[tokio::test]
async fn siege_v7_malformed_json_output_returns_typed_error() {
    // Module that returns a tiny buffer with invalid JSON bytes (0xFF 0xFF = invalid UTF-8).
    let wasm = wat::parse_str(
        r#"(module
          (memory (export "memory") 1)
          ;; Write 0xFF 0xFF at offset 0.
          (data (i32.const 0) "\ff\ff")
          (func (export "alloc") (param i32) (result i32) i32.const 64)
          ;; cascade_plugin_call: return ptr=0, len=2 (the 0xFF 0xFF bytes)
          (func (export "cascade_plugin_call") (param i32 i32) (result i64)
            ;; packed: ptr=0 << 32 | len=2
            i64.const 2
          )
        )"#,
    )
    .expect("malformed output WAT");

    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("sandbox");
    let plugin = sandbox
        .load_with_permissions(&wasm, "malformed-output", JsonPermissions::default(), None)
        .expect("compile");

    let result = plugin
        .call_export_json("cascade_plugin_call", &serde_json::json!({}))
        .await;

    assert!(
        result.is_err(),
        "malformed JSON output must produce an error: {result:?}"
    );
    let err = result.unwrap_err();
    assert!(
        matches!(err, PluginError::Serialize(_) | PluginError::Trap(_)),
        "must be Serialize or Trap error, not a host crash: {err:?}"
    );
}

// ═══════════════════════════════════════════════════════════════════════════════
// DQA: Manifest schema validator vs validator parity checks
// ═══════════════════════════════════════════════════════════════════════════════

/// All ALLOWED_CAPABILITIES tokens are accepted; none-of-the-above is rejected.
/// This documents the complete capability vocabulary and asserts exhaustiveness.
#[test]
fn dqa_allowed_capability_vocabulary_complete() {
    let allowed = &["fs_read", "fs_write", "net_outbound", "env_read"];
    for cap in allowed {
        let json = format!(
            r#"{{"id":"com.ex.t","name":"x","version":"1.0.0","description":"x",
            "author":"a","license":"MIT","entry_wasm":"p.wasm","kind":"ChatTool",
            "min_cascade_version":">=0.1.0","capabilities":["{cap}"]}}"#
        );
        PluginJsonManifest::from_json_str(&json)
            .unwrap_or_else(|e| panic!("allowed capability {cap} should parse: {e}"));
    }

    // Verify an unlisted token is rejected.
    let bad_json = r#"{"id":"com.ex.t","name":"x","version":"1.0.0","description":"x",
        "author":"a","license":"MIT","entry_wasm":"p.wasm","kind":"ChatTool",
        "min_cascade_version":">=0.1.0","capabilities":["admin_shell"]}"#;
    let err =
        PluginJsonManifest::from_json_str(bad_json).expect_err("admin_shell must be rejected");
    assert!(matches!(err, ManifestError::UnknownCapability { .. }));
}

/// Error taxonomy: every ManifestError variant must produce a non-empty, human-readable message.
#[test]
fn dqa_manifest_error_messages_are_non_empty() {
    let errors: Vec<ManifestError> = vec![
        ManifestError::MissingSchemaVersion,
        ManifestError::SchemaVersionTooNew {
            name: "test".into(),
            required: 99,
            supported: 1,
        },
        ManifestError::TomlParse("bad toml".into()),
        ManifestError::JsonParse("bad json".into()),
        ManifestError::InvalidId {
            id: "bad".into(),
            reason: "test".into(),
        },
        ManifestError::InvalidVersion {
            id: "com.ex.t".into(),
            reason: "test".into(),
        },
        ManifestError::AbsoluteWasmPath {
            id: "com.ex.t".into(),
            path: "/usr/lib/plugin.wasm".into(),
            reason: "test".into(),
        },
        ManifestError::PathTraversal {
            id: "com.ex.t".into(),
            path: "../../etc/passwd".into(),
            reason: "test".into(),
        },
        ManifestError::InvalidMinCascadeVersion {
            id: "com.ex.t".into(),
            reason: "test".into(),
        },
        ManifestError::UnknownCapability {
            id: "com.ex.t".into(),
            capability: "bad_cap".into(),
            allowed: "fs_read, net_outbound".into(),
        },
        ManifestError::Validation {
            id: "com.ex.t".into(),
            reason: "test validation".into(),
        },
        ManifestError::Io {
            path: "/tmp/plugin.json".into(),
            reason: "test io".into(),
        },
    ];

    for err in errors {
        let msg = err.to_string();
        assert!(
            !msg.is_empty(),
            "ManifestError must have a non-empty message: {err:?}"
        );
        // All messages should be at least marginally informative (> 10 chars).
        assert!(
            msg.len() > 10,
            "ManifestError message too short ({} chars): {msg}",
            msg.len()
        );
    }
}

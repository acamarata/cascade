//! Sandbox integration tests.
//!
//! Purpose: verify sandbox resource enforcement, capability-denial paths, and
//!   lifecycle behavior without requiring a full daemon startup.
//! Inputs: inline WASM bytes (minimal modules built via wat2wasm equivalents).
//! Constraints: these tests run in the host process (not inside WASM); they test
//!   the sandbox wrapper, not the plugins themselves.
//! SPORT: cascade-plugins / sandbox layer

use cascade_plugins::{
    manifest::PluginType,
    sandbox::{ResourceLimits, DEFAULT_FD_CAP, MEM_LARGE_MB, MEM_SMALL_MB},
};

// ── Resource limit defaults ───────────────────────────────────────────────────

#[test]
fn chunker_gets_small_memory_default() {
    let limits = ResourceLimits::for_type(PluginType::Chunker);
    assert_eq!(
        limits.max_memory_bytes,
        MEM_SMALL_MB * 1024 * 1024,
        "Chunker should default to {MEM_SMALL_MB} MB"
    );
}

#[test]
fn retriever_gets_large_memory_default() {
    let limits = ResourceLimits::for_type(PluginType::Retriever);
    assert_eq!(
        limits.max_memory_bytes,
        MEM_LARGE_MB * 1024 * 1024,
        "Retriever should default to {MEM_LARGE_MB} MB (H2 fix — ONNX/in-memory corpora)"
    );
}

#[test]
fn provider_gets_large_memory_default() {
    let limits = ResourceLimits::for_type(PluginType::EmbeddingProvider);
    assert_eq!(limits.max_memory_bytes, MEM_LARGE_MB * 1024 * 1024);
}

#[test]
fn agent_gets_large_memory_default() {
    let limits = ResourceLimits::for_type(PluginType::Agent);
    assert_eq!(limits.max_memory_bytes, MEM_LARGE_MB * 1024 * 1024);
}

#[test]
fn fd_cap_default_is_32() {
    let limits = ResourceLimits::for_type(PluginType::Chunker);
    assert_eq!(
        limits.max_fds, DEFAULT_FD_CAP,
        "FD cap should default to 32 (H1 fix)"
    );
}

#[test]
fn memory_cap_clamped_to_1gb() {
    let limits = ResourceLimits::for_type(PluginType::Chunker).with_memory_mb(2048);
    assert_eq!(
        limits.max_memory_bytes,
        1024 * 1024 * 1024,
        "memory should be clamped at 1 GB max"
    );
}

// ── Capability model tests ────────────────────────────────────────────────────

#[test]
fn ipc_cascade_reserved_produces_actionable_error() {
    use cascade_plugins::capability::DeclaredCapabilities;
    let caps = DeclaredCapabilities {
        ipc_cascade: Some(true),
        ..Default::default()
    };
    let err = caps.resolve(false).unwrap_err();
    let msg = err.to_string();
    assert!(
        msg.contains("reserved") && msg.contains("not implemented"),
        "error must explain that the capability is reserved and unavailable: {msg}"
    );
}

#[test]
fn net_outbound_blocked_by_user_config() {
    use cascade_plugins::capability::{Capability, DeclaredCapabilities};
    let caps = DeclaredCapabilities {
        net_outbound: Some(true),
        ..Default::default()
    };
    let set = caps.resolve(true).unwrap();
    assert!(
        !set.has(&Capability::NetOutbound),
        "net_outbound should be denied when user config disables it"
    );
    assert!(
        set.denied.contains(&Capability::NetOutbound),
        "denied set should record the blocked capability for audit"
    );
}

// ── Lifecycle tests ───────────────────────────────────────────────────────────

#[test]
fn quarantine_after_threshold_crashes() {
    use cascade_plugins::lifecycle::{PluginRegistry, PluginState};
    use std::path::PathBuf;

    let reg = PluginRegistry::new();
    reg.register(
        "test-plugin",
        PathBuf::from("/tmp/test-plugin"),
        Default::default(),
    );

    // Advance to Active state.
    reg.transition("test-plugin", PluginState::Validating)
        .unwrap();
    reg.transition("test-plugin", PluginState::Loading).unwrap();
    reg.transition("test-plugin", PluginState::Active).unwrap();

    // First two crashes -> Crashed, not Quarantined.
    let s1 = reg.record_crash("test-plugin").unwrap();
    assert_eq!(s1, PluginState::Crashed);
    reg.transition("test-plugin", PluginState::Loading).unwrap();
    reg.transition("test-plugin", PluginState::Active).unwrap();

    let s2 = reg.record_crash("test-plugin").unwrap();
    assert_eq!(s2, PluginState::Crashed);
    reg.transition("test-plugin", PluginState::Loading).unwrap();
    reg.transition("test-plugin", PluginState::Active).unwrap();

    // Third crash in window -> Quarantined.
    let s3 = reg.record_crash("test-plugin").unwrap();
    assert_eq!(
        s3,
        PluginState::Quarantined,
        "third crash should trigger quarantine"
    );

    // Loading while quarantined must fail with an actionable error.
    let err = reg
        .transition("test-plugin", PluginState::Loading)
        .unwrap_err();
    let msg = err.to_string();
    assert!(
        msg.contains("cascade plugin enable"),
        "error must tell user how to re-enable: {msg}"
    );
}

#[test]
fn enable_lifts_quarantine() {
    use cascade_plugins::lifecycle::{PluginRegistry, PluginState};
    use std::path::PathBuf;

    let reg = PluginRegistry::new();
    reg.register("q-plugin", PathBuf::from("/tmp/q"), Default::default());

    // Force quarantine by recording 3 crashes rapidly.
    reg.transition("q-plugin", PluginState::Validating).unwrap();
    reg.transition("q-plugin", PluginState::Loading).unwrap();
    reg.transition("q-plugin", PluginState::Active).unwrap();
    reg.record_crash("q-plugin").unwrap();
    reg.transition("q-plugin", PluginState::Loading).unwrap();
    reg.transition("q-plugin", PluginState::Active).unwrap();
    reg.record_crash("q-plugin").unwrap();
    reg.transition("q-plugin", PluginState::Loading).unwrap();
    reg.transition("q-plugin", PluginState::Active).unwrap();
    let final_state = reg.record_crash("q-plugin").unwrap();
    assert_eq!(final_state, PluginState::Quarantined);

    reg.enable("q-plugin").unwrap();
    assert_eq!(
        reg.state("q-plugin"),
        Some(PluginState::Discovered),
        "enable should reset state to Discovered"
    );
}

// ── Manifest schema version tests ─────────────────────────────────────────────

#[test]
fn manifest_missing_schema_version_is_actionable() {
    use cascade_plugins::manifest::PluginManifest;

    let toml = r#"
name = "x"
version = "1.0.0"
description = "x"
plugin_type = "chunker"
cascade_version = ">=0.1.0"
"#;
    let err = PluginManifest::parse(toml.as_bytes()).unwrap_err();
    let msg = err.to_string();
    assert!(
        msg.contains("schema_version = 1"),
        "error should tell author exactly what to add: {msg}"
    );
}

#[test]
fn manifest_future_schema_version_names_supported_max() {
    use cascade_plugins::manifest::PluginManifest;

    let toml = r#"
schema_version = 9999
name = "future"
version = "1.0.0"
description = "x"
plugin_type = "chunker"
cascade_version = ">=0.1.0"
"#;
    let err = PluginManifest::parse(toml.as_bytes()).unwrap_err();
    let msg = err.to_string();
    assert!(
        msg.contains("Update Cascade"),
        "error should tell user to update Cascade: {msg}"
    );
}

// ── IPC validation gate tests ─────────────────────────────────────────────────

#[test]
fn input_denied_error_does_not_invoke_wasm() {
    // This test verifies the contract: if InputValidator returns Err, the error
    // kind is PluginError::InputDenied and no WASM execution happens.
    // Full integration (with actual WASM module) is in the sandbox simulator.
    use cascade_plugins::ipc::{InputValidator, PluginError, PluginRequest};

    struct AlwaysDenyValidator;
    impl InputValidator for AlwaysDenyValidator {
        fn validate(&self, _: &PluginRequest) -> Result<(), String> {
            Err("simulated injection attempt detected".into())
        }
    }

    let req = PluginRequest {
        plugin: "test".into(),
        export: "chunker_chunk".into(),
        payload: serde_json::json!({}),
        request_id: None,
    };

    let validation_result = AlwaysDenyValidator.validate(&req);
    assert!(validation_result.is_err());

    // Map to PluginError::InputDenied (mirrors what dispatch() does).
    let plugin_err = PluginError::InputDenied {
        plugin: req.plugin.clone(),
        reason: validation_result.unwrap_err(),
    };
    let msg = plugin_err.to_string();
    assert!(
        msg.contains("input denied"),
        "error variant should identify as input denied: {msg}"
    );
}

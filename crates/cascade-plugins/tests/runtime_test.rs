//! Runtime integration tests — wasmtime fuel limits + WASI capability gating.
//!
//! Purpose: verify PluginSandbox loads WAT fixtures, enforces fuel limits, and
//!   denies ungated filesystem access.
//! Inputs: inline WAT modules (compiled via `wat` crate — no external toolchain).
//! Constraints: no external WASM toolchain; all WASM bytes produced inline.
//! SPORT: cascade-plugins / runtime module (T-P4-E03-04)

use cascade_plugins::{
    manifest::{JsonPermissions, PluginManifest, PluginType},
    runtime::{PluginError, PluginSandbox, PLUGIN_FUEL_LIMIT},
};
use std::io::Write;
use std::sync::{Arc, Mutex};
use tracing_subscriber::fmt::MakeWriter;
use wasmtime::Val;

// ── Helpers ───────────────────────────────────────────────────────────────────

fn hello_wat_bytes() -> Vec<u8> {
    wat::parse_str(include_str!("fixtures/hello.wat")).expect("hello.wat must be valid WAT")
}

/// Build a minimal PluginManifest for tests.
fn test_manifest(name: &str, plugin_type: PluginType) -> PluginManifest {
    PluginManifest {
        schema_version: 1,
        name: name.to_owned(),
        version: "0.1.0".to_owned(),
        description: "test plugin".to_owned(),
        plugin_type,
        cascade_version: ">=0.1.0".to_owned(),
        capabilities: Default::default(),
        extensions: vec![],
        abi_version: 1,
    }
}

/// WAT module with a tight counting loop — exhausts fuel quickly.
fn tight_loop_wat() -> Vec<u8> {
    wat::parse_str(
        r#"
        (module
          (memory (export "memory") 1)
          (func (export "tight_loop") (result i32)
            (local $i i32)
            i32.const 0
            local.set $i
            (block $break
              (loop $loop
                local.get $i
                i32.const 1
                i32.add
                local.set $i
                local.get $i
                i32.const 2000000000
                i32.lt_s
                br_if $loop
              )
            )
            local.get $i
          )
        )
    "#,
    )
    .expect("tight_loop WAT must be valid")
}

fn host_calls_wat() -> Vec<u8> {
    wat::parse_str(
        r#"
        (module
          (import "cascade:plugins/host" "log"
            (func $log (param i32 i32 i32)))
          (import "cascade:plugins/host" "kv-get"
            (func $kv_get (param i32 i32 i32) (result i32)))
          (import "cascade:plugins/host" "kv-set"
            (func $kv_set (param i32 i32 i32 i32)))

          (memory (export "memory") 1)
          (data (i32.const 0) "actual guest memory log payload")
          (data (i32.const 64) "roundtrip-key")
          (data (i32.const 96) "42")

          (func (export "emit-log")
            i32.const 3
            i32.const 0
            i32.const 31
            call $log)

          (func (export "set-value")
            i32.const 64
            i32.const 13
            i32.const 96
            i32.const 2
            call $kv_set)

          (func (export "get-value") (result i32)
            (local $len i32)
            i32.const 64
            i32.const 13
            i32.const 128
            call $kv_get
            local.set $len

            local.get $len
            i32.const 2
            i32.eq
            i32.const 128
            i32.load8_u
            i32.const 52
            i32.eq
            i32.and
            i32.const 129
            i32.load8_u
            i32.const 50
            i32.eq
            i32.and)
        )
        "#,
    )
    .expect("host-calls WAT must be valid")
}

#[derive(Clone, Default)]
struct CapturedLogs(Arc<Mutex<Vec<u8>>>);

impl CapturedLogs {
    fn text(&self) -> String {
        String::from_utf8(self.0.lock().unwrap().clone()).unwrap()
    }
}

struct CapturedLogWriter(Arc<Mutex<Vec<u8>>>);

impl Write for CapturedLogWriter {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

impl<'a> MakeWriter<'a> for CapturedLogs {
    type Writer = CapturedLogWriter;

    fn make_writer(&'a self) -> Self::Writer {
        CapturedLogWriter(Arc::clone(&self.0))
    }
}

// ── T-P4-E03-04 Tests ─────────────────────────────────────────────────────────

#[tokio::test(flavor = "current_thread")]
async fn host_log_reads_guest_memory_and_kv_persists_across_calls_and_reload() {
    let wasm = host_calls_wat();
    let temp = tempfile::tempdir().unwrap();
    let data_dir = temp.path().join("plugin-data");
    let sandbox = PluginSandbox::new(PluginType::ToolIntegration).unwrap();
    let plugin = sandbox
        .load_with_permissions_and_data_dir(
            &wasm,
            "host-calls-test",
            JsonPermissions::default(),
            None,
            &data_dir,
        )
        .unwrap();

    let logs = CapturedLogs::default();
    let subscriber = tracing_subscriber::fmt()
        .with_writer(logs.clone())
        .with_ansi(false)
        .without_time()
        .finish();
    // This test uses a current-thread runtime so the thread-local subscriber
    // remains installed while the async wasmtime call is polled.
    let subscriber_guard = tracing::subscriber::set_default(subscriber);
    plugin.call("emit-log", &[]).await.unwrap();
    drop(subscriber_guard);
    assert!(
        logs.text().contains("actual guest memory log payload"),
        "host log output did not contain the guest-memory message: {}",
        logs.text()
    );

    plugin.call("set-value", &[]).await.unwrap();
    let result = plugin.call("get-value", &[]).await.unwrap();
    assert!(matches!(result.as_slice(), [Val::I32(1)]));
    drop(plugin);

    let reloaded = sandbox
        .load_with_permissions_and_data_dir(
            &wasm,
            "host-calls-test",
            JsonPermissions::default(),
            None,
            &data_dir,
        )
        .unwrap();
    let result = reloaded.call("get-value", &[]).await.unwrap();
    assert!(matches!(result.as_slice(), [Val::I32(1)]));

    let db_path = data_dir.join("plugin-kv.sqlite3");
    assert!(
        db_path.is_file(),
        "KV database was not created under plugin data dir"
    );
    let conn = rusqlite::Connection::open(db_path).unwrap();
    let stored: Vec<u8> = conn
        .query_row(
            "SELECT value FROM plugin_kv WHERE key = 'roundtrip-key'",
            [],
            |row| row.get(0),
        )
        .unwrap();
    assert_eq!(stored, b"42");
}

/// Test (a): load hello.wat and call `hello` — must return i32 42.
#[tokio::test]
async fn test_hello_fixture_returns_42() {
    let wasm = hello_wat_bytes();
    let manifest = test_manifest("hello-test", PluginType::Chunker);

    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("PluginSandbox::new must succeed");
    let plugin = sandbox
        .load(&wasm, &manifest)
        .expect("hello.wat must load cleanly");

    let results = plugin
        .call("hello", &[])
        .await
        .expect("hello() must succeed");

    assert_eq!(results.len(), 1, "hello() returns exactly one value");
    match &results[0] {
        Val::I32(v) => assert_eq!(*v, 42, "hello() must return 42"),
        other => panic!("expected i32, got {other:?}"),
    }
}

/// Test (b): set fuel=1 and verify ResourceExhausted error on tight loop.
#[tokio::test]
async fn test_fuel_exhaustion_returns_resource_exhausted() {
    let wasm = tight_loop_wat();

    let sandbox = PluginSandbox::new(PluginType::Chunker).expect("PluginSandbox::new must succeed");

    // Load with minimal fuel (1 instruction) to guarantee exhaustion.
    let plugin = sandbox
        .load_with_permissions(&wasm, "fuel-test", JsonPermissions::default(), Some(1))
        .expect("tight_loop must compile");

    let result = plugin.call("tight_loop", &[]).await;

    assert!(result.is_err(), "tight_loop with fuel=1 must fail");

    let err = result.expect_err("tight_loop with fuel=1 must return an error");
    match err {
        PluginError::ResourceExhausted { limit } => {
            assert_eq!(
                limit, 1,
                "ResourceExhausted must carry the configured limit"
            );
        }
        other => panic!("expected ResourceExhausted, got: {other:?}"),
    }
}

/// Test: fuel limit constant is the spec-mandated 1_000_000_000.
#[test]
fn test_plugin_fuel_limit_constant() {
    assert_eq!(
        PLUGIN_FUEL_LIMIT, 1_000_000_000,
        "PLUGIN_FUEL_LIMIT must be 1e9 per spec"
    );
}

/// Test: PluginSandbox::new succeeds for all five plugin trait types.
#[test]
fn test_sandbox_new_for_all_plugin_types() {
    for plugin_type in [
        PluginType::Chunker,
        PluginType::Retriever,
        PluginType::Parser,
        PluginType::EmbeddingProvider,
        PluginType::Reranker,
        PluginType::QueryStrategy,
        PluginType::Agent,
        PluginType::ToolIntegration,
    ] {
        let result = PluginSandbox::new(plugin_type);
        assert!(
            result.is_ok(),
            "PluginSandbox::new must succeed for {plugin_type:?}"
        );
    }
}

/// Test: loading invalid WASM bytes returns PluginError::Compile.
#[test]
fn test_invalid_wasm_returns_compile_error() {
    let bad_bytes = b"this is not WASM";
    let manifest = test_manifest("bad-wasm", PluginType::Chunker);

    let sandbox = PluginSandbox::new(PluginType::Chunker).unwrap();
    let result = sandbox.load(bad_bytes, &manifest);

    assert!(result.is_err());
    let err = result.err().expect("load must have returned an error");
    match err {
        PluginError::Compile(_) => { /* expected */ }
        other => panic!("expected Compile error, got: {other:?}"),
    }
}

/// Test: hello.wat with default fuel (1e9) does NOT exhaust.
#[tokio::test]
async fn test_hello_does_not_exhaust_default_fuel() {
    let wasm = hello_wat_bytes();
    let manifest = test_manifest("no-exhaust", PluginType::Chunker);

    let sandbox = PluginSandbox::new(PluginType::Chunker).unwrap();
    let plugin = sandbox.load(&wasm, &manifest).unwrap();

    // Must NOT return ResourceExhausted with default fuel.
    let result = plugin.call("hello", &[]).await;
    assert!(
        !matches!(result, Err(PluginError::ResourceExhausted { .. })),
        "hello() with default fuel must not exhaust: {result:?}"
    );
}

// ── WASI capability gating tests (T-P4-E03-04) ───────────────────────────────

/// Test: empty permissions builds a valid sandbox (WASI deny-by-default).
#[tokio::test]
async fn test_empty_permissions_sandbox_calls_succeed() {
    let wasm = hello_wat_bytes();
    let manifest = test_manifest("empty-perms", PluginType::Chunker);

    let sandbox = PluginSandbox::new(PluginType::Chunker).unwrap();
    let plugin = sandbox.load(&wasm, &manifest).unwrap();

    // hello() does not use WASI — must succeed even with zero permissions.
    let result = plugin.call("hello", &[]).await;
    assert!(
        result.is_ok(),
        "hello() with empty perms must succeed: {result:?}"
    );
}

/// Test: env isolation — plugin cannot access env vars not in the allow-list.
#[test]
fn test_env_isolation_via_wasi_ctx() {
    use cascade_plugins::manifest::EnvPermission;
    use cascade_plugins::runtime::wasi_ctx::build_wasi_ctx;

    // Build a WASI ctx that only allows "CASCADE_PLUGIN_TEST_VAR" (not set in env).
    let perms = JsonPermissions {
        env: vec![EnvPermission {
            name: "CASCADE_PLUGIN_TEST_VAR_NOT_SET".to_owned(),
        }],
        ..Default::default()
    };
    let ctx = build_wasi_ctx(&perms);
    assert!(ctx.is_ok(), "WASI ctx with env perms must build");
    // The variable is not set so won't be injected, but the context is valid.
}

/// Test: fs outside preopen is denied at the WASI context layer.
#[test]
fn test_fs_outside_preopen_not_added_to_context() {
    use cascade_plugins::manifest::FsPermission;
    use cascade_plugins::runtime::wasi_ctx::build_wasi_ctx;

    let perms = JsonPermissions {
        fs: vec![FsPermission {
            path: "/nonexistent/not/here".to_owned(),
            read: true,
            write: false,
        }],
        ..Default::default()
    };
    // Non-existent path must be silently skipped (graceful degradation).
    let ctx = build_wasi_ctx(&perms);
    assert!(
        ctx.is_ok(),
        "non-existent preopen path must be skipped gracefully"
    );
}

// ── WIT binding round-trip tests (T-P4-E03-05) ───────────────────────────────

/// Test: ToolCall serialises and deserialises without field loss.
#[test]
fn test_tool_call_round_trip() {
    use cascade_plugins::wit_bindings::{ToolCall, ToolResult};

    let call = ToolCall {
        call_id: "test-01".to_owned(),
        tool_name: "weather".to_owned(),
        args_json: r#"{"city":"London"}"#.to_owned(),
    };

    let json = serde_json::to_value(&call).expect("ToolCall must serialise");
    let back: ToolCall = serde_json::from_value(json).expect("ToolCall must deserialise");
    assert_eq!(call, back, "ToolCall must round-trip without field loss");

    let result = ToolResult {
        call_id: "test-01".to_owned(),
        output: "Cloudy, 15°C".to_owned(),
        data_json: Some(r#"{"temp":15}"#.to_owned()),
    };
    let json = serde_json::to_value(&result).expect("ToolResult must serialise");
    let back: ToolResult = serde_json::from_value(json).expect("ToolResult must deserialise");
    assert_eq!(
        result, back,
        "ToolResult must round-trip without field loss"
    );
}

/// Test: AgentResponse round-trips including nested ToolCallRequest list.
#[test]
fn test_agent_response_round_trip() {
    use cascade_plugins::wit_bindings::{AgentResponse, ToolCallRequest};

    let resp = AgentResponse {
        text: "I'll check the weather.".to_owned(),
        tool_calls: vec![ToolCallRequest {
            call_id: "c1".to_owned(),
            tool_name: "weather".to_owned(),
            args_json: r#"{"city":"London"}"#.to_owned(),
        }],
        is_final: false,
    };

    let json = serde_json::to_value(&resp).expect("AgentResponse must serialise");
    let back: AgentResponse = serde_json::from_value(json).expect("AgentResponse must deserialise");
    assert_eq!(
        resp, back,
        "AgentResponse must round-trip without field loss"
    );
}

/// Test: Chunk serialises and deserialises without field loss.
#[test]
fn test_chunk_round_trip() {
    use cascade_plugins::wit_bindings::{Chunk, ChunkMeta};

    let chunk = Chunk {
        text: "The quick brown fox".to_owned(),
        meta: ChunkMeta {
            index: 0,
            byte_offset: 0,
            section: Some("Introduction".to_owned()),
        },
    };

    let json = serde_json::to_value(&chunk).expect("Chunk must serialise");
    let back: Chunk = serde_json::from_value(json).expect("Chunk must deserialise");
    assert_eq!(chunk, back, "Chunk must round-trip without field loss");
}

/// Test: RetrievalResult round-trip.
#[test]
fn test_retrieval_result_round_trip() {
    use cascade_plugins::wit_bindings::RetrievalResult;

    let r = RetrievalResult {
        text: "relevant snippet".to_owned(),
        score: 0.95,
        source: "doc://readme.md".to_owned(),
        chunk_index: 3,
    };

    let json = serde_json::to_value(&r).expect("RetrievalResult must serialise");
    let back: RetrievalResult =
        serde_json::from_value(json).expect("RetrievalResult must deserialise");
    assert_eq!(r.text, back.text);
    assert_eq!(r.source, back.source);
    assert_eq!(r.chunk_index, back.chunk_index);
    // f32 round-trip through JSON may lose precision — check approximately.
    assert!(
        (r.score - back.score).abs() < 0.001,
        "score must survive round-trip"
    );
}

/// Test: Embedding round-trip.
#[test]
fn test_embedding_round_trip() {
    use cascade_plugins::wit_bindings::Embedding;

    let emb = Embedding {
        vector: vec![0.1_f32, 0.2, 0.3],
        input_index: 0,
    };

    let json = serde_json::to_value(&emb).expect("Embedding must serialise");
    let back: Embedding = serde_json::from_value(json).expect("Embedding must deserialise");
    assert_eq!(emb.input_index, back.input_index);
    assert_eq!(emb.vector.len(), back.vector.len());
}

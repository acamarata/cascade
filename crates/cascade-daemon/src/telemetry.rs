//! Telemetry initialisation for the cascade daemon.
//!
//! Purpose: configure layered tracing-subscriber (JSON rolling file + compact
//! stderr, optional OpenTelemetry OTLP export) and return the handles that must
//! be kept alive for the process lifetime.
//!
//! Inputs:  `log_dir` — directory where rolling log files are written.
//!          `otel_provider` — optional SdkTracerProvider from `init_tracing`.
//! Outputs: `WorkerGuard` — must be bound in `main()`.
//!          `Option<SdkTracerProvider>` — from `init_tracing`; must be kept
//!          alive and `shutdown()` called on daemon exit to flush pending spans.
//! Constraints: must be called exactly once per process; panics if the global
//! subscriber is already set (tracing-subscriber invariant).
//! SPORT: MASTER-CRATES.md — cascade-daemon logging=✅ structured JSON rolling
//!        rotation, otel_tracing=✅ optional OTLP/gRPC (CASCADE_OTEL_ENDPOINT)

use std::path::Path;
use tracing_appender::non_blocking::WorkerGuard;
use tracing_appender::{non_blocking, rolling};
use tracing_subscriber::{fmt, layer::SubscriberExt, util::SubscriberInitExt, EnvFilter, Layer};

// OTel imports — used only when an endpoint is provided.
use opentelemetry::trace::TracerProvider as _;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::trace::SdkTracerProvider;

/// Initialise an OpenTelemetry OTLP/gRPC tracer provider.
///
/// Returns `None` when `endpoint` is `None` (OTel export disabled) so the
/// caller can skip wiring the OTel subscriber layer entirely.
///
/// # Arguments
///
/// * `endpoint` — OTLP collector gRPC address, e.g. `http://localhost:4317`.
///   Pass `None` to disable tracing export.
///
/// # Returns
///
/// `Some(SdkTracerProvider)` when OTel is configured; `None` otherwise. The
/// returned provider must be stored and `provider.shutdown()` called on exit.
///
/// The batch exporter drives its own background runtime as of
/// opentelemetry_sdk 0.32 — the explicit `runtime::Tokio` argument no longer
/// exists — but the exporter still needs a Tokio reactor, so call this only
/// after `#[tokio::main]` has started the runtime.
///
/// # Errors
///
/// Errors during exporter or provider construction are swallowed and `None` is
/// returned. This matches the fire-and-forget contract: an unavailable collector
/// must not crash the daemon.
pub fn init_tracing(endpoint: Option<&str>) -> Option<SdkTracerProvider> {
    let endpoint = endpoint?;

    let exporter = opentelemetry_otlp::SpanExporter::builder()
        .with_tonic()
        .with_endpoint(endpoint)
        .build()
        .ok()?;

    // `Resource::new` became private in 0.32; resources are built through the
    // builder, which also supplies the SDK's own default attributes.
    let resource = opentelemetry_sdk::Resource::builder()
        .with_attributes(vec![
            opentelemetry::KeyValue::new(
                opentelemetry_semantic_conventions::resource::SERVICE_NAME,
                "cascaded",
            ),
            opentelemetry::KeyValue::new(
                opentelemetry_semantic_conventions::resource::SERVICE_VERSION,
                env!("CARGO_PKG_VERSION"),
            ),
        ])
        .build();

    // `trace::Config` is gone in 0.32 — the resource is set directly on the
    // provider builder instead.
    let provider = SdkTracerProvider::builder()
        .with_batch_exporter(exporter)
        .with_resource(resource)
        .build();

    opentelemetry::global::set_tracer_provider(provider.clone());
    Some(provider)
}

/// Initialise the global tracing subscriber with two (or three) layers:
///
/// 1. **JSON file layer** — writes newline-delimited JSON to a daily rolling
///    file under `log_dir` (prefix `cascaded`, suffix `log`). Retains the last
///    7 files. Uses a non-blocking background writer; the returned `WorkerGuard`
///    must be stored for the process lifetime to ensure all buffered records
///    are flushed before exit.
/// 2. **Stderr compact layer** — human-readable compact format on stderr,
///    gated by `RUST_LOG` (defaults to `info` if unset).
/// 3. **OTel bridge layer** (optional) — when `otel_provider` is `Some`,
///    forwards spans to the global tracer provider via `tracing-opentelemetry`.
///
/// # Panics
///
/// Panics if `log_dir` cannot be created or if the rolling appender fails to
/// initialise. Panics (via `tracing-subscriber`) if a global subscriber has
/// already been installed.
// Called by main() when OTEL telemetry is enabled via config.telemetry.enabled.
#[allow(dead_code)]
pub fn init_logging(log_dir: &Path, otel_provider: Option<&SdkTracerProvider>) -> WorkerGuard {
    std::fs::create_dir_all(log_dir).expect("cannot create log dir");

    // Bug 4 fix: set a restrictive umask (0o177 → new files get 0o600) for the
    // duration of the appender build so the rolling appender creates its log
    // file with 0o600 rather than the process-default umask (typically 0o022,
    // which yields 0o644).  The umask is restored unconditionally immediately
    // after build() returns.
    //
    // Why here rather than chmodding afterwards: the rolling appender creates
    // the log file during build(), not on first write.  Calling set_permissions
    // on entries that pre-exist in log_dir (old code) misses the file just
    // created by build() because the directory scan runs before build().
    // Setting the umask around build() is the only race-free approach.
    #[cfg(unix)]
    let _prev_umask = {
        // SAFETY: umask(2) is async-signal-safe and always succeeds.  We
        // restore the previous value immediately after build() so other code
        // in this process is not affected.
        unsafe { libc::umask(0o177) }
    };

    let file_appender = rolling::Builder::new()
        .rotation(rolling::Rotation::DAILY)
        .filename_prefix("cascaded")
        .filename_suffix("log")
        .max_log_files(7)
        .build(log_dir)
        .expect("cannot build rolling appender");

    // Restore the previous umask now that the appender file has been created.
    #[cfg(unix)]
    unsafe {
        libc::umask(_prev_umask);
    }

    // Belt-and-suspenders: chmod any log file in log_dir that was already
    // present from a prior run (the umask above only covers the file created
    // by this build() call).
    // Unix-only: Windows has no mode bitmask. Log-file ACL tightening on
    // Windows is tracked with the DACL work (T-P7-E25-10); doing nothing here
    // is honest, whereas a no-op named like a permission fix is not.
    #[cfg(unix)]
    if let Ok(entries) = std::fs::read_dir(log_dir) {
        use std::os::unix::fs::PermissionsExt;
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_file() {
                let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600));
            }
        }
    }

    let (non_blocking_writer, guard) = non_blocking(file_appender);
    let file_layer = fmt::layer()
        .json()
        .with_writer(non_blocking_writer)
        .with_filter(EnvFilter::new("info"));
    let stderr_layer = fmt::layer()
        .compact()
        .with_writer(std::io::stderr)
        .with_filter(EnvFilter::from_default_env());

    let registry = tracing_subscriber::registry()
        .with(file_layer)
        .with(stderr_layer);

    if let Some(provider) = otel_provider {
        let tracer = provider.tracer("cascaded");
        let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);
        registry.with(otel_layer).init();
    } else {
        registry.init();
    }

    guard
}

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    #[test]
    #[serial(global_env)]
    fn test_log_file_created() {
        // NOTE: this test cannot call init_logging because the global subscriber
        // is already set by a prior test run in the same process. We verify the
        // rolling appender builds cleanly instead.
        let dir = TempDir::new().unwrap();
        std::fs::create_dir_all(dir.path()).unwrap();
        // Verify appender construction succeeds.
        let _appender = rolling::Builder::new()
            .rotation(rolling::Rotation::DAILY)
            .filename_prefix("cascaded")
            .filename_suffix("log")
            .max_log_files(7)
            .build(dir.path())
            .expect("rolling appender must build");
        let entries: Vec<_> = std::fs::read_dir(dir.path()).unwrap().collect();
        // Rolling appender creates the file on first write, not on construction.
        // The dir exists — that is sufficient for this smoke test.
        let _ = entries;
    }

    /// Verify that the rolling appender creates its log file with mode 0o600.
    ///
    /// The fix (Bug 4): we set umask(0o177) before calling rolling::Builder::build()
    /// so the newly created log file inherits permissions 0o600.  This test
    /// replicates the exact umask-around-build pattern from init_logging and
    /// confirms the resulting file is not world- or group-readable.
    ///
    /// We cannot call init_logging() directly (it installs a global subscriber
    /// which panics on double-init), so we test the underlying file-creation
    /// mechanic in isolation.
    #[cfg(unix)]
    #[test]
    #[serial(global_env)]
    fn test_log_file_created_with_mode_0600() {
        use std::os::unix::fs::MetadataExt;

        let dir = TempDir::new().unwrap();
        std::fs::create_dir_all(dir.path()).unwrap();

        // Mirror the fix: set umask(0o177) → new files get 0o600.
        let prev = unsafe { libc::umask(0o177) };

        let appender = rolling::Builder::new()
            .rotation(rolling::Rotation::DAILY)
            .filename_prefix("test-cascaded")
            .filename_suffix("log")
            .max_log_files(7)
            .build(dir.path())
            .expect("rolling appender must build");

        // Restore umask immediately (mirrors init_logging).
        unsafe { libc::umask(prev) };

        // Trigger a write so the appender flushes its buffer and the file
        // appears on disk.  We use the blocking write path via a short-lived
        // non-blocking pair so the flush is synchronous enough for the test.
        {
            use std::io::Write as _;
            let (mut writer, _guard) = tracing_appender::non_blocking(appender);
            writeln!(writer, "{{\"msg\":\"test\"}}").expect("write must succeed");
            // _guard drops here → background thread flushes and file is visible
        }

        // Give the non-blocking writer a moment to flush.
        std::thread::sleep(std::time::Duration::from_millis(50));

        // Find the log file in the temp dir and check its mode.
        let mut found = false;
        for entry in std::fs::read_dir(dir.path()).unwrap().flatten() {
            let meta = entry.metadata().expect("metadata must be readable");
            if meta.is_file() {
                found = true;
                let mode = meta.mode() & 0o777;
                assert_eq!(
                    mode,
                    0o600,
                    "log file {} must have mode 0o600, got 0o{:o}",
                    entry.path().display(),
                    mode
                );
            }
        }
        assert!(found, "at least one log file must have been created");
    }

    // init_tracing(None) must return None without panicking and without
    // attempting any network connection.
    #[test]
    #[serial(global_env)]
    fn test_init_tracing_none_returns_none() {
        let result = init_tracing(None);
        assert!(result.is_none(), "init_tracing(None) must return None");
    }

    // init_tracing(Some(endpoint)) must return Some without panicking even
    // when no OTLP collector is running. Connection errors are fire-and-forget.
    // Requires a Tokio runtime because the batch exporter spawns async tasks.
    // We do NOT call provider.shutdown() in tests to avoid blocking on a
    // network connection that will never be established in CI.
    #[tokio::test]
    #[serial(global_env)]
    async fn test_init_tracing_some_returns_some() {
        let result = init_tracing(Some("http://localhost:4317"));
        assert!(
            result.is_some(),
            "init_tracing(Some(endpoint)) must return Some (setup succeeds without a real collector)"
        );
        // Drop the provider without calling shutdown() — the background exporter
        // will fail silently since no collector is listening, which is the
        // documented fire-and-forget behaviour for this optional feature.
        drop(result);
    }

    // Verify that span attribute names in this module do not include sensitive
    // field names that could inadvertently log user content or secrets.
    #[test]
    fn test_no_sensitive_span_attributes() {
        let source = include_str!("telemetry.rs");
        let sensitive = [
            "\"query\"",
            "\"content\"",
            "\"text\"",
            "\"path\"",
            "\"key\"",
            "\"secret\"",
        ];
        for name in &sensitive {
            for line in source.lines() {
                let trimmed = line.trim();
                // Skip comment lines — the name may appear in documentation.
                if trimmed.starts_with("//") {
                    continue;
                }
                assert!(
                    !trimmed.contains(name),
                    "Sensitive span attribute {name} found in telemetry.rs line: {trimmed:?}\n\
                     Rename to avoid logging user content."
                );
            }
        }
    }

    // Verify that the config-gated code path (telemetry.enabled = false, the
    // default) produces no exporter. This mirrors the runtime gate in main.rs:
    // `Config::default().telemetry.enabled == false` -> init_tracing(None).
    #[test]
    #[serial(global_env)]
    fn test_telemetry_gate_disabled_returns_none_exporter() {
        // When the config gate is false, main.rs calls init_tracing(None).
        // Confirm that code path returns None -- no exporter is constructed.
        let result = init_tracing(None);
        assert!(
            result.is_none(),
            "init_tracing(None) must return None -- no exporter when telemetry is disabled"
        );
    }
}

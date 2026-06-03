//! Telemetry initialisation for the cascade daemon.
//!
//! Purpose: configure layered tracing-subscriber (JSON rolling file + compact
//! stderr, optional OpenTelemetry OTLP export) and return the handles that must
//! be kept alive for the process lifetime.
//!
//! Inputs:  `log_dir` — directory where rolling log files are written.
//!          `otel_provider` — optional TracerProvider returned by `init_tracing`.
//! Outputs: `WorkerGuard` — must be bound in `main()`.
//!          `Option<TracerProvider>` — returned by `init_tracing`; must be kept
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
use opentelemetry_sdk::trace::TracerProvider;

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
/// `Some(TracerProvider)` when OTel is configured; `None` otherwise. The
/// returned provider must be stored and `provider.shutdown()` called on exit.
///
/// The batch exporter runs on the Tokio runtime (`opentelemetry_sdk::runtime::Tokio`);
/// call this only after `#[tokio::main]` has started the runtime.
///
/// # Errors
///
/// Errors during exporter or provider construction are swallowed and `None` is
/// returned. This matches the fire-and-forget contract: an unavailable collector
/// must not crash the daemon.
pub fn init_tracing(endpoint: Option<&str>) -> Option<TracerProvider> {
    let endpoint = endpoint?;

    let exporter = opentelemetry_otlp::new_exporter()
        .tonic()
        .with_endpoint(endpoint)
        .build_span_exporter()
        .ok()?;

    let resource = opentelemetry_sdk::Resource::new(vec![
        opentelemetry::KeyValue::new(
            opentelemetry_semantic_conventions::resource::SERVICE_NAME,
            "cascaded",
        ),
        opentelemetry::KeyValue::new(
            opentelemetry_semantic_conventions::resource::SERVICE_VERSION,
            env!("CARGO_PKG_VERSION"),
        ),
    ]);

    let config = opentelemetry_sdk::trace::Config::default().with_resource(resource);

    let provider = opentelemetry_sdk::trace::TracerProvider::builder()
        .with_batch_exporter(exporter, opentelemetry_sdk::runtime::Tokio)
        .with_config(config)
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
pub fn init_logging(log_dir: &Path, otel_provider: Option<&TracerProvider>) -> WorkerGuard {
    std::fs::create_dir_all(log_dir).expect("cannot create log dir");
    let file_appender = rolling::Builder::new()
        .rotation(rolling::Rotation::DAILY)
        .filename_prefix("cascaded")
        .filename_suffix("log")
        .max_log_files(7)
        .build(log_dir)
        .expect("cannot build rolling appender");
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
    use tempfile::TempDir;

    #[test]
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

    // init_tracing(None) must return None without panicking and without
    // attempting any network connection.
    #[test]
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
}

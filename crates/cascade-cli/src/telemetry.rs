//! Telemetry initialisation for the cascade CLI.
//!
//! Purpose: configure layered tracing-subscriber (compact stderr only,
//! optional OpenTelemetry OTLP export) for interactive CLI commands.
//!
//! Inputs:  `otel_provider` — optional provider from `init_cli_tracing`.
//! Outputs: `Option<OtelProvider>` — from `init_cli_tracing`; must be kept
//!          alive and `shutdown()` called on CLI exit to flush pending spans.
//! Constraints: must be called exactly once per process; panics if the global
//! subscriber is already set (tracing-subscriber invariant).
//! SPORT: MASTER-CRATES.md — cascade-cli logging=✅ stderr compact,
//!        otel_tracing=✅ optional OTLP/gRPC (CASCADE_OTEL_ENDPOINT)

use tracing_subscriber::{fmt, layer::SubscriberExt, util::SubscriberInitExt, EnvFilter, Layer};

#[cfg(feature = "otel")]
use opentelemetry::trace::TracerProvider as _;
#[cfg(feature = "otel")]
use opentelemetry_otlp::WithExportConfig;

/// The tracer provider handle callers hold between `init_cli_tracing` and exit.
///
/// Behind the `otel` feature this is the real SDK provider. Without the
/// feature it is an UNINHABITED type, so `Option<&OtelProvider>` can only ever
/// be `None` and every otel branch compiles away — call sites need no `cfg`
/// of their own (T-P7-E25-11).
#[cfg(feature = "otel")]
pub type OtelProvider = opentelemetry_sdk::trace::SdkTracerProvider;

/// Uninhabited stand-in used when the `otel` feature is off. See the
/// feature-enabled alias above.
#[cfg(not(feature = "otel"))]
pub enum OtelProvider {}

/// Span export is not compiled in — always `None`.
///
/// Kept as a real function so `main` does not need to branch on the feature.
#[cfg(not(feature = "otel"))]
pub fn init_cli_tracing(_endpoint: Option<&str>) -> Option<OtelProvider> {
    None
}

/// Initialise an OpenTelemetry OTLP/gRPC tracer provider for the CLI.
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
/// `Some(OtelProvider)` when OTel is configured; `None` otherwise. The
/// returned provider must be stored and `provider.shutdown()` called on exit.
///
/// The batch exporter runs on the Tokio runtime (`opentelemetry_sdk::runtime::Tokio`);
/// call this only after `#[tokio::main]` has started the runtime.
///
/// # Errors
///
/// Errors during exporter or provider construction are swallowed and `None` is
/// returned. This matches the fire-and-forget contract: an unavailable collector
/// must not crash the CLI.
#[cfg(feature = "otel")]
pub fn init_cli_tracing(endpoint: Option<&str>) -> Option<OtelProvider> {
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
                "cascade-cli",
            ),
            opentelemetry::KeyValue::new(
                opentelemetry_semantic_conventions::resource::SERVICE_VERSION,
                env!("CARGO_PKG_VERSION"),
            ),
        ])
        .build();

    // `trace::Config` is gone in 0.32 — the resource is set directly on the
    // provider builder, and the batch exporter no longer takes a runtime arg.
    let provider = OtelProvider::builder()
        .with_batch_exporter(exporter)
        .with_resource(resource)
        .build();

    opentelemetry::global::set_tracer_provider(provider.clone());
    Some(provider)
}

/// Initialise the global tracing subscriber with two (or three) layers:
///
/// 1. **Stderr compact layer** — human-readable compact format on stderr,
///    gated by `RUST_LOG` (defaults to `warn` if unset). No file logging
///    for the CLI (interactive tool, no persistent logs).
/// 2. **OTel bridge layer** (optional) — when `otel_provider` is `Some`,
///    forwards spans to the global tracer provider via `tracing-opentelemetry`.
///
/// # Panics
///
/// Panics (via `tracing-subscriber`) if a global subscriber has already been
/// installed.
pub fn init_cli_logging(otel_provider: Option<&OtelProvider>) {
    let stderr_layer = fmt::layer()
        .compact()
        .with_writer(std::io::stderr)
        .with_filter(EnvFilter::from_default_env().add_directive(tracing::Level::WARN.into()));

    let registry = tracing_subscriber::registry().with(stderr_layer);

    #[cfg(feature = "otel")]
    if let Some(provider) = otel_provider {
        let tracer = provider.tracer("cascade-cli");
        let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);
        registry.with(otel_layer).init();
        return;
    }

    // Without the `otel` feature `otel_provider` is uninhabited and always
    // None; the binding is named to keep the signature stable either way.
    let _ = &otel_provider;
    registry.init();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_init_cli_logging_no_panic() {
        // Verify init_cli_logging(None) does not panic.
        // Note: this test runs in isolation and sets the global subscriber,
        // so subsequent tests cannot call init again (tracing-subscriber
        // invariant). Only one test per process can call init_cli_logging.
        init_cli_logging(None);
        // If we reach here, the function succeeded without panicking.
    }

    #[test]
    fn test_init_cli_tracing_none_returns_none() {
        let result = init_cli_tracing(None);
        assert!(result.is_none(), "init_cli_tracing(None) must return None");
    }

    // init_cli_tracing(Some(endpoint)) must return Some without panicking even
    // when no OTLP collector is running. Connection errors are fire-and-forget.
    // Requires a Tokio runtime because the batch exporter spawns async tasks.
    // We do NOT call provider.shutdown() in tests to avoid blocking on a
    // network connection that will never be established in CI.
    #[cfg(feature = "otel")]
    #[tokio::test]
    async fn test_init_cli_tracing_some_returns_some() {
        let result = init_cli_tracing(Some("http://localhost:4317"));
        assert!(
            result.is_some(),
            "init_cli_tracing(Some(endpoint)) must return Some (setup succeeds without a real collector)"
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
                // Skip comment lines -- the name may appear in documentation.
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
}

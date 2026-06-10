//! Widget plugin trait — host-side plugin-facing API.
//!
//! Purpose: define the `WidgetPlugin` trait that a WASM plugin must satisfy to
//!   be registered as a GUI widget in the Cascade host. Widgets render read-only
//!   informational panels in the Cascade desktop application.
//!
//! Inputs:  none (widgets are polled by the host on demand).
//! Outputs: `WidgetData` — title, body string, and JSON metadata.
//! Constraints: `render()` must be fast (no network, no filesystem); the host
//!   may poll every few seconds; all public types are `Serialize + Deserialize`;
//!   trait is object-safe with `Send + Sync + 'static` bounds.
//! SPORT: cascade-plugins / widget plugin trait (T-P4-E03-18)

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use thiserror::Error;

// ── I/O shapes ────────────────────────────────────────────────────────────────

/// Render payload returned by a `WidgetPlugin::render()` call.
///
/// The host displays `title` as the panel header and `body` as the panel
/// content. `metadata` is surfaced to the GUI for filtering or sorting.
///
/// This shape intentionally mirrors the guest-side `cascade_pdk::WidgetData`
/// so no conversion is needed at the WASM boundary.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WidgetData {
    /// Short title shown in the widget panel header.
    pub title: String,
    /// Main content string rendered in the widget body.
    pub body: String,
    /// Arbitrary JSON metadata; use `serde_json::Value::Null` if empty.
    pub metadata: serde_json::Value,
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors that a `WidgetPlugin` may return (plugin-level failures).
///
/// Render errors are rare — they indicate a plugin crash or resource
/// exhaustion, not a content-level failure. The GUI shows an error state.
#[derive(Debug, Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum WidgetPluginError {
    /// The plugin encountered an I/O failure during render.
    #[error("IO error in widget plugin: {message}")]
    Io { message: String },

    /// The plugin is temporarily unavailable.
    #[error("widget plugin unavailable: {message}")]
    Unavailable { message: String },

    /// An internal plugin error with a diagnostic message.
    #[error("widget plugin internal error: {message}")]
    Internal { message: String },
}

// ── Capability declarations ───────────────────────────────────────────────────

/// Capability declaration for a `WidgetPlugin`.
///
/// Widgets are typically stateless read-only renderers and do not need
/// filesystem or network access. Declare the minimum set you require.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WidgetPluginCapabilities {
    /// Whether this widget requires outbound network access.
    #[serde(default)]
    pub needs_net_outbound: bool,

    /// Whether this widget requires filesystem read access.
    #[serde(default)]
    pub needs_fs_read: bool,

    /// Requested WASM memory in MiB (default: 32).
    #[serde(default = "default_widget_memory_mb")]
    pub max_memory_mb: u32,
}

fn default_widget_memory_mb() -> u32 {
    32
}

impl Default for WidgetPluginCapabilities {
    fn default() -> Self {
        Self {
            needs_net_outbound: false,
            needs_fs_read: false,
            max_memory_mb: default_widget_memory_mb(),
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Host-side trait a WASM widget plugin must satisfy.
///
/// The host polls `render()` on its own schedule (typically every few seconds)
/// and updates the GUI panel with the returned `WidgetData`. Widget plugins
/// MUST NOT perform blocking I/O inside `render()` — keep it fast.
///
/// # Example (mock implementation for tests)
///
/// ```rust
/// # use cascade_plugins::traits::widget::{WidgetPlugin, WidgetPluginCapabilities, WidgetPluginError, WidgetData};
/// # use async_trait::async_trait;
/// struct ClockWidget;
///
/// #[async_trait]
/// impl WidgetPlugin for ClockWidget {
///     fn name(&self) -> &str { "clock-widget" }
///     fn capabilities(&self) -> WidgetPluginCapabilities { Default::default() }
///
///     async fn render(&self) -> Result<WidgetData, WidgetPluginError> {
///         Ok(WidgetData {
///             title: "Clock".into(),
///             body: "UTC: 2026-01-01T00:00:00Z".into(),
///             metadata: serde_json::Value::Null,
///         })
///     }
/// }
/// ```
#[async_trait]
pub trait WidgetPlugin: Send + Sync + 'static {
    /// Stable widget identifier (must be unique within a plugin registry).
    fn name(&self) -> &str;

    /// Capability declaration — inspected at plugin registration time.
    fn capabilities(&self) -> WidgetPluginCapabilities;

    /// Render the widget and return a `WidgetData` payload.
    ///
    /// Called by the host whenever the GUI panel needs a refresh. Keep this
    /// fast — do not perform blocking I/O or heavy computation here.
    async fn render(&self) -> Result<WidgetData, WidgetPluginError>;
}

// ── Object-safety static assertion ───────────────────────────────────────────

fn _assert_object_safe(_: &dyn WidgetPlugin) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    struct MockClockWidget;

    #[async_trait]
    impl WidgetPlugin for MockClockWidget {
        fn name(&self) -> &str {
            "clock-widget"
        }

        fn capabilities(&self) -> WidgetPluginCapabilities {
            Default::default()
        }

        async fn render(&self) -> Result<WidgetData, WidgetPluginError> {
            Ok(WidgetData {
                title: "Clock".into(),
                body: "UTC: 2026-01-01T00:00:00Z".into(),
                metadata: serde_json::Value::Null,
            })
        }
    }

    #[tokio::test]
    async fn widget_render_happy_path() {
        let widget = MockClockWidget;
        let data = widget.render().await.unwrap();
        assert_eq!(data.title, "Clock");
        assert!(data.body.starts_with("UTC: "));
    }

    #[test]
    fn widget_data_serde_round_trip() {
        let data = WidgetData {
            title: "Test".into(),
            body: "body text".into(),
            metadata: serde_json::json!({ "key": "value" }),
        };
        let json = serde_json::to_string(&data).unwrap();
        let decoded: WidgetData = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.title, "Test");
        assert_eq!(decoded.metadata["key"], "value");
    }

    #[test]
    fn widget_error_serde_round_trip() {
        let err = WidgetPluginError::Internal {
            message: "test error".into(),
        };
        let json = serde_json::to_string(&err).unwrap();
        let decoded: WidgetPluginError = serde_json::from_str(&json).unwrap();
        let msg = decoded.to_string();
        assert!(msg.contains("test error"));
    }

    #[test]
    fn default_capabilities_no_permissions() {
        let caps = WidgetPluginCapabilities::default();
        assert!(!caps.needs_net_outbound);
        assert!(!caps.needs_fs_read);
        assert_eq!(caps.max_memory_mb, 32);
    }

    #[test]
    fn trait_object_usable_as_box() {
        let _widget: Box<dyn WidgetPlugin> = Box::new(MockClockWidget);
    }
}

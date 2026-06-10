//! hello-world — Cascade DataSource plugin example.
//!
//! Purpose: the simplest possible Cascade plugin. Demonstrates the minimal
//!   structure needed to implement a DataSource: one struct, the `#[cascade_plugin]`
//!   attribute, and `fetch_items()` returning a single hard-coded item.
//!
//! This is the right starting point if you want to build a plugin that feeds
//! documents into the Cascade RAG pipeline (GitHub issues, Linear tickets,
//! Notion pages, etc.).
//!
//! To build:  cargo build --target wasm32-wasip1
//! To install: copy target/wasm32-wasip1/debug/hello_world.wasm to ~/.cascade/plugins/
//!
//! Inputs:  `DataSourceFetchArgs` — optional pagination cursor.
//! Outputs: `DataSourcePage` — a list of `DataItem` + optional next cursor.
//! Constraints: no filesystem, network, or environment permissions needed.

// Step 1: Import the three things every plugin needs.
//   - `cascade_plugin` — the attribute macro that generates the WASM ABI entry point.
//   - `Plugin`         — the trait you implement.
//   - `PluginKind`     — the enum variant that declares which ABI surface to expose.
use cascade_pdk::{cascade_plugin, Plugin, PluginKind};

// Step 2: Import the I/O types for your plugin kind (DataSource here).
use cascade_pdk::{DataItem, DataSourceFetchArgs, DataSourcePage, PluginError};

// Step 3: Define your plugin struct.
//   Apply `#[cascade_plugin]` — this generates the `cascade_plugin_call` WASM
//   export, the `alloc`/`dealloc` exports, and wires the dispatch table.
//   The struct itself can be stateless (like this example) or hold config fields.
#[cascade_plugin]
pub struct HelloWorld;

// Step 4: Implement the `Plugin` trait.
impl Plugin for HelloWorld {
    // `name()` must return a stable, unique identifier. This is the name shown
    // in `cascade plugin list` and used as the source prefix in DataItems.
    fn name(&self) -> &str {
        "hello-world"
    }

    // `kind()` tells the host which ABI method to call. For a DataSource,
    // the host calls `fetch_items()` to retrieve documents for RAG ingestion.
    fn kind(&self) -> PluginKind {
        PluginKind::DataSource
    }

    // `fetch_items()` is the only method you need to override for a DataSource.
    //
    // The host calls this in a loop, passing the `next_cursor` from each response
    // as the `args.cursor` for the next call, until `next_cursor` is `None`.
    //
    // In a real plugin you would:
    // - Deserialize `args.cursor` to resume pagination.
    // - Call an external API (requires `net_outbound` capability declared in plugin.json).
    // - Map API responses to `DataItem` structs.
    // - Return a `next_cursor` if more pages are available.
    fn fetch_items(
        &self,
        _args: DataSourceFetchArgs,
    ) -> Result<DataSourcePage, PluginError> {
        // For this example we return a single hard-coded item and no next cursor,
        // which tells the host "this is the only page."
        Ok(DataSourcePage {
            items: vec![DataItem {
                id: String::from("1"),
                title: String::from("Hello, World!"),
                body: String::from("This is a demo plugin."),
                url: String::from("https://cascade.dev/docs/plugins"),
                labels: vec![
                    String::from("demo"),
                    String::from("example"),
                ],
                created_at: String::from("2026-01-01T00:00:00Z"),
                updated_at: String::from("2026-01-01T00:00:00Z"),
                source: String::from("hello-world:demo"),
                extra_json: String::from("{}"),
            }],
            // `None` = no more pages.
            next_cursor: None,
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn name_and_kind() {
        let p = HelloWorld;
        assert_eq!(p.name(), "hello-world");
        assert_eq!(p.kind(), PluginKind::DataSource);
    }

    #[test]
    fn fetch_items_returns_hello_world() {
        let p = HelloWorld;
        let page = p.fetch_items(DataSourceFetchArgs { cursor: None }).unwrap();
        assert_eq!(page.items.len(), 1);
        assert_eq!(page.items[0].title, "Hello, World!");
        assert_eq!(page.items[0].body, "This is a demo plugin.");
        assert!(page.next_cursor.is_none());
    }

    #[test]
    fn fetch_items_source_field() {
        let p = HelloWorld;
        let page = p.fetch_items(DataSourceFetchArgs { cursor: None }).unwrap();
        assert_eq!(page.items[0].source, "hello-world:demo");
    }
}

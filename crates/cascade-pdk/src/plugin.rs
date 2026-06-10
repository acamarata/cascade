//! `Plugin` trait — the core interface every plugin struct must implement.
//!
//! Purpose: define the contract between plugin authors and the Cascade host.
//!   Each method corresponds to one of the five capability categories
//!   (Chunker, Retriever, Provider, ChatTool, Agent). Methods not relevant
//!   to a plugin's declared kind have default implementations that return
//!   `PluginError::NotImplemented`.
//!
//! Constraints: must compile for wasm32-wasip1 (no_std + alloc).
//! SPORT: cascade-pdk / plugin trait (T-P4-E03-10)

use crate::types::{
    AgentContext, AgentResponse, Chunk, ChunkOpts, DataSourceFetchArgs, DataSourcePage, Document,
    EmbedOpts, Embedding, PluginError, PluginKind, RetrievalResult, RetrieveOpts, ToolCall,
    ToolResult, WidgetData,
};

/// Core plugin interface.
///
/// Implement this on your plugin struct and apply the `#[cascade_plugin]`
/// attribute to generate the WASM ABI entry points automatically.
///
/// # Required methods
/// - [`Plugin::name`] — unique plugin identifier.
/// - [`Plugin::kind`] — declares which capability category this plugin implements.
///
/// # Optional capability methods
/// Override only the method(s) matching your plugin's declared kind.
/// All others default to `Err(PluginError::NotImplemented)`.
pub trait Plugin {
    /// Unique identifier for this plugin (e.g. `"my-chunker"`).
    fn name(&self) -> &str;

    /// The capability kind this plugin implements.
    fn kind(&self) -> PluginKind;

    /// Human-readable version string (default: `"0.1.0"`).
    fn version(&self) -> &str {
        "0.1.0"
    }

    // ── Chunker ───────────────────────────────────────────────────────────────

    /// Split `doc` into chunks according to `opts`.
    ///
    /// Override this method if `kind()` returns `PluginKind::Chunker`.
    fn chunk_document(&self, _doc: Document, _opts: ChunkOpts) -> Result<Vec<Chunk>, PluginError> {
        Err(PluginError::NotImplemented)
    }

    // ── Retriever ─────────────────────────────────────────────────────────────

    /// Return the top-K results for `query`.
    ///
    /// Override this method if `kind()` returns `PluginKind::Retriever`.
    fn retrieve(
        &self,
        _query: String,
        _opts: RetrieveOpts,
    ) -> Result<Vec<RetrievalResult>, PluginError> {
        Err(PluginError::NotImplemented)
    }

    // ── Embedding Provider ────────────────────────────────────────────────────

    /// Compute embeddings for a batch of texts.
    ///
    /// Override this method if `kind()` returns `PluginKind::Provider`.
    fn embed(&self, _texts: Vec<String>, _opts: EmbedOpts) -> Result<Vec<Embedding>, PluginError> {
        Err(PluginError::NotImplemented)
    }

    // ── Chat Tool ─────────────────────────────────────────────────────────────

    /// Handle a tool call from the AI assistant.
    ///
    /// Override this method if `kind()` returns `PluginKind::ChatTool`.
    fn on_tool_call(&self, _call: ToolCall) -> Result<ToolResult, PluginError> {
        Err(PluginError::NotImplemented)
    }

    // ── Agent ─────────────────────────────────────────────────────────────────

    /// Process an agent context and produce a response.
    ///
    /// Override this method if `kind()` returns `PluginKind::Agent`.
    fn run_agent(&self, _ctx: AgentContext) -> Result<AgentResponse, PluginError> {
        Err(PluginError::NotImplemented)
    }

    // ── Widget ────────────────────────────────────────────────────────────────

    /// Render this widget and return a `WidgetData` payload for the GUI.
    ///
    /// Override this method if `kind()` returns `PluginKind::Widget`.
    /// Called by the host whenever the widget panel needs a refresh.
    fn render(&self) -> Result<WidgetData, PluginError> {
        Err(PluginError::NotImplemented)
    }

    // ── DataSource ────────────────────────────────────────────────────────────

    /// Fetch a page of items from the external data source.
    ///
    /// Override this method if `kind()` returns `PluginKind::DataSource`.
    ///
    /// `args.cursor` is `None` on the first call; pass the `next_cursor` from
    /// the previous response to advance through pages. Return `next_cursor: None`
    /// when there are no more pages.
    fn fetch_items(&self, _args: DataSourceFetchArgs) -> Result<DataSourcePage, PluginError> {
        Err(PluginError::NotImplemented)
    }
}

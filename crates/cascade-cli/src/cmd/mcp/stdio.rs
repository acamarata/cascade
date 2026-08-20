//! `cascade mcp stdio` — run MCP server in stdio subprocess mode.

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
};

use crate::cmd::Command;

// ── Stdio subcommand ──────────────────────────────────────────────────────────

/// Arguments for `cascade mcp stdio`.
///
/// Starts the MCP server in stdio mode, reading JSON-RPC from stdin and
/// writing responses to stdout. No auth required — the subprocess boundary
/// provides isolation. This is the transport configured by Codex via
/// `codex/config.yaml → mcp.servers[name=cascade]`.
#[derive(Debug, clap::Args)]
pub struct McpStdioArgs {
    /// Override the socket path for daemon connection (for testing).
    #[arg(long, value_name = "PATH")]
    pub socket: Option<PathBuf>,
}

#[async_trait]
impl Command for McpStdioArgs {
    async fn run(&self) -> Result<()> {
        // McpServer holds !Send types (Box<dyn Transport>). We run it on a
        // dedicated single-threaded Tokio runtime in a blocking thread so the
        // outer multi-threaded runtime's Send constraint is satisfied.
        tokio::task::spawn_blocking(|| {
            use cascade_mcp::tool::ToolRegistry;
            use cascade_mcp::transport::stdio::StdioTransport;
            use cascade_mcp::{McpServer, McpServerConfig};
            use cascade_rag::index_manager::IndexManager;
            use cascade_rag::retrieve::rrf::RrfRetriever;
            use tokio::task::LocalSet;

            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .map_err(|e| CascadeError::Other(format!("runtime build error: {e}")))?;

            let local = LocalSet::new();
            rt.block_on(local.run_until(async {
                let config = McpServerConfig::default();
                let transport = StdioTransport::new();

                // Build a registry with an EMPTY retriever slot so the server
                // can serve `initialize` immediately — no index I/O on the
                // critical path (E11.1 / P11 regression fix).
                // T-P7-E15-04: inject the shared production dedup pool
                // (existing Cascade RAG DB — same file IndexManager migrates
                // below, so `context_fingerprints` is guaranteed there).
                // Degrades to dedup-off if the DB cannot be opened.
                let tools = ToolRegistry::new().with_production_db_pool();

                // Clone the retriever slot handle so the background task can
                // inject the real retriever once the index is open.
                let slot = tools.retriever_slot();

                // Spawn the index-open work as a local task so it runs
                // concurrently with the server loop.  On success it writes the
                // retriever into the slot; on failure it logs a warning and
                // leaves the slot None (graceful degradation).
                let config_dir = home_dir().join(".cascade");
                tokio::task::spawn_local(async move {
                    match IndexManager::open(&config_dir).await {
                        Ok(mgr) => match mgr.open_rag_index().await {
                            Ok(idx) => {
                                let retriever: std::sync::Arc<dyn cascade_types::retriever::Retriever> =
                                    std::sync::Arc::new(RrfRetriever::fts_only(idx));
                                let mut guard = slot.write().await;
                                *guard = Some(retriever);
                                tracing::info!("RAG index ready — cascade.search live");
                            }
                            Err(e) => {
                                tracing::warn!(err = %e, "could not open RagIndex — search will return 'index not ready'");
                            }
                        },
                        Err(e) => {
                            tracing::warn!(err = %e, "could not open IndexManager — search will return 'index not ready'");
                        }
                    }
                });

                let server = McpServer::new(config, Box::new(transport)).with_tools(tools);
                tokio::task::spawn_local(server.run())
                    .await
                    .map_err(|e| CascadeError::Other(format!("MCP stdio join error: {e}")))?
                    .map_err(|e| CascadeError::Other(format!("MCP stdio server error: {e}")))
            }))
        })
        .await
        .map_err(|e| CascadeError::Other(format!("spawn_blocking join error: {e}")))?
    }
}

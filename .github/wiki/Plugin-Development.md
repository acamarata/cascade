# Plugin Development

Cascade plugins extend the system with new data sources, AI tools, embedding providers, and autonomous agents. Plugins are compiled to WebAssembly (WASM) and run inside a sandboxed wasmtime host.

This page is an overview. For the complete step-by-step guide including the PDK API reference and full examples, see [Plugin Development Guide](plugin-development-guide.md).

---

## What plugins can do

| Plugin kind | Description |
|---|---|
| `DataSource` | Ingest data from external services (GitHub Issues, Jira, Notion) into the RAG index |
| `ChatTool` | Add callable tools to the MCP server |
| `EmbeddingProvider` | Replace the default BGE-M3 embedder with a custom model |
| `Agent` | Autonomous sub-agent triggered by daemon events or schedules |

---

## How plugins work

Each plugin is a `.wasm` binary paired with a `plugin.json` manifest. The manifest declares:

- Plugin name, version, and kind
- Capabilities it needs (filesystem read, network, keychain read)
- Entry point functions

At startup, the daemon scans `~/.cascade/plugins/` and loads each `.wasm` file. Plugins run in isolated wasmtime sandboxes. Anything not declared in the manifest is denied.

---

## Quick start

Install the PDK and generate a new plugin from the template:

```bash
cargo install cascade-pdk-cli
cascade-pdk new my-plugin --kind data-source
cd my-plugin
cargo build --target wasm32-wasip1 --release
```

Install the built plugin:

```bash
cascade plugin install target/wasm32-wasip1/release/my_plugin.wasm
cascade plugin list
```

---

## Plugin manifest example

```json
{
  "name": "my-plugin",
  "version": "0.1.0",
  "kind": "DataSource",
  "capabilities": ["network"],
  "entrypoints": {
    "ingest": "cascade_ingest"
  }
}
```

---

## Capability model

Capabilities are declared up-front and enforced at runtime. The host denies any call not covered by the manifest.

| Capability | What it allows |
|---|---|
| `filesystem_read` | Read files under the declared path prefix |
| `filesystem_write` | Write files under the declared path prefix |
| `network` | Outbound HTTP requests to declared domains |
| `keychain_read` | Read named secrets from the OS keychain |
| `stdin_stdout` | Read/write stdio (for pipe-based data sources) |

---

## Inspecting a plugin

Before installing an unknown plugin, inspect its manifest:

```bash
cascade plugin inspect ./third-party-plugin.wasm
```

This shows the capabilities it requests without loading the plugin. Only install plugins you trust.

---

## Bundled plugins

These plugins ship with Cascade:

| Plugin | Kind | Source |
|---|---|---|
| `github-issues` | DataSource | GitHub Issues → RAG |
| `gitlab` | DataSource | GitLab Issues + MRs → RAG |
| `jira` | DataSource | Jira tickets → RAG |
| `linear` | DataSource | Linear issues → RAG |

Source and documentation for each: [github-issues](plugins-github-issues.md), [gitlab](plugins-gitlab.md), [jira](plugins-jira.md), [linear](plugins-linear.md).

---

## Full guide

For the complete PDK API, ABI contract, testing harness, CI setup, and publishing instructions, see [Plugin Development Guide](plugin-development-guide.md).

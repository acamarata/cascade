# cascade-cli

The `cascade` command line tool: resolve 6-tier instruction cascades, search
your local RAG index, manage AI provider keys and Gemini pools, apply
templates, run MCP client setup, dispatch cross-repo agent sessions, and
control the `cascaded` daemon.

Part of [Cascade](https://github.com/acamarata/cascade), a FOSS context
manager for AI coding agents.

## Install

```bash
cargo install cascade-cli
```

Or grab a signed binary from the
[releases page](https://github.com/acamarata/cascade/releases).

## Quick start

```bash
cascade status          # daemon + index health
cascade resolve         # print the merged instruction cascade
cascade search "auth"   # hybrid RAG search with citations
cascade template list   # browse bundled templates
cascade mcp setup --all # wire Cascade into your AI tools
```

Full documentation lives in the
[project wiki](https://github.com/acamarata/cascade/wiki).

## License

MIT

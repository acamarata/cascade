# echo-tool — Cascade Plugin Example

**What this demonstrates:** how to write a `ChatTool` plugin — a tool that the
Cascade AI assistant can call by name during a conversation. The tool receives
JSON arguments chosen by the AI and returns a string result.

This example returns the input arguments unchanged (echo). Use it as the
starting point for any plugin that does computation on AI-supplied inputs:
code search, database lookup, file parsing, API calls, etc.

## How it works

A ChatTool plugin implements `on_tool_call(ToolCall) -> Result<ToolResult>`.

- `call.args_json` — JSON string of arguments the AI passed.
- `ToolResult::output` — string returned to the AI as the tool result.

The `plugin.json` manifest declares `"kind": "ChatTool"` and zero permissions
(no filesystem or network access needed for this example).

## How to build

```bash
cargo build --target wasm32-wasip1
```

## How to install

```bash
mkdir -p ~/.cascade/plugins/echo-tool
cp target/wasm32-wasip1/debug/echo_tool.wasm ~/.cascade/plugins/echo-tool/
cp plugin.json ~/.cascade/plugins/echo-tool/
cascade plugin list
```

## How to test

```bash
cargo test
```

## Next steps

- Replace the echo with real logic — parse `call.args_json`, run your code, return a result.
- Register the tool's JSON Schema with the AI via `ToolIntegration::schema()`.
- See `../hello-world/` for a DataSource example.
- See `../clock-widget/` for a Widget example.
- Read the [plugin development guide](https://github.com/acamarata/cascade/wiki/plugin-development-guide).

# Plugin Examples

Self-contained Cascade plugin examples. Each has its own `Cargo.toml`,
`plugin.json`, and `README.md` explaining what it demonstrates.

| Directory | Kind | What it shows |
|---|---|---|
| `hello-world/` | DataSource | Minimum viable plugin — one struct, one method, one hard-coded item. |
| `echo-tool/` | ChatTool | Tool that returns input arguments unchanged — annotated ToolIntegration pattern. |
| `clock-widget/` | Widget | Renders UTC time in a GUI panel — annotated Widget + WASI clock pattern. |

## Quick start

Build any example:

```bash
cd hello-world
cargo build --target wasm32-wasip1
```

Install it:

```bash
mkdir -p ~/.cascade/plugins/hello-world
cp target/wasm32-wasip1/debug/hello_world.wasm ~/.cascade/plugins/hello-world/
cp plugin.json ~/.cascade/plugins/hello-world/
cascade plugin list
```

Run unit tests (no WASM target needed):

```bash
cargo test
```

## Choosing a starting point

- Building a data connector (GitHub, Linear, Notion, etc.)? Start with `hello-world/`.
- Adding a custom AI tool? Start with `echo-tool/`.
- Adding an informational panel to the GUI? Start with `clock-widget/`.

## Further reading

- [Plugin development guide](https://github.com/acamarata/cascade/wiki/plugin-development-guide)
- First-party plugins with full test suites: `../../plugins/echo-tool/` and `../../plugins/clock-widget/`
- PDK source: `../../crates/cascade-pdk/`

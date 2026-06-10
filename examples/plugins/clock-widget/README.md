# clock-widget — Cascade Plugin Example

**What this demonstrates:** how to write a `Widget` plugin — a plugin that
renders a read-only informational panel in the Cascade desktop GUI. The host
polls `render()` on its own schedule and updates the panel automatically.

This example renders the current UTC time using the WASI clock syscall
(`std::time::SystemTime::now()` on wasm32-wasip1) without any external
dependencies.

## How it works

A Widget plugin implements `render() -> Result<WidgetData>`.

- `WidgetData::title` — shown in the panel header.
- `WidgetData::body` — main content rendered in the panel.
- `WidgetData::metadata` — optional JSON for GUI filtering/sorting.

The `plugin.json` manifest declares `"kind": "Widget"` and zero permissions
(only the WASI clock syscall is needed, which is always available).

## How to build

```bash
cargo build --target wasm32-wasip1
```

## How to install

```bash
mkdir -p ~/.cascade/plugins/clock-widget
cp target/wasm32-wasip1/debug/clock_widget.wasm ~/.cascade/plugins/clock-widget/
cp plugin.json ~/.cascade/plugins/clock-widget/
cascade plugin list
```

## How to test

```bash
cargo test
```

## Next steps

- Replace the UTC time with your own data (e.g. build status, weather, metrics).
- If you need network access to fetch data, add `"net_outbound"` to the
  `capabilities` array and declare the host in `permissions.net`.
- For heavy computation, consider caching the result and returning the cached
  value on subsequent polls.
- See `../hello-world/` for a DataSource example.
- See `../echo-tool/` for a ChatTool example.
- Read the [plugin development guide](https://github.com/acamarata/cascade/wiki/plugin-development-guide).

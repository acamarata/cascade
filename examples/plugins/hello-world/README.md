# hello-world — Cascade Plugin Example

**What this demonstrates:** the absolute minimum needed to write a Cascade
DataSource plugin. One struct, one trait, three steps. Start here if you want
to feed documents into Cascade's RAG pipeline (GitHub issues, Linear tickets,
Notion pages, or any other data source).

## How it works

A DataSource plugin implements `fetch_items()`. The host calls it in a loop,
advancing through pages via a cursor, until `next_cursor` is `None`. Each page
returns a list of `DataItem` structs that the RAG pipeline indexes.

This example always returns one hard-coded item and no cursor (single page).

## How to build

```bash
cargo build --target wasm32-wasip1
```

The compiled binary is written to `target/wasm32-wasip1/debug/hello_world.wasm`.

For a release build:

```bash
cargo build --target wasm32-wasip1 --release
```

## How to install

Copy the compiled `.wasm` and `plugin.json` to your Cascade plugins directory:

```bash
mkdir -p ~/.cascade/plugins/hello-world
cp target/wasm32-wasip1/debug/hello_world.wasm ~/.cascade/plugins/hello-world/
cp plugin.json ~/.cascade/plugins/hello-world/
```

Then verify the plugin is visible:

```bash
cascade plugin list
```

## How to test

Run the unit tests on the host (no WASM target needed for unit tests):

```bash
cargo test
```

## Next steps

- Replace the hard-coded `DataItem` with a real API call.
- Add `"net_outbound"` to the `capabilities` array in `plugin.json`.
- Add `net` permissions in `plugin.json` for each host you need to reach.
- See `../echo-tool/` for a ChatTool example.
- See `../clock-widget/` for a Widget example.
- Read the [plugin development guide](https://github.com/acamarata/cascade/wiki/plugin-development-guide).

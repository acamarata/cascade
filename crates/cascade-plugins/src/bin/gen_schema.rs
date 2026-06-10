//! Schema generator — writes `schemas/plugin.schema.json` to stdout.
//!
//! Usage: cargo run -p cascade-plugins --bin gen-schema > crates/cascade-plugins/schemas/plugin.schema.json
//!
//! Purpose: generate the JSON Schema for `plugin.json` using schemars.
//! Inputs: none (schema derived from `PluginJsonManifest` via proc-macro).
//! Outputs: JSON Schema (JSON, UTF-8) on stdout.

fn main() {
    print!("{}", cascade_plugins::manifest::generate_json_schema());
}

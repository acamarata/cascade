//! MCP method handlers — modular, per-primitive implementations.
//!
//! Each submodule owns one MCP primitive (prompts, etc.) and registers
//! its methods with the [`HandlerRegistry`] at server setup time.
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-mcp — handlers/

pub mod prompts;

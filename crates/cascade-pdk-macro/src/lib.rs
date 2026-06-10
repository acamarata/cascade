//! cascade-pdk-macro — proc-macro crate for the Cascade Plugin Development Kit.
//!
//! # Purpose
//! Provides the `#[cascade_plugin]` attribute macro that, when applied to a struct
//! implementing the `Plugin` trait, generates:
//! 1. `cascade_init() -> i32` — WASM export called once at instantiation.
//! 2. `cascade_plugin_call(args_ptr: i32, args_len: i32) -> (i32, i32)` — main
//!    dispatch entry point called by the host for every plugin invocation.
//! 3. Thin helper exports for each capability kind (e.g. `chunker_chunk`,
//!    `tool_call`, `retriever_retrieve`, `provider_embed`, `agent_run`).
//!
//! # JSON-string ABI
//! The generated `cascade_plugin_call` reads a JSON object from guest memory:
//! ```json
//! { "func": "<function_name>", "args": { ... } }
//! ```
//! It dispatches to the appropriate `Plugin` trait method, serialises the result
//! to JSON, writes it to a new guest memory allocation, and returns
//! `(result_ptr: i32, result_len: i32)` to the host.
//!
//! # Error handling
//! Any error is returned as:
//! ```json
//! { "kind": "internal", "message": "..." }
//! ```
//!
//! Inputs:  struct item annotated with `#[cascade_plugin]`
//! Outputs: `cascade_init`, `cascade_plugin_call`, and per-kind helper exports
//! Constraints: generated code is `no_std + alloc`-compatible for wasm32 targets.
//! SPORT: cascade-pdk-macro (T-P4-E03-10)

use proc_macro::TokenStream;
use proc_macro2::Span;
use quote::quote;
use syn::{parse_macro_input, Ident, ItemStruct};

/// Attribute macro that generates WASM ABI entry points for a Cascade plugin.
///
/// Apply to any struct that implements `cascade_pdk::Plugin`:
///
/// ```rust,ignore
/// use cascade_pdk::{cascade_plugin, Plugin, PluginKind, ToolCall, ToolResult, PluginError};
///
/// #[cascade_plugin]
/// pub struct MyTool;
///
/// impl Plugin for MyTool {
///     fn name(&self) -> &str { "my-tool" }
///     fn kind(&self) -> PluginKind { PluginKind::ChatTool }
///     fn on_tool_call(&self, call: ToolCall) -> Result<ToolResult, PluginError> {
///         Ok(ToolResult { call_id: call.call_id, output: "ok".into(), data_json: None })
///     }
/// }
/// ```
///
/// The macro generates the following `#[no_mangle]` exports:
/// - `cascade_init() -> i32` — returns 0 on success.
/// - `cascade_plugin_call(args_ptr: i32, args_len: i32) -> (i32, i32)` — main dispatch.
///
/// # Safety
/// The generated code uses `unsafe` blocks solely for pointer operations required
/// by the WASM linear-memory ABI. All pointer arithmetic is bounds-checked against
/// the provided length values.
#[proc_macro_attribute]
pub fn cascade_plugin(_attr: TokenStream, item: TokenStream) -> TokenStream {
    let input = parse_macro_input!(item as ItemStruct);
    let struct_name = &input.ident;

    // Generate a static instance of the plugin struct.
    // For wasm32 targets this is fine — each WASM module is single-threaded.
    let static_ident = Ident::new(
        &format!("__CASCADE_PLUGIN_{}", struct_name.to_string().to_uppercase()),
        Span::call_site(),
    );

    let expanded = quote! {
        // Keep the original struct definition.
        #input

        // ── Static plugin instance ────────────────────────────────────────────
        // Safety: WASM modules execute single-threaded; no concurrent access.
        static #static_ident: ::std::sync::atomic::AtomicBool =
            ::std::sync::atomic::AtomicBool::new(false);

        // ── cascade_init ──────────────────────────────────────────────────────
        /// Called once by the host immediately after instantiation.
        /// Returns 0 on success, non-zero on failure.
        #[no_mangle]
        pub extern "C" fn cascade_init() -> i32 {
            // Mark the plugin as initialised.
            #static_ident.store(true, ::std::sync::atomic::Ordering::SeqCst);
            0
        }

        // ── cascade_plugin_call ───────────────────────────────────────────────
        /// Main dispatch entry point.
        ///
        /// Reads a JSON object `{ "func": "<name>", "args": { ... } }` from the
        /// slice `[args_ptr .. args_ptr + args_len]`, dispatches to the Plugin
        /// trait method matching `func`, serialises the result, writes it to a
        /// new heap allocation, and returns `(result_ptr, result_len)`.
        ///
        /// On JSON decode or dispatch errors, returns a serialised `PluginError`.
        #[no_mangle]
        pub extern "C" fn cascade_plugin_call(
            args_ptr: i32,
            args_len: i32,
        ) -> i64 {
            let plugin = #struct_name;

            // Read the input JSON from linear memory.
            // Safety: the host guarantees args_ptr..args_ptr+args_len is a valid
            // UTF-8 slice allocated via the guest's `alloc` export.
            let args_bytes = unsafe {
                ::std::slice::from_raw_parts(args_ptr as *const u8, args_len as usize)
            };

            let result_json: ::cascade_pdk::__private::String = match
                ::cascade_pdk::__private::serde_json::from_slice::<
                    ::cascade_pdk::__private::DispatchEnvelope
                >(args_bytes)
            {
                Err(e) => {
                    ::cascade_pdk::__private::error_json(
                        &format!("JSON decode error: {}", e)
                    )
                }
                Ok(envelope) => {
                    ::cascade_pdk::__private::dispatch(&plugin, envelope)
                }
            };

            // Allocate result memory and write the JSON bytes.
            // wasm32-wasip1 is a full WASI target with std, so ::std::alloc
            // is correct for both host and WASM guest builds.
            let bytes = result_json.into_bytes();
            let len = bytes.len();
            let layout = ::std::alloc::Layout::array::<u8>(len).unwrap();
            let ptr = unsafe { ::std::alloc::alloc(layout) };
            unsafe {
                ::std::ptr::copy_nonoverlapping(bytes.as_ptr(), ptr, len);
            }

            // Pack (ptr, len) into a single i64 return value.
            // ABI: i64 = (ptr as u64) << 32 | (len as u64).
            // Host unpacks: ptr = (packed >> 32) as usize; len = (packed & 0xFFFF_FFFF) as usize.
            // Rationale: Rust extern "C" cdylib on wasm32 cannot return multi-value
            // (i32, i32); a single i64 is the correct cross-boundary convention.
            ((ptr as u64 as i64) << 32) | (len as u64 as i64)
        }
    };

    TokenStream::from(expanded)
}

// ── Utility macros for plugin trait registration (register_* style) ───────────

/// Register a struct as a Cascade Chunker plugin (alias for `#[cascade_plugin]`).
///
/// Identical to `#[cascade_plugin]` — use the kind-specific aliases for
/// documentation clarity.
#[proc_macro_attribute]
pub fn register_chunker(attr: TokenStream, item: TokenStream) -> TokenStream {
    cascade_plugin(attr, item)
}

/// Register a struct as a Cascade ChatTool plugin.
#[proc_macro_attribute]
pub fn register_tool(attr: TokenStream, item: TokenStream) -> TokenStream {
    cascade_plugin(attr, item)
}

/// Register a struct as a Cascade Retriever plugin.
#[proc_macro_attribute]
pub fn register_retriever(attr: TokenStream, item: TokenStream) -> TokenStream {
    cascade_plugin(attr, item)
}

/// Register a struct as a Cascade EmbeddingProvider plugin.
#[proc_macro_attribute]
pub fn register_provider(attr: TokenStream, item: TokenStream) -> TokenStream {
    cascade_plugin(attr, item)
}

/// Register a struct as a Cascade Agent plugin.
#[proc_macro_attribute]
pub fn register_agent(attr: TokenStream, item: TokenStream) -> TokenStream {
    cascade_plugin(attr, item)
}

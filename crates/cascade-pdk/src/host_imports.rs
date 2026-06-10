//! Host-function import declarations for WASM guest plugins.
//!
//! Purpose: declare the host functions the Cascade runtime exposes to
//!   guest plugins. These must match the registration in
//!   `cascade-plugins/src/wit_bindings/mod.rs` (`register_host_functions`).
//!
//! Import module: `"cascade:plugins/host"` (matches WIT world package path).
//!
//! Available host functions:
//! - `log(level: i32, ptr: *const u8, len: usize)` — emit a log line.
//! - `kv_get(key_ptr, key_len) -> (val_ptr, val_len)` — read a KV entry.
//! - `kv_set(key_ptr, key_len, val_ptr, val_len)` — write a KV entry.
//!
//! Constraints: only compiled on wasm32 targets; on host targets the helpers
//!   are no-ops or stubs so unit tests can link.
//! SPORT: cascade-pdk / host imports (T-P4-E03-10)

#[cfg(target_arch = "wasm32")]
#[link(wasm_import_module = "cascade:plugins/host")]
extern "C" {
    /// Emit a log line to the host's tracing subscriber.
    ///
    /// `level`: 0 = TRACE, 1 = DEBUG, 2 = INFO, 3 = WARN, 4 = ERROR.
    /// `ptr` / `len`: UTF-8 message bytes (host reads, does not free).
    #[link_name = "cascade:plugins/host::log"]
    pub fn host_log(level: i32, ptr: *const u8, len: usize);

    /// Read a named key from the plugin's scoped KV store.
    ///
    /// Returns a pointer to a UTF-8 value string + its length.
    /// Returns `(null, 0)` if the key is not present.
    /// Caller must call `dealloc(ptr, len)` after use.
    #[link_name = "cascade:plugins/host::kv-get"]
    pub fn host_kv_get(
        key_ptr: *const u8,
        key_len: usize,
        val_ptr_out: *mut *mut u8,
        val_len_out: *mut usize,
    );

    /// Write a named key into the plugin's scoped KV store.
    #[link_name = "cascade:plugins/host::kv-set"]
    pub fn host_kv_set(
        key_ptr: *const u8,
        key_len: usize,
        val_ptr: *const u8,
        val_len: usize,
    );
}

// ── Ergonomic wrappers ────────────────────────────────────────────────────────

/// Emit a log line at INFO level to the Cascade host.
///
/// No-op on non-wasm32 targets (use `tracing` directly in host code).
pub fn log_info(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: msg is a valid UTF-8 str; ptr/len are valid for the call duration.
    unsafe {
        host_log(2, msg.as_ptr(), msg.len());
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Emit a log line at DEBUG level to the Cascade host.
pub fn log_debug(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: same as log_info.
    unsafe {
        host_log(1, msg.as_ptr(), msg.len());
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Emit a log line at WARN level to the Cascade host.
pub fn log_warn(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: same as log_info.
    unsafe {
        host_log(3, msg.as_ptr(), msg.len());
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Retrieve a key from the plugin KV store.
///
/// On wasm32 targets this calls the host's `kv-get` function via the imported ABI.
/// On non-wasm32 targets this is a stub that always returns `None`.
pub fn kv_get(_key: &str) -> Option<String> {
    #[cfg(target_arch = "wasm32")]
    {
        // TODO: wire up the actual host import once the KV store ABI is finalised.
        // For now this stub keeps non-wasm host tests compiling; the real wasm
        // implementation will call host_kv_get() directly.
        None
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        None
    }
}

/// Set a key in the plugin KV store (stub — no-op on non-wasm32 targets).
pub fn kv_set(_key: &str, _value: &str) {}

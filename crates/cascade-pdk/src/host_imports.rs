//! Host-function import declarations for WASM guest plugins.
//!
//! Purpose: declare the host functions the Cascade runtime exposes to
//!   guest plugins. These must match the registration in
//!   `cascade-plugins/src/sandbox.rs` (`register_cascade_host_functions`).
//!
//! Import module: `"cascade:plugins/host"` (matches WIT world package path).
//!
//! # Available host functions
//!
//! - `log(level: i32, ptr: i32, len: i32)` — emit a log line to the host tracer.
//! - `kv-get(key_ptr: i32, key_len: i32, out_ptr: i32) -> i32` — read a KV entry.
//! - `kv-set(key_ptr: i32, key_len: i32, val_ptr: i32, val_len: i32)` — write a KV entry.
//!
//! # kv-get ABI
//!
//! `kv-get(key_ptr, key_len, out_ptr) -> i32`
//!
//! The host reads `key[0..key_len]` from guest memory, looks up the key, and if
//! found writes the UTF-8 value bytes into guest memory at `out_ptr`. Returns
//! the value byte length (0 = key not found or out_ptr out of bounds).
//!
//! The caller must pre-allocate the output buffer (`alloc(MAX_VAL_LEN)`) before
//! calling kv_get and dealloc it after use. The ergonomic `kv_get` wrapper below
//! handles this automatically using a 4096-byte stack buffer.
//!
//! WHY single out_ptr rather than (val_ptr, val_len) return:
//! WASM multi-value returns require explicit ABI support not available via
//! `extern "C"` in Rust cdylib. A caller-allocated output buffer + i32 length
//! return is the simplest correct convention.
//!
//! Constraints: only compiled on wasm32 targets; on host targets the helpers
//!   are no-ops or stubs so unit tests can link without cross-compilation.
//! SPORT: cascade-pdk / host imports (T-P4-E03-10)

#[cfg(target_arch = "wasm32")]
#[link(wasm_import_module = "cascade:plugins/host")]
extern "C" {
    /// Emit a log line to the host's tracing subscriber.
    ///
    /// `level`: 0 = TRACE, 1 = DEBUG, 2 = INFO, 3 = WARN, 4 = ERROR.
    /// `ptr` / `len`: UTF-8 message bytes in guest linear memory.
    /// Host reads the bytes during the call; caller retains ownership.
    fn host_log(level: i32, ptr: i32, len: i32);

    /// Read a named key from the plugin's per-invocation scoped KV store.
    ///
    /// Writes the UTF-8 value bytes into guest memory at `out_ptr` and
    /// returns the byte length. Returns 0 if the key is not present or if
    /// `out_ptr + value_len` exceeds WASM linear memory bounds.
    ///
    /// The caller must allocate an output buffer of sufficient size before
    /// calling this function. The `kv_get` ergonomic wrapper does this via
    /// a fixed 4096-byte stack buffer — values longer than 4095 bytes are
    /// silently truncated at the host side (host writes up to `out_buf.len()`).
    fn host_kv_get(key_ptr: i32, key_len: i32, out_ptr: i32) -> i32;

    /// Write a named key into the plugin's per-invocation scoped KV store.
    ///
    /// Values written here are visible to subsequent `kv-get` calls within
    /// the same invocation only (per-invocation scope, not persistent).
    fn host_kv_set(key_ptr: i32, key_len: i32, val_ptr: i32, val_len: i32);
}

// ── Ergonomic wrappers ────────────────────────────────────────────────────────

/// Emit a log line at INFO level to the Cascade host.
///
/// No-op on non-wasm32 targets (use `tracing` directly in host code).
pub fn log_info(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: msg is a valid UTF-8 str; ptr/len are in-bounds for the call duration.
    unsafe {
        host_log(2, msg.as_ptr() as i32, msg.len() as i32);
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Emit a log line at DEBUG level to the Cascade host.
pub fn log_debug(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: same as log_info.
    unsafe {
        host_log(1, msg.as_ptr() as i32, msg.len() as i32);
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Emit a log line at WARN level to the Cascade host.
pub fn log_warn(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: same as log_info.
    unsafe {
        host_log(3, msg.as_ptr() as i32, msg.len() as i32);
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Retrieve a key from the plugin's per-invocation KV store.
///
/// Returns `Some(value)` if the key exists, `None` if not found.
///
/// # Value size limit
/// Values longer than 4095 bytes will be truncated to fit the internal stack
/// buffer. If you need larger values, use the raw `host_kv_get` import directly
/// with a heap-allocated buffer. This limit is intentional — KV is designed for
/// small configuration values, not binary blobs.
///
/// # Non-wasm32 behavior
/// Returns `None` on non-wasm32 targets (host tests run without the KV store
/// wired to a real HashMap; the plugin's unit tests use the `test_harness`
/// module instead).
pub fn kv_get(key: &str) -> Option<String> {
    #[cfg(target_arch = "wasm32")]
    {
        // Stack-allocated output buffer: 4096 bytes covers typical config values.
        // WHY stack rather than heap: no allocator overhead; single call into host.
        let mut buf = [0u8; 4096];
        // Safety:
        //   - key.as_ptr() points to valid UTF-8 bytes for the duration of the call.
        //   - buf.as_mut_ptr() is a valid guest memory pointer in bounds.
        //   - host_kv_get writes at most `buf.len()` bytes (host bounds-checks).
        let val_len = unsafe {
            host_kv_get(
                key.as_ptr() as i32,
                key.len() as i32,
                buf.as_mut_ptr() as i32,
            )
        };
        if val_len <= 0 {
            return None;
        }
        let val_len = val_len as usize;
        // Clamp to buffer size in case host wrote exactly 4096 bytes.
        let val_len = val_len.min(buf.len());
        String::from_utf8(buf[..val_len].to_vec()).ok()
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = key;
        None
    }
}

/// Set a key in the plugin's per-invocation KV store.
///
/// The value is visible to subsequent `kv_get` calls within the same invocation.
/// Values do NOT persist across calls.
///
/// No-op on non-wasm32 targets.
pub fn kv_set(key: &str, value: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: key and value are valid UTF-8 str slices; ptrs are valid for call duration.
    unsafe {
        host_kv_set(
            key.as_ptr() as i32,
            key.len() as i32,
            value.as_ptr() as i32,
            value.len() as i32,
        );
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = (key, value);
    }
}

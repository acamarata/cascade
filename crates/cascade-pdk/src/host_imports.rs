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

// ── ABI conversion helpers (T-P7-E25-04) ──────────────────────────────────────
//
// The host ABI passes pointers and lengths as `i32`. On wasm32 both are really
// `u32`, so any value above `i32::MAX` wraps to a NEGATIVE number. The host
// now rejects negatives outright, but a guest that silently sends one turns a
// too-long string into a confusing "out of bounds" instead of a clear refusal.
//
// These helpers are deliberately NOT `cfg(target_arch = "wasm32")`: keeping
// them target-independent is what makes them testable on the host, where the
// rest of this module compiles away.

/// Convert a length to the `i32` the host ABI expects.
///
/// Returns `None` when the length cannot be represented — the caller then
/// skips the host call rather than passing a wrapped negative value.
// Callers are the wasm32-only wrappers below; on a host build only the
// tests reach these, so the lib target sees them as unused.
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
pub(crate) fn abi_len(len: usize) -> Option<i32> {
    i32::try_from(len).ok()
}

/// Convert a guest pointer to the `i32` the host ABI expects.
///
/// Returns `None` for an address above `i32::MAX`. Cascade caps plugin linear
/// memory well below 2 GiB, so this should be unreachable in practice; it is
/// checked anyway because "should be unreachable" is not a bounds check.
// Callers are the wasm32-only wrappers below; on a host build only the
// tests reach these, so the lib target sees them as unused.
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
pub(crate) fn abi_ptr(addr: usize) -> Option<i32> {
    i32::try_from(addr).ok()
}

// ── Ergonomic wrappers ────────────────────────────────────────────────────────

/// Emit a log line at INFO level to the Cascade host.
///
/// No-op on non-wasm32 targets (use `tracing` directly in host code).
pub fn log_info(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: msg is a valid UTF-8 str; ptr/len are in-bounds for the call duration.
    unsafe {
        // A message whose pointer or length will not fit the i32 ABI is
        // dropped rather than sent as a wrapped negative value.
        if let (Some(ptr), Some(len)) = (abi_ptr(msg.as_ptr() as usize), abi_len(msg.len())) {
            host_log(2, ptr, len);
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Emit a log line at DEBUG level to the Cascade host.
pub fn log_debug(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: same as log_info.
    unsafe {
        // A message whose pointer or length will not fit the i32 ABI is
        // dropped rather than sent as a wrapped negative value.
        if let (Some(ptr), Some(len)) = (abi_ptr(msg.as_ptr() as usize), abi_len(msg.len())) {
            host_log(1, ptr, len);
        }
    }
    #[cfg(not(target_arch = "wasm32"))]
    let _ = msg;
}

/// Emit a log line at WARN level to the Cascade host.
pub fn log_warn(msg: &str) {
    #[cfg(target_arch = "wasm32")]
    // Safety: same as log_info.
    unsafe {
        // A message whose pointer or length will not fit the i32 ABI is
        // dropped rather than sent as a wrapped negative value.
        if let (Some(ptr), Some(len)) = (abi_ptr(msg.as_ptr() as usize), abi_len(msg.len())) {
            host_log(3, ptr, len);
        }
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
        let (Some(key_ptr), Some(key_len), Some(out_ptr)) = (
            abi_ptr(key.as_ptr() as usize),
            abi_len(key.len()),
            abi_ptr(buf.as_mut_ptr() as usize),
        ) else {
            // Unrepresentable in the ABI — treat as "not found" rather than
            // calling the host with a wrapped negative value.
            return None;
        };
        let val_len = unsafe { host_kv_get(key_ptr, key_len, out_ptr) };
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
        if let (Some(kp), Some(kl), Some(vp), Some(vl)) = (
            abi_ptr(key.as_ptr() as usize),
            abi_len(key.len()),
            abi_ptr(value.as_ptr() as usize),
            abi_len(value.len()),
        ) {
            host_kv_set(kp, kl, vp, vl);
        }
        // Otherwise the pair cannot be expressed in the ABI; skip the call
        // rather than storing under a wrapped pointer.
    }
    #[cfg(not(target_arch = "wasm32"))]
    {
        let _ = (key, value);
    }
}

#[cfg(test)]
mod abi_conversion_tests {
    use super::{abi_len, abi_ptr};

    // These cover the GUEST end of the FFI boundary (T-P7-E25-04). The wrappers
    // themselves are wasm32-only, but the conversion logic they depend on is
    // not, so the refusal behaviour is testable on the host.

    #[test]
    fn ordinary_lengths_and_pointers_convert() {
        assert_eq!(abi_len(0), Some(0));
        assert_eq!(abi_len(4096), Some(4096));
        assert_eq!(abi_ptr(0), Some(0));
        assert_eq!(abi_ptr(1 << 20), Some(1 << 20));
    }

    #[test]
    fn the_largest_representable_value_still_converts() {
        let max = i32::MAX as usize;
        assert_eq!(abi_len(max), Some(i32::MAX));
        assert_eq!(abi_ptr(max), Some(i32::MAX));
    }

    #[test]
    fn values_above_i32_max_are_refused_not_wrapped() {
        // The bug this replaces: `len as i32` turns 0x8000_0000 into
        // -2147483648 and hands the host a negative length.
        let over = (i32::MAX as usize) + 1;
        assert_eq!(abi_len(over), None, "must refuse, not wrap to a negative");
        assert_eq!(abi_ptr(over), None, "must refuse, not wrap to a negative");
        assert_eq!(abi_len(usize::MAX), None);
        assert_eq!(abi_ptr(usize::MAX), None);
    }

    #[test]
    fn no_input_ever_produces_a_negative_abi_value() {
        for candidate in [
            0usize,
            1,
            4095,
            i32::MAX as usize - 1,
            i32::MAX as usize,
            i32::MAX as usize + 1,
            u32::MAX as usize,
            usize::MAX,
        ] {
            if let Some(v) = abi_len(candidate) {
                assert!(v >= 0, "abi_len({candidate}) produced a negative {v}");
            }
            if let Some(v) = abi_ptr(candidate) {
                assert!(v >= 0, "abi_ptr({candidate}) produced a negative {v}");
            }
        }
    }
}

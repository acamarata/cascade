//! Windows Credential Manager keychain implementation using the `windows` crate.
//!
//! # Purpose
//! Wraps the Windows Credential Manager API via `windows::Win32::Security::Credentials`
//! to provide OS-backed credential storage. Compiled and active only on `target_os = "windows"`.
//!
//! # Security invariant
//! Secret values MUST NOT appear in any tracing span, log message, or error string.
//! Only service/account names and Win32 error codes may appear.
//!
//! # Constraints
//! - Requires Windows Vista or later (Credential Manager available by default).
//! - Credentials are stored as CRED_TYPE_GENERIC.
//!
//! # SPORT
//! MASTER-SECURITY.md: os-keychain-crate / WindowsKeychain

use std::ffi::OsStr;
use std::os::windows::ffi::OsStrExt;

use windows::core::PWSTR;
use windows::Win32::Foundation::{ERROR_NOT_FOUND, FILETIME};
use windows::Win32::Security::Credentials::{
    CredDeleteW, CredFree, CredReadW, CredWriteW, CREDENTIALW, CRED_FLAGS,
    CRED_PERSIST_LOCAL_MACHINE, CRED_TYPE_GENERIC,
};

use crate::{Keychain, KeychainError};

/// Windows Credential Manager keychain implementation.
///
/// # Purpose
/// Stores credentials in the Windows Credential Manager as CRED_TYPE_GENERIC entries.
/// `service` and `account` are combined into a single target name used for lookup.
///
/// # Inputs / Outputs
/// Matches the `Keychain` trait contract. `service/account` form the credential target name.
///
/// # Constraints
/// - Only compiled on `target_os = "windows"`.
/// - Secret values never appear in tracing or error output.
///
/// # SPORT
/// MASTER-SECURITY.md: os-keychain-crate / WindowsKeychain
pub struct WindowsKeychain;

impl WindowsKeychain {
    /// Create a new Windows Credential Manager handle.
    pub fn new() -> Self {
        Self
    }

    /// Build the target name `"cascade/<service>/<account>"` as a wide (UTF-16) string.
    fn target_name(service: &str, account: &str) -> Vec<u16> {
        let s = format!("cascade/{service}/{account}");
        OsStr::new(&s)
            .encode_wide()
            .chain(std::iter::once(0u16))
            .collect()
    }

    /// Build a service-prefix `"cascade/<service>/"` wide string for listing.
    fn service_prefix(service: &str) -> String {
        format!("cascade/{service}/")
    }
}

impl Default for WindowsKeychain {
    fn default() -> Self {
        Self::new()
    }
}

impl Keychain for WindowsKeychain {
    fn get_key(&self, service: &str, account: &str) -> Result<String, KeychainError> {
        tracing::debug!(service, account, "Windows keychain: get_key");
        let target = Self::target_name(service, account);
        let mut pcredential = std::ptr::null_mut();
        // SAFETY: target is a valid null-terminated wide string; pcredential receives the pointer.
        let result = unsafe {
            CredReadW(
                PWSTR(target.as_ptr() as *mut u16),
                CRED_TYPE_GENERIC,
                0,
                &mut pcredential,
            )
        };
        if let Err(e) = result {
            let code = e.code();
            if code == ERROR_NOT_FOUND.to_hresult() {
                return Err(KeychainError::NotFound);
            }
            tracing::debug!(service, account, win32_error_code = ?code, "Windows CredReadW error");
            return Err(KeychainError::Other(format!("CredReadW error: {code:?}")));
        }
        // SAFETY: CredReadW succeeded; pcredential is valid; we free it with CredFree.
        let secret = unsafe {
            let cred = &*pcredential;
            let blob =
                std::slice::from_raw_parts(cred.CredentialBlob, cred.CredentialBlobSize as usize);
            let result = String::from_utf8(blob.to_vec())
                .map_err(|_| KeychainError::Other("stored secret is not valid UTF-8".to_owned()));
            CredFree(pcredential as *mut std::ffi::c_void);
            result
        };
        // Secret returned to caller only — never logged.
        secret
    }

    fn set_key(&self, service: &str, account: &str, secret: &str) -> Result<(), KeychainError> {
        tracing::debug!(service, account, "Windows keychain: set_key");
        let target = Self::target_name(service, account);
        let account_wide: Vec<u16> = OsStr::new(account)
            .encode_wide()
            .chain(std::iter::once(0u16))
            .collect();
        let secret_bytes = secret.as_bytes();
        let cred = CREDENTIALW {
            Flags: CRED_FLAGS(0),
            Type: CRED_TYPE_GENERIC,
            TargetName: PWSTR(target.as_ptr() as *mut u16),
            Comment: PWSTR::null(),
            LastWritten: FILETIME::default(),
            CredentialBlobSize: secret_bytes.len() as u32,
            // Secret bytes passed to OS; value is not logged.
            CredentialBlob: secret_bytes.as_ptr() as *mut u8,
            Persist: CRED_PERSIST_LOCAL_MACHINE,
            AttributeCount: 0,
            Attributes: std::ptr::null_mut(),
            TargetAlias: PWSTR::null(),
            UserName: PWSTR(account_wide.as_ptr() as *mut u16),
        };
        // SAFETY: cred is fully initialized; CredWriteW takes a const pointer.
        unsafe { CredWriteW(&cred, 0) }.map_err(|e| {
            let code = e.code();
            tracing::debug!(service, account, win32_error_code = ?code, "Windows CredWriteW error");
            KeychainError::Other(format!("CredWriteW error: {code:?}"))
        })
    }

    fn delete_key(&self, service: &str, account: &str) -> Result<(), KeychainError> {
        tracing::debug!(service, account, "Windows keychain: delete_key");
        let target = Self::target_name(service, account);
        // SAFETY: target is a valid null-terminated wide string.
        unsafe {
            CredDeleteW(
                PWSTR(target.as_ptr() as *mut u16),
                CRED_TYPE_GENERIC,
                0,
            )
        }
        .map_err(|e| {
            let code = e.code();
            if code == ERROR_NOT_FOUND.to_hresult() {
                return KeychainError::NotFound;
            }
            tracing::debug!(service, account, win32_error_code = ?code, "Windows CredDeleteW error");
            KeychainError::Other(format!("CredDeleteW error: {code:?}"))
        })
    }

    fn list_keys(&self, service: &str) -> Result<Vec<String>, KeychainError> {
        tracing::debug!(service, "Windows keychain: list_keys");
        // Windows Credential Manager's CredEnumerateW supports a filter prefix.
        // We filter by "cascade/<service>/" and strip the prefix to return account names.
        use windows::Win32::Security::Credentials::{
            CredEnumerateW, CRED_ENUMERATE_FLAGS, CREDENTIALW as CW,
        };
        let prefix = Self::service_prefix(service);
        let prefix_wide: Vec<u16> = OsStr::new(&prefix)
            .encode_wide()
            .chain(std::iter::once(0u16))
            .collect();
        let mut count: u32 = 0;
        let mut ppcredentials: *mut *mut CW = std::ptr::null_mut();
        // SAFETY: prefix_wide is a valid null-terminated wide string.
        let result = unsafe {
            CredEnumerateW(
                PWSTR(prefix_wide.as_ptr() as *mut u16),
                CRED_ENUMERATE_FLAGS(0),
                &mut count,
                &mut ppcredentials,
            )
        };
        if let Err(e) = result {
            let code = e.code();
            if code == ERROR_NOT_FOUND.to_hresult() {
                return Ok(vec![]);
            }
            return Err(KeychainError::Other(format!(
                "CredEnumerateW error: {code:?}"
            )));
        }
        // SAFETY: CredEnumerateW succeeded; ppcredentials is valid; we free with CredFree.
        let accounts = unsafe {
            let slice = std::slice::from_raw_parts(ppcredentials, count as usize);
            let result: Vec<String> = slice
                .iter()
                .filter_map(|&pcred| {
                    let cred = &*pcred;
                    if cred.TargetName.is_null() {
                        return None;
                    }
                    // Decode wide TargetName. Format: "cascade/<service>/<account>"
                    let target_ptr = cred.TargetName.0;
                    let mut len = 0usize;
                    while *target_ptr.add(len) != 0 {
                        len += 1;
                    }
                    let wide = std::slice::from_raw_parts(target_ptr, len);
                    let target_str = String::from_utf16_lossy(wide);
                    // Strip prefix to recover account name.
                    target_str.strip_prefix(&prefix).map(|s| s.to_owned())
                })
                .collect();
            CredFree(ppcredentials as *mut std::ffi::c_void);
            result
        };
        Ok(accounts)
    }
}

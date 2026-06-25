use cascade_types::auto_auth::{AuthType, DiscoveredAccount, ImportResult};

/// Import selected `DiscoveredAccount` entries into cascade-keychain.
///
/// # Purpose
/// For each `importable` `EnvApiKey` account in `selected`, reads the env var
/// value and stores it in cascade-keychain under the appropriate provider key.
/// Non-importable accounts are skipped with a note.
///
/// # Inputs
/// - `selected`: the accounts the user chose to import.
///
/// # Outputs
/// `ImportResult` with imported/skipped/errors lists.
///
/// # Constraints
/// - Key values are NEVER logged.
/// - Keychain writes via `cascade_keychain::platform_keychain()`.
pub async fn import_accounts(selected: Vec<DiscoveredAccount>) -> ImportResult {
    use cascade_keychain::{platform_keychain, Keychain};

    let kc: Box<dyn Keychain> = platform_keychain();
    let mut imported = Vec::new();
    let mut skipped = Vec::new();
    let mut errors = Vec::new();

    for account in selected {
        if !account.importable {
            skipped.push(account.email_or_hint.clone());
            continue;
        }

        match account.auth_type {
            AuthType::EnvApiKey => {
                // Extract variable name from hint "env: VAR_NAME"
                let var_name = account
                    .email_or_hint
                    .strip_prefix("env: ")
                    .unwrap_or(&account.email_or_hint);

                match std::env::var(var_name) {
                    Ok(key_value) if !key_value.is_empty() => {
                        // Store under "dev.cascade" service, keyed by provider name
                        let account_key = format!("{}-api-key", account.provider);
                        match kc.set_key("dev.cascade", &account_key, &key_value) {
                            Ok(()) => imported.push(account.email_or_hint.clone()),
                            Err(e) => {
                                // SECURITY: key_value is NOT included in the error message
                                errors.push(format!(
                                    "keychain write failed for {}: {}",
                                    account.provider, e
                                ));
                            }
                        }
                        // SECURITY: key_value is dropped here without logging
                        drop(key_value);
                    }
                    Ok(_) => {
                        errors.push(format!("env var {} is empty", var_name));
                    }
                    Err(_) => {
                        errors.push(format!("env var {} not set at import time", var_name));
                    }
                }
            }
            _ => {
                skipped.push(account.email_or_hint.clone());
            }
        }
    }

    ImportResult {
        imported,
        skipped,
        errors,
    }
}

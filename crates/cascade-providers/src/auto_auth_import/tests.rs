// tests.rs is itself declared `mod tests;` by the parent module; the inner
// wrapper keeps the cfg(test) scoping self-contained in this file.
#[allow(clippy::module_inception)]
#[cfg(test)]
mod tests {
    use super::super::{
        import::import_accounts,
        scanners::{
            scan_all, scan_antigravity, scan_claude_code, scan_codex, scan_env_vars, scan_opencode,
        },
    };
    use cascade_types::auto_auth::{AuthSource, AuthType, DiscoveredAccount};
    use serial_test::serial;
    use std::io::Write;
    use tempfile::TempDir;

    // Helper: write a file relative to a temp dir
    fn write_file(dir: &TempDir, relative: &str, content: &str) {
        let path = dir.path().join(relative);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).unwrap();
        }
        let mut f = std::fs::File::create(&path).unwrap();
        f.write_all(content.as_bytes()).unwrap();
    }

    // ── CC scan ──────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_cc_detects_email_in_settings_json() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".claude/settings.json",
            r#"{"email":"claude@example.com","model":"claude-3-5-sonnet"}"#,
        );

        // Override HOME for this test
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_claude_code();

        // Restore
        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(!results.is_empty(), "expected at least one CC account");
        let acc = &results[0];
        assert_eq!(acc.source, AuthSource::ClaudeCode);
        assert_eq!(acc.email_or_hint, "claude@example.com");
        assert_eq!(acc.provider, "anthropic");
        assert!(
            !acc.importable,
            "CC OAuth tokens are never directly importable"
        );
    }

    #[test]
    #[serial(global_env)]
    fn scan_cc_skips_when_no_settings_file() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_claude_code();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty(), "no settings.json → no accounts");
    }

    #[test]
    #[serial(global_env)]
    fn scan_cc_skips_malformed_credentials() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        // settings.json exists (so CC is "installed") but .credentials is garbage
        write_file(
            &tmp,
            ".claude/settings.json",
            r#"{"model":"claude-3-5-sonnet"}"#,
        );
        write_file(&tmp, ".claude/.credentials", "NOT_VALID_JSON{{{");

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        // Should not panic; returns account with "unknown" hint
        let results = scan_claude_code();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        // settings.json exists → we emit a placeholder
        assert_eq!(results.len(), 1);
        assert!(results[0].email_or_hint.contains("unknown"));
    }

    // ── OpenCode scan ────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_opencode_detects_providers() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".config/opencode/config.json",
            r#"{"providers":{"anthropic":{"email":"oc@example.com"},"openai":{}}}"#,
        );

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_opencode();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(!results.is_empty(), "expected OpenCode providers");
        assert!(results.iter().any(|a| a.source == AuthSource::OpenCode));
        let anthropic = results.iter().find(|a| a.provider == "anthropic");
        assert!(anthropic.is_some());
        assert_eq!(anthropic.unwrap().email_or_hint, "oc@example.com");
    }

    #[test]
    #[serial(global_env)]
    fn scan_opencode_skips_malformed_json() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(&tmp, ".config/opencode/config.json", "{NOT JSON}}}");

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_opencode();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty(), "malformed config → skip, no panic");
    }

    // ── Codex scan ───────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_codex_detects_email() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(&tmp, ".codex/auth.json", r#"{"email":"codex@example.com"}"#);

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_codex();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert_eq!(results.len(), 1);
        assert_eq!(results[0].source, AuthSource::Codex);
        assert_eq!(results[0].email_or_hint, "codex@example.com");
        assert_eq!(results[0].provider, "openai");
        assert!(!results[0].importable);
    }

    #[test]
    #[serial(global_env)]
    fn scan_codex_skips_when_absent() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_codex();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty());
    }

    // ── Env var scan ─────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_env_vars_detects_anthropic_key() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-test-key");

        let results = scan_env_vars();

        std::env::remove_var("ANTHROPIC_API_KEY");

        assert_eq!(results.len(), 1);
        let acc = &results[0];
        assert_eq!(acc.source, AuthSource::EnvVar);
        assert_eq!(acc.provider, "anthropic");
        assert_eq!(acc.auth_type, AuthType::EnvApiKey);
        assert!(acc.importable);
        assert_eq!(acc.email_or_hint, "env: ANTHROPIC_API_KEY");
        // SECURITY: key value "sk-ant-test-key" must NOT appear in email_or_hint
        assert!(!acc.email_or_hint.contains("sk-ant"));
    }

    #[test]
    #[serial(global_env)]
    fn scan_env_vars_empty_when_none_set() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("ANTHROPIC_API_KEY");
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");

        let results = scan_env_vars();

        // Nothing set → empty
        assert!(
            results.is_empty(),
            "expected empty when no API keys are set: {:?}",
            results
        );
    }

    #[test]
    #[serial(global_env)]
    fn scan_env_vars_detects_multiple_keys() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-1");
        std::env::set_var("OPENAI_API_KEY", "sk-openai-1");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");

        let results = scan_env_vars();

        std::env::remove_var("ANTHROPIC_API_KEY");
        std::env::remove_var("OPENAI_API_KEY");

        assert_eq!(results.len(), 2);
        assert!(results.iter().all(|a| a.importable));
    }

    // ── Aggregate ────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_all_returns_vec_no_panic() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        // With no env vars and empty HOME-equivalent, should return empty without panic.
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());
        std::env::remove_var("ANTHROPIC_API_KEY");
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");

        let results = scan_all();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        // May be empty — that's valid; just must not panic
        let _ = results;
    }

    // ── JWT decode helper ────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn decode_jwt_email_extracts_email() {
        use super::super::helpers::decode_jwt_email;
        // Build a minimal JWT-shaped token: base64url(header).base64url(payload).sig
        use base64::Engine;
        let payload = r#"{"sub":"user123","email":"jwt@example.com","iat":1234567890}"#;
        let encoded_payload =
            base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(payload.as_bytes());
        let token = format!("eyJhbGciOiJIUzI1NiJ9.{}.fake_signature", encoded_payload);

        let result = decode_jwt_email(&token);
        assert_eq!(result, Some("jwt@example.com".to_string()));
    }

    #[test]
    #[serial(global_env)]
    fn decode_jwt_email_handles_malformed_token() {
        use super::super::helpers::decode_jwt_email;
        assert_eq!(decode_jwt_email("not.a.jwt.with.garbage"), None);
        assert_eq!(decode_jwt_email(""), None);
        assert_eq!(decode_jwt_email("nodots"), None);
    }

    // ── Import (in-memory keychain) ───────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn import_env_api_key_stores_in_keychain() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        // This test uses the in-memory keychain via platform_keychain() in CI.
        // We verify that the function path runs without panic when the env var is present.
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-test-import");

        let account = DiscoveredAccount {
            source: AuthSource::EnvVar,
            email_or_hint: "env: ANTHROPIC_API_KEY".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::EnvApiKey,
            importable: true,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(import_accounts(vec![account]));

        std::env::remove_var("ANTHROPIC_API_KEY");

        // Should be imported (or error if running as root without keychain)
        assert!(
            result.imported.len() == 1 || !result.errors.is_empty(),
            "expected imported=1 or an error string; got {:?}",
            result
        );
        assert!(result.skipped.is_empty());
        // SECURITY: the key value must NOT appear in the result strings
        assert!(!result.imported.join("").contains("sk-ant-test-import"));
        assert!(!result.errors.join("").contains("sk-ant-test-import"));
    }

    #[test]
    #[serial(global_env)]
    fn import_non_importable_goes_to_skipped() {
        let account = DiscoveredAccount {
            source: AuthSource::ClaudeCode,
            email_or_hint: "cc@example.com".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(import_accounts(vec![account]));

        assert!(result.imported.is_empty());
        assert_eq!(result.skipped.len(), 1);
        assert!(result.errors.is_empty());
    }

    // ── Serde shape (camelCase) ───────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn discovered_account_serde_camel_case() {
        let account = DiscoveredAccount {
            source: AuthSource::ClaudeCode,
            email_or_hint: "test@example.com".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        };
        let json = serde_json::to_string(&account).unwrap();
        assert!(
            json.contains("\"emailOrHint\""),
            "expected camelCase emailOrHint, got: {json}"
        );
        assert!(
            json.contains("\"authType\""),
            "expected camelCase authType, got: {json}"
        );
        assert!(
            json.contains("\"claudeCode\""),
            "expected claudeCode source, got: {json}"
        );
    }

    // ── Antigravity scan ─────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_antigravity_detects_email_in_config_json() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".config/antigravity/config.json",
            r#"{"email":"ag@example.com","token":"tok123"}"#,
        );

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_antigravity();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(!results.is_empty(), "expected antigravity account");
        let acc = &results[0];
        assert_eq!(acc.source, AuthSource::Antigravity);
        assert_eq!(acc.email_or_hint, "ag@example.com");
        assert!(!acc.importable);
    }

    #[test]
    #[serial(global_env)]
    fn scan_antigravity_returns_empty_when_not_installed() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_antigravity();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty(), "no config → no accounts");
    }

    #[test]
    #[serial(global_env)]
    fn scan_antigravity_fallback_dot_antigravity() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".antigravity/config.json",
            r#"{"userEmail":"ag2@example.com"}"#,
        );

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_antigravity();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(
            !results.is_empty(),
            "expected antigravity via ~/.antigravity"
        );
        assert_eq!(results[0].email_or_hint, "ag2@example.com");
    }

    #[test]
    #[serial(global_env)]
    fn scan_all_includes_antigravity() {
        let _env_guard = crate::test_helpers::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".config/antigravity/config.json",
            r#"{"email":"ag@example.com"}"#,
        );

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_all();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        let has_ag = results.iter().any(|a| a.source == AuthSource::Antigravity);
        assert!(has_ag, "scan_all must include Antigravity results");
    }
}

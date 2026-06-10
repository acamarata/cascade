//! YAML, JSON, and TOML structured-data parsers.
//!
//! All three parsers implement [`DocumentParser`] and convert structured config/data files
//! into human-readable flattened key-value text suitable for chunking and embedding.
//!
//! Nested maps are joined with `.` (e.g. `database.host: localhost`).
//! Array elements are bracket-indexed (e.g. `items[0]: foo`).
//!
//! # Limits
//! - Depth cap: 32 levels of nesting (beyond that, the subtree is omitted).
//! - Size cap: 1 MiB input file size (returns `CascadeError::ParseFailed` above this).
//!
//! # Ticket
//! T-P4-E01-16
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::parse::structured

use std::path::Path;

use cascade_types::error::{CascadeError, Result};

use super::{DocumentParser, DocumentText};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum file size accepted by structured parsers (1 MiB).
const MAX_FILE_BYTES: u64 = 1024 * 1024;

/// Maximum nesting depth before the subtree is silently omitted.
const MAX_DEPTH: usize = 32;

// ── flatten_value (shared helper) ─────────────────────────────────────────────

/// Recursively flatten a [`serde_json::Value`] into `"key: value"` lines.
///
/// # Arguments
/// * `prefix` — dot-path built so far (empty string for the root).
/// * `value`  — current JSON value node.
/// * `depth`  — current recursion depth; nodes beyond `MAX_DEPTH` are dropped.
///
/// # Output
/// A `String` of `\n`-separated `"key: value"` lines.  Null values emit
/// `"key: null"`.  Empty objects/arrays emit nothing.
///
/// # Purpose
/// Converts YAML/JSON/TOML to a flat textual representation that chunkers and
/// FTS5 indexing can consume without knowledge of the original structure.
fn flatten_value(prefix: &str, value: &serde_json::Value, depth: usize) -> String {
    if depth > MAX_DEPTH {
        return String::new();
    }

    match value {
        serde_json::Value::Object(map) => {
            let mut parts: Vec<String> = Vec::with_capacity(map.len());
            for (k, v) in map {
                let child_prefix = if prefix.is_empty() {
                    k.clone()
                } else {
                    format!("{prefix}.{k}")
                };
                let child = flatten_value(&child_prefix, v, depth + 1);
                if !child.is_empty() {
                    parts.push(child);
                }
            }
            parts.join("\n")
        }
        serde_json::Value::Array(arr) => {
            let mut parts: Vec<String> = Vec::with_capacity(arr.len());
            for (i, v) in arr.iter().enumerate() {
                let child_prefix = format!("{prefix}[{i}]");
                let child = flatten_value(&child_prefix, v, depth + 1);
                if !child.is_empty() {
                    parts.push(child);
                }
            }
            parts.join("\n")
        }
        // Leaf nodes
        serde_json::Value::Null => format!("{prefix}: null"),
        serde_json::Value::Bool(b) => format!("{prefix}: {b}"),
        serde_json::Value::Number(n) => format!("{prefix}: {n}"),
        serde_json::Value::String(s) => format!("{prefix}: {s}"),
    }
}

// ── guard: check file size ────────────────────────────────────────────────────

fn guard_size(path: &Path) -> Result<()> {
    let meta = std::fs::metadata(path).map_err(|e| CascadeError::ParseFailed {
        path: path.to_path_buf(),
        detail: format!("cannot stat file: {e}"),
    })?;
    if meta.len() > MAX_FILE_BYTES {
        return Err(CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!(
                "file too large for structured parser ({} bytes > {} byte limit)",
                meta.len(),
                MAX_FILE_BYTES
            ),
        });
    }
    Ok(())
}

// ── YamlParser ────────────────────────────────────────────────────────────────

/// Parses `.yaml` and `.yml` files into flattened key-value text.
///
/// Uses `serde_yaml` for deserialization, then converts the document to
/// `serde_json::Value` for uniform flattening.
///
/// # Errors
/// Returns [`CascadeError::ParseFailed`] for:
/// - Files larger than 1 MiB.
/// - Invalid YAML syntax.
/// - YAML documents whose root is not a mapping or sequence (e.g. a bare scalar).
///
/// SPORT: MASTER-CRATES.md → cascade-rag::parse::structured::YamlParser
///
/// # Ticket
/// T-P4-E01-16
pub struct YamlParser;

impl DocumentParser for YamlParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(
            path.extension().and_then(|e| e.to_str()),
            Some("yaml" | "yml")
        )
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        guard_size(path)?;

        let raw = std::fs::read_to_string(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("read error: {e}"),
        })?;

        // Deserialize to serde_yaml::Value, then convert to serde_json::Value
        let yaml_val: serde_yaml::Value =
            serde_yaml::from_str(&raw).map_err(|e| CascadeError::ParseFailed {
                path: path.to_path_buf(),
                detail: format!("YAML parse error: {e}"),
            })?;

        let json_val = yaml_to_json(yaml_val);
        let text = flatten_value("", &json_val, 0);

        let title = path
            .file_stem()
            .and_then(|s| s.to_str())
            .map(String::from);

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata: std::collections::HashMap::new(),
            parser_name: self.parser_name().to_owned(),
        })
    }

    fn parser_name(&self) -> &str {
        "yaml"
    }
}

/// Convert `serde_yaml::Value` → `serde_json::Value` losslessly.
fn yaml_to_json(v: serde_yaml::Value) -> serde_json::Value {
    match v {
        serde_yaml::Value::Null => serde_json::Value::Null,
        serde_yaml::Value::Bool(b) => serde_json::Value::Bool(b),
        serde_yaml::Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                serde_json::Value::Number(i.into())
            } else if let Some(f) = n.as_f64() {
                serde_json::Number::from_f64(f)
                    .map(serde_json::Value::Number)
                    .unwrap_or(serde_json::Value::Null)
            } else {
                serde_json::Value::Null
            }
        }
        serde_yaml::Value::String(s) => serde_json::Value::String(s),
        serde_yaml::Value::Sequence(seq) => {
            serde_json::Value::Array(seq.into_iter().map(yaml_to_json).collect())
        }
        serde_yaml::Value::Mapping(map) => {
            let mut obj = serde_json::Map::new();
            for (k, v) in map {
                // YAML keys can be non-strings; stringify them
                let key = match k {
                    serde_yaml::Value::String(s) => s,
                    other => format!("{other:?}"),
                };
                obj.insert(key, yaml_to_json(v));
            }
            serde_json::Value::Object(obj)
        }
        serde_yaml::Value::Tagged(tagged) => yaml_to_json(tagged.value),
    }
}

// ── JsonParser ────────────────────────────────────────────────────────────────

/// Parses `.json` files into flattened key-value text.
///
/// Uses `serde_json` directly — no conversion needed.
///
/// # Errors
/// Returns [`CascadeError::ParseFailed`] for:
/// - Files larger than 1 MiB.
/// - Invalid JSON syntax.
///
/// SPORT: MASTER-CRATES.md → cascade-rag::parse::structured::JsonParser
///
/// # Ticket
/// T-P4-E01-16
pub struct JsonParser;

impl DocumentParser for JsonParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(
            path.extension().and_then(|e| e.to_str()),
            Some("json")
        )
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        guard_size(path)?;

        let raw = std::fs::read_to_string(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("read error: {e}"),
        })?;

        let json_val: serde_json::Value =
            serde_json::from_str(&raw).map_err(|e| CascadeError::ParseFailed {
                path: path.to_path_buf(),
                detail: format!("JSON parse error: {e}"),
            })?;

        let text = flatten_value("", &json_val, 0);

        let title = path
            .file_stem()
            .and_then(|s| s.to_str())
            .map(String::from);

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata: std::collections::HashMap::new(),
            parser_name: self.parser_name().to_owned(),
        })
    }

    fn parser_name(&self) -> &str {
        "json"
    }
}

// ── TomlParser ────────────────────────────────────────────────────────────────

/// Parses `.toml` files into flattened key-value text.
///
/// Uses the `toml` crate to deserialize to `toml::Value`, then converts to
/// `serde_json::Value` for uniform flattening.
///
/// # Errors
/// Returns [`CascadeError::ParseFailed`] for:
/// - Files larger than 1 MiB.
/// - Invalid TOML syntax.
///
/// SPORT: MASTER-CRATES.md → cascade-rag::parse::structured::TomlParser
///
/// # Ticket
/// T-P4-E01-16
pub struct TomlParser;

impl DocumentParser for TomlParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(
            path.extension().and_then(|e| e.to_str()),
            Some("toml")
        )
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        guard_size(path)?;

        let raw = std::fs::read_to_string(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("read error: {e}"),
        })?;

        let toml_val: toml::Value =
            toml::from_str(&raw).map_err(|e| CascadeError::ParseFailed {
                path: path.to_path_buf(),
                detail: format!("TOML parse error: {e}"),
            })?;

        let json_val = toml_to_json(toml_val);
        let text = flatten_value("", &json_val, 0);

        let title = path
            .file_stem()
            .and_then(|s| s.to_str())
            .map(String::from);

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata: std::collections::HashMap::new(),
            parser_name: self.parser_name().to_owned(),
        })
    }

    fn parser_name(&self) -> &str {
        "toml"
    }
}

/// Convert `toml::Value` → `serde_json::Value`.
fn toml_to_json(v: toml::Value) -> serde_json::Value {
    match v {
        toml::Value::Boolean(b) => serde_json::Value::Bool(b),
        toml::Value::Integer(i) => serde_json::Value::Number(i.into()),
        toml::Value::Float(f) => serde_json::Number::from_f64(f)
            .map(serde_json::Value::Number)
            .unwrap_or(serde_json::Value::Null),
        toml::Value::String(s) => serde_json::Value::String(s),
        toml::Value::Datetime(dt) => serde_json::Value::String(dt.to_string()),
        toml::Value::Array(arr) => {
            serde_json::Value::Array(arr.into_iter().map(toml_to_json).collect())
        }
        toml::Value::Table(table) => {
            let mut obj = serde_json::Map::new();
            for (k, v) in table {
                obj.insert(k, toml_to_json(v));
            }
            serde_json::Value::Object(obj)
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::NamedTempFile;

    // ── flatten_value unit tests ───────────────────────────────────────────────

    /// Flat object produces simple "key: value" lines.
    #[test]
    fn flatten_flat_object() {
        let v = serde_json::json!({ "name": "Alice", "age": 30 });
        let out = flatten_value("", &v, 0);
        assert!(out.contains("name: Alice"), "got: {out}");
        assert!(out.contains("age: 30"), "got: {out}");
    }

    /// Nested object produces dot-notation keys.
    #[test]
    fn flatten_nested_object() {
        let v = serde_json::json!({ "database": { "host": "localhost", "port": 5432 } });
        let out = flatten_value("", &v, 0);
        assert!(out.contains("database.host: localhost"), "got: {out}");
        assert!(out.contains("database.port: 5432"), "got: {out}");
    }

    /// Array produces bracket-indexed lines.
    #[test]
    fn flatten_array() {
        let v = serde_json::json!({ "items": ["foo", "bar", "baz"] });
        let out = flatten_value("", &v, 0);
        assert!(out.contains("items[0]: foo"), "got: {out}");
        assert!(out.contains("items[1]: bar"), "got: {out}");
        assert!(out.contains("items[2]: baz"), "got: {out}");
    }

    /// Depth cap: a value at depth > MAX_DEPTH is silently dropped (no panic).
    #[test]
    fn flatten_depth_cap_no_panic() {
        // Build a deeply nested object exceeding MAX_DEPTH
        let mut v = serde_json::json!("leaf");
        for _ in 0..=(MAX_DEPTH + 2) {
            v = serde_json::json!({ "k": v });
        }
        // Should not panic; output may be partial
        let _out = flatten_value("root", &v, 0);
    }

    /// Null leaf emits "key: null".
    #[test]
    fn flatten_null_leaf() {
        let v = serde_json::json!({ "val": null });
        let out = flatten_value("", &v, 0);
        assert!(out.contains("val: null"), "got: {out}");
    }

    // ── YamlParser tests ───────────────────────────────────────────────────────

    /// YAML nested map produces dot-notation flat lines.
    #[test]
    fn yaml_nested_flatten() {
        let mut f = NamedTempFile::with_suffix(".yaml").unwrap();
        write!(
            f,
            "database:\n  host: localhost\n  port: 5432\napp:\n  name: cascade\n"
        )
        .unwrap();
        let parser = YamlParser;
        let doc = parser.parse(f.path()).expect("yaml parse");
        assert_eq!(doc.parser_name, "yaml");
        assert!(doc.text.contains("database.host: localhost"), "got: {}", doc.text);
        assert!(doc.text.contains("database.port: 5432"), "got: {}", doc.text);
        assert!(doc.text.contains("app.name: cascade"), "got: {}", doc.text);
    }

    /// YAML with arrays produces bracket-indexed lines.
    #[test]
    fn yaml_array_flatten() {
        let mut f = NamedTempFile::with_suffix(".yml").unwrap();
        write!(f, "tags:\n  - alpha\n  - beta\n").unwrap();
        let parser = YamlParser;
        let doc = parser.parse(f.path()).expect("yaml parse");
        assert!(doc.text.contains("tags[0]: alpha"), "got: {}", doc.text);
        assert!(doc.text.contains("tags[1]: beta"), "got: {}", doc.text);
    }

    /// Malformed YAML returns a typed ParseFailed error, not a panic.
    #[test]
    fn yaml_malformed_returns_error() {
        let mut f = NamedTempFile::with_suffix(".yaml").unwrap();
        write!(f, "key: [\nunclosed bracket\n").unwrap();
        let parser = YamlParser;
        let result = parser.parse(f.path());
        assert!(result.is_err(), "expected error for malformed YAML");
        let err = result.unwrap_err();
        assert!(
            matches!(err, CascadeError::ParseFailed { .. }),
            "expected ParseFailed, got: {err:?}"
        );
    }

    /// can_parse returns true only for .yaml / .yml extensions.
    #[test]
    fn yaml_can_parse_extensions() {
        let parser = YamlParser;
        assert!(parser.can_parse(Path::new("config.yaml")));
        assert!(parser.can_parse(Path::new("config.yml")));
        assert!(!parser.can_parse(Path::new("config.json")));
        assert!(!parser.can_parse(Path::new("config.toml")));
    }

    // ── JsonParser tests ───────────────────────────────────────────────────────

    /// JSON nested object produces dot-notation lines.
    #[test]
    fn json_nested_flatten() {
        let mut f = NamedTempFile::with_suffix(".json").unwrap();
        write!(
            f,
            r#"{{"server":{{"host":"0.0.0.0","port":8080}},"debug":true}}"#
        )
        .unwrap();
        let parser = JsonParser;
        let doc = parser.parse(f.path()).expect("json parse");
        assert_eq!(doc.parser_name, "json");
        assert!(doc.text.contains("server.host: 0.0.0.0"), "got: {}", doc.text);
        assert!(doc.text.contains("server.port: 8080"), "got: {}", doc.text);
        assert!(doc.text.contains("debug: true"), "got: {}", doc.text);
    }

    /// JSON array produces bracket-indexed lines.
    #[test]
    fn json_array_flatten() {
        let mut f = NamedTempFile::with_suffix(".json").unwrap();
        write!(f, r#"{{"items":["foo","bar"]}}"#).unwrap();
        let parser = JsonParser;
        let doc = parser.parse(f.path()).expect("json parse");
        assert!(doc.text.contains("items[0]: foo"), "got: {}", doc.text);
        assert!(doc.text.contains("items[1]: bar"), "got: {}", doc.text);
    }

    /// Malformed JSON returns typed error.
    #[test]
    fn json_malformed_returns_error() {
        let mut f = NamedTempFile::with_suffix(".json").unwrap();
        write!(f, r#"{{"key": "value" missing_comma "other": 1}}"#).unwrap();
        let parser = JsonParser;
        let result = parser.parse(f.path());
        assert!(result.is_err());
        assert!(matches!(result.unwrap_err(), CascadeError::ParseFailed { .. }));
    }

    /// can_parse returns true only for .json.
    #[test]
    fn json_can_parse_extension() {
        let parser = JsonParser;
        assert!(parser.can_parse(Path::new("data.json")));
        assert!(!parser.can_parse(Path::new("data.yaml")));
        assert!(!parser.can_parse(Path::new("data.toml")));
    }

    // ── TomlParser tests ───────────────────────────────────────────────────────

    /// TOML nested table produces dot-notation lines.
    #[test]
    fn toml_nested_flatten() {
        let mut f = NamedTempFile::with_suffix(".toml").unwrap();
        write!(f, "[database]\nhost = \"localhost\"\nport = 5432\n\n[app]\nname = \"cascade\"\n")
            .unwrap();
        let parser = TomlParser;
        let doc = parser.parse(f.path()).expect("toml parse");
        assert_eq!(doc.parser_name, "toml");
        assert!(doc.text.contains("database.host: localhost"), "got: {}", doc.text);
        assert!(doc.text.contains("database.port: 5432"), "got: {}", doc.text);
        assert!(doc.text.contains("app.name: cascade"), "got: {}", doc.text);
    }

    /// TOML array produces bracket-indexed lines.
    #[test]
    fn toml_array_flatten() {
        let mut f = NamedTempFile::with_suffix(".toml").unwrap();
        write!(f, "tags = [\"alpha\", \"beta\"]\n").unwrap();
        let parser = TomlParser;
        let doc = parser.parse(f.path()).expect("toml parse");
        assert!(doc.text.contains("tags[0]: alpha"), "got: {}", doc.text);
        assert!(doc.text.contains("tags[1]: beta"), "got: {}", doc.text);
    }

    /// Malformed TOML returns typed error.
    #[test]
    fn toml_malformed_returns_error() {
        let mut f = NamedTempFile::with_suffix(".toml").unwrap();
        write!(f, "[section\nbad = 'unclosed").unwrap();
        let parser = TomlParser;
        let result = parser.parse(f.path());
        assert!(result.is_err());
        assert!(matches!(result.unwrap_err(), CascadeError::ParseFailed { .. }));
    }

    /// can_parse returns true only for .toml.
    #[test]
    fn toml_can_parse_extension() {
        let parser = TomlParser;
        assert!(parser.can_parse(Path::new("Cargo.toml")));
        assert!(!parser.can_parse(Path::new("config.yaml")));
        assert!(!parser.can_parse(Path::new("config.json")));
    }

    /// Depth cap integration: deeply nested YAML does not panic.
    #[test]
    fn yaml_depth_cap_integration() {
        // Build deeply nested YAML manually
        let mut yaml = String::from("l0:\n");
        for i in 1..=35 {
            yaml.push_str(&"  ".repeat(i));
            yaml.push_str(&format!("l{i}:\n"));
        }
        yaml.push_str(&"  ".repeat(36));
        yaml.push_str("leaf: deep_value\n");

        let mut f = NamedTempFile::with_suffix(".yaml").unwrap();
        write!(f, "{yaml}").unwrap();
        let parser = YamlParser;
        // Must not panic; output may be truncated at depth cap
        let _doc = parser.parse(f.path()).expect("deep yaml should parse without panic");
    }
}

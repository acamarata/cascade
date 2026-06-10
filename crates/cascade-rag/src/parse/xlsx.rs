//! XLSX/XLS parser backed by `calamine` (pure Rust, MIT).
//!
//! ## Strategy
//!
//! 1. Open with `calamine::open_workbook_auto(path)` (auto-detects `.xlsx` / `.xls`).
//! 2. For each sheet: prepend `## Sheet: {name}`, then format rows as tab-separated.
//! 3. Join all sheets with `\n\n`.
//! 4. Title: first sheet name, fallback to filename stem.
//! 5. Metadata: `sheet_names` (comma-joined), `row_count` (total rows across sheets).
//!
//! ## Cell formatting
//!
//! | calamine `Data` variant | Output |
//! |---|---|
//! | `String(s)` | verbatim string |
//! | `Float(f)` | decimal string |
//! | `Int(i)` | integer string |
//! | `Bool(b)` | `"true"` / `"false"` |
//! | `Empty` | `""` (empty field) |
//! | `Error(_)` | `""` (silently skipped) |
//!
//! ## Known limitations
//!
//! - `calamine` loads the full sheet range into memory.  Workbooks with millions
//!   of rows may cause high memory usage.  For large sheets, consider pre-filtering
//!   or raising the `ROW_CAP` constant before use in production.
//! - Formulas are not extracted (evaluated cell values only).
//! - Charts and embedded images are ignored.
//!
//! ## Cargo feature
//!
//! Enable with `cascade-rag = { features = ["xlsx-parser"] }`.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::parse::XlsxParser
//!
//! # Ticket
//! T-P4-E01-19

use std::collections::HashMap;
use std::path::Path;

use calamine::{open_workbook_auto, Data, Reader};
use cascade_types::error::{CascadeError, Result};
use tracing::debug;

use crate::parse::{DocumentParser, DocumentText};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum rows extracted per sheet.  Sheets exceeding this limit are truncated
/// with a trailing `[truncated: N rows omitted]` note in the output.
///
/// Rationale: prevents runaway memory usage on large spreadsheets fed into RAG.
/// Adjust upward if typical workbooks contain more rows.
const ROW_CAP: usize = 10_000;

// ── XlsxParser ───────────────────────────────────────────────────────────────

/// XLSX / XLS spreadsheet parser using `calamine`.
///
/// Converts spreadsheet content to human-readable, tab-separated text suitable
/// for the RAG chunking pipeline.
///
/// # Purpose
/// Extract structured cell data from `.xlsx` and `.xls` files as plain text.
///
/// # Inputs
/// A filesystem path to an `.xlsx` or `.xls` file.
///
/// # Outputs
/// [`DocumentText`] with:
/// - `text`: per-sheet sections prefixed with `## Sheet: {name}`, rows as TSV
/// - `title`: first sheet name, or filename stem
/// - `metadata`: `sheet_names` (comma-joined), `row_count` (total rows)
/// - `parser_name`: `"xlsx"`
///
/// # Constraints
/// - Full sheet data loaded into memory; per-sheet row cap: `ROW_CAP` (10 000).
/// - Formulas not extracted; evaluated cell values only.
/// - Charts and images ignored.
///
/// # SPORT
/// MASTER-CRATES.md → cascade-rag::parse::XlsxParser
#[derive(Debug, Default)]
pub struct XlsxParser;

impl DocumentParser for XlsxParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(
            path.extension().and_then(|e| e.to_str()),
            Some("xlsx") | Some("xls")
        )
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        debug!(path = %path.display(), "parsing XLSX/XLS");

        let mut workbook = open_workbook_auto(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("calamine open error: {e}"),
        })?;

        // Snapshot sheet names before borrowing workbook mutably for ranges.
        let sheet_names: Vec<String> = workbook.sheet_names().to_vec();

        let mut sections: Vec<String> = Vec::with_capacity(sheet_names.len());
        let mut total_rows: usize = 0;

        for sheet_name in &sheet_names {
            // In calamine 0.24, worksheet_range returns Result<Range<Data>, Error>
            // directly (not Option); errors are surfaced immediately.
            let range = match workbook.worksheet_range(sheet_name) {
                Ok(r) => r,
                Err(e) => {
                    // Unreadable sheet: emit a note and continue with remaining sheets.
                    sections.push(format!(
                        "## Sheet: {sheet_name}\n[error reading sheet: {e}]"
                    ));
                    continue;
                }
            };

            let mut lines: Vec<String> = Vec::new();
            let mut row_count = 0usize;

            for row in range.rows() {
                if row_count >= ROW_CAP {
                    let remaining = range.height().saturating_sub(ROW_CAP);
                    lines.push(format!("[truncated: {remaining} rows omitted]"));
                    break;
                }
                let line = row
                    .iter()
                    .map(format_cell)
                    .collect::<Vec<_>>()
                    .join("\t");
                lines.push(line);
                row_count += 1;
            }

            total_rows += row_count;

            let section = format!("## Sheet: {sheet_name}\n{}", lines.join("\n"));
            sections.push(section);
        }

        let text = sections.join("\n\n");

        // Title: first sheet name or filename stem.
        let title = sheet_names
            .first()
            .cloned()
            .or_else(|| {
                path.file_stem()
                    .and_then(|s| s.to_str())
                    .map(String::from)
            });

        // Metadata.
        let mut metadata: HashMap<String, String> = HashMap::new();
        metadata.insert("sheet_names".into(), sheet_names.join(", "));
        metadata.insert("row_count".into(), total_rows.to_string());

        debug!(
            path = %path.display(),
            sheets = sheet_names.len(),
            total_rows,
            chars = text.len(),
            "XLSX parsed"
        );

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata,
            parser_name: "xlsx".into(),
        })
    }

    fn parser_name(&self) -> &str {
        "xlsx"
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Format a single cell value as a string.
///
/// Returns an empty string for empty cells and silently skips formula errors,
/// so that downstream tab-separated fields remain consistent in width.
fn format_cell(cell: &Data) -> String {
    match cell {
        Data::String(s) => s.clone(),
        Data::Float(f) => f.to_string(),
        Data::Int(i) => i.to_string(),
        Data::Bool(b) => b.to_string(),
        Data::Empty => String::new(),
        Data::Error(_) => String::new(),
        // DateTime, DateTimeIso, DurationIso variants: use Display impl
        _ => cell.to_string(),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn fixtures_dir() -> PathBuf {
        let manifest = std::env::var("CARGO_MANIFEST_DIR").unwrap();
        PathBuf::from(manifest).join("tests/fixtures")
    }

    /// T-P4-E01-19: Two-sheet fixture produces correct section headers and TSV rows.
    #[test]
    fn xlsx_simple_fixture_roundtrip() {
        let parser = XlsxParser;
        let path = fixtures_dir().join("simple.xlsx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        assert_eq!(result.parser_name, "xlsx");
        assert!(
            result.text.contains("## Sheet: Data"),
            "should have Data sheet header; got: {:?}",
            result.text
        );
        assert!(
            result.text.contains("## Sheet: Summary"),
            "should have Summary sheet header"
        );
        assert!(
            result.text.contains("Alice"),
            "should contain cell value Alice"
        );
        assert!(
            result.text.contains("Bob"),
            "should contain cell value Bob"
        );
    }

    /// T-P4-E01-19: sheet_names metadata is comma-joined sheet names.
    #[test]
    fn xlsx_sheet_names_metadata() {
        let parser = XlsxParser;
        let path = fixtures_dir().join("simple.xlsx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        let sheet_names = result.metadata.get("sheet_names").expect("sheet_names metadata");
        assert!(
            sheet_names.contains("Data"),
            "sheet_names should include Data: {sheet_names}"
        );
        assert!(
            sheet_names.contains("Summary"),
            "sheet_names should include Summary: {sheet_names}"
        );
    }

    /// T-P4-E01-19: Title is the first sheet name.
    #[test]
    fn xlsx_title_is_first_sheet() {
        let parser = XlsxParser;
        let path = fixtures_dir().join("simple.xlsx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        assert_eq!(
            result.title.as_deref(),
            Some("Data"),
            "title should be first sheet name"
        );
    }

    /// T-P4-E01-19: row_count metadata is total rows across all sheets.
    #[test]
    fn xlsx_row_count_metadata() {
        let parser = XlsxParser;
        let path = fixtures_dir().join("simple.xlsx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        let row_count: usize = result
            .metadata
            .get("row_count")
            .expect("row_count metadata")
            .parse()
            .expect("row_count should be numeric");

        // Data sheet: 3 rows (header + 2 data); Summary sheet: 1 row → total 4
        assert_eq!(row_count, 4, "row_count should be 4 across both sheets");
    }

    /// T-P4-E01-19: Rows are tab-separated.
    #[test]
    fn xlsx_rows_are_tab_separated() {
        let parser = XlsxParser;
        let path = fixtures_dir().join("simple.xlsx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        // The header row "Name\tValue\tNotes" should appear.
        assert!(
            result.text.contains('\t'),
            "output should contain tab separators"
        );
    }

    /// T-P4-E01-19: Numeric-only sheet parses correctly.
    #[test]
    fn xlsx_numbers_fixture() {
        let parser = XlsxParser;
        let path = fixtures_dir().join("numbers.xlsx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        assert!(result.text.contains("## Sheet: Numbers"));
        // First row: 1 2 3
        assert!(result.text.contains('1'));
        assert!(result.text.contains('2'));
        assert!(result.text.contains('3'));
    }

    /// T-P4-E01-19: Corrupt/invalid file returns Err, not panic.
    #[test]
    fn xlsx_malformed_returns_err() {
        let parser = XlsxParser;
        let tmp = tempfile::NamedTempFile::with_suffix(".xlsx").unwrap();
        std::fs::write(tmp.path(), b"not a valid xlsx file \x00\xFF garbage").unwrap();

        let result = parser.parse(tmp.path());
        assert!(result.is_err(), "malformed XLSX should return Err");
    }

    /// T-P4-E01-19: can_parse accepts .xlsx and .xls, rejects others.
    #[test]
    fn xlsx_can_parse_extension_check() {
        let parser = XlsxParser;
        assert!(parser.can_parse(Path::new("file.xlsx")));
        assert!(parser.can_parse(Path::new("file.xls")));
        assert!(!parser.can_parse(Path::new("file.csv")));
        assert!(!parser.can_parse(Path::new("file.pdf")));
        assert!(!parser.can_parse(Path::new("file.docx")));
    }
}

//! Stamp comment helpers — read/write `<!-- cascade:applied … -->` entries.

pub(super) const STAMP_PREFIX: &str = "<!-- cascade:applied";

/// Build the stamp comment line for one applied template.
pub(crate) fn make_stamp(id: &str, version: &str, applied_at: &str) -> String {
    format!(
        r#"<!-- cascade:applied {{ id="{}", version="{}", applied_at="{}" }} -->"#,
        id, version, applied_at
    )
}

/// Parse all stamp entries from `content`, returning `(id, version)` pairs.
pub(super) fn extract_stamps(content: &str) -> Vec<(String, String)> {
    let mut stamps = Vec::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with(STAMP_PREFIX) {
            continue;
        }
        if let (Some(id), Some(ver)) = (
            extract_attr(trimmed, "id"),
            extract_attr(trimmed, "version"),
        ) {
            stamps.push((id, ver));
        }
    }
    stamps
}

/// Extract the value of `key="..."` from a stamp comment line.
pub(super) fn extract_attr(line: &str, key: &str) -> Option<String> {
    let needle = format!("{}=\"", key);
    let start = line.find(&needle)? + needle.len();
    let rest = &line[start..];
    let end = rest.find('"')?;
    Some(rest[..end].to_string())
}

/// Return true if `content` already has a stamp for `(id, version)`.
pub(super) fn is_stamped(content: &str, id: &str, version: &str) -> bool {
    extract_stamps(content)
        .iter()
        .any(|(sid, sver)| sid == id && sver == version)
}

//! Upgrade helpers — deprecation comments and stamp replacement.

use super::stamp::make_stamp;

/// Insert a deprecation notice comment immediately before a section heading.
///
/// If the heading is not found in the content, returns the content unchanged.
pub(super) fn add_deprecation_comment(content: &str, heading: &str) -> String {
    let lines: Vec<&str> = content.lines().collect();
    let mut result = String::new();
    let mut found = false;

    for line in &lines {
        if !found && *line == heading {
            result.push_str("<!-- cascade:deprecated — this section was removed in the new template version -->\n");
            found = true;
        }
        result.push_str(line);
        result.push('\n');
    }

    result
}

/// Replace the old stamp entry (for `old_id`/`old_version`) with a new stamp
/// for `new_id`/`new_version`.  If no matching old stamp is found, appends
/// the new stamp.
pub(super) fn replace_or_append_stamp(
    content: &str,
    old_id: &str,
    old_version: &str,
    new_id: &str,
    new_version: &str,
) -> String {
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();
    let new_stamp = make_stamp(new_id, new_version, &now);

    let old_stamp_prefix = format!(
        r#"<!-- cascade:applied {{ id="{}", version="{}"#,
        old_id, old_version
    );

    // Try to replace the old stamp line.
    let mut replaced = false;
    let mut result = String::new();
    for line in content.lines() {
        if !replaced && line.trim().starts_with(&old_stamp_prefix) {
            result.push_str(&new_stamp);
            result.push('\n');
            replaced = true;
        } else {
            result.push_str(line);
            result.push('\n');
        }
    }

    if !replaced {
        // No old stamp found — append the new one.
        if !result.ends_with('\n') {
            result.push('\n');
        }
        result.push_str(&format!("\n{}\n", new_stamp));
    }

    result
}

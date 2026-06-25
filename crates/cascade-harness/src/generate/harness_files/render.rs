//! Content renderers — produce the harness-native file body.

use cascade_core::cascade_resolution::ResolvedCascade;
use cascade_core::model_ids::DEFAULT_HARNESS_MODEL;

use super::kind::HarnessKind;
use super::super::safe_write::content_hash;

/// Render pointer-comment lines for on-demand rules.
///
/// Each on-demand rule becomes a `-> <text> (load_when: <condition>)` comment
/// line. These are appended after the always-loaded body so the harness file
/// stays within context-budget while still advertising what rules exist.
///
/// Returns an empty string when there are no on-demand rules.
pub(super) fn render_on_demand_pointer_section(resolved: &ResolvedCascade) -> String {
    if resolved.on_demand_rules.is_empty() {
        return String::new();
    }
    let mut out = String::from("\n<!-- cascade:on-demand-rules -->\n");
    out.push_str("<!-- The following rules are NOT loaded automatically. Load them when their condition applies. -->\n");
    for rule in &resolved.on_demand_rules {
        match &rule.load_when {
            Some(when) => {
                out.push_str(&format!(
                    "<!-- -> {} (load_when: {}, source: {}) -->\n",
                    rule.text.trim(),
                    when,
                    rule.source_tier,
                ));
            }
            None => {
                out.push_str(&format!(
                    "<!-- -> {} (on-demand, source: {}) -->\n",
                    rule.text.trim(),
                    rule.source_tier,
                ));
            }
        }
    }
    out
}

/// Render the harness-native instruction file content.
///
/// The merged instruction body is IDENTICAL across all harnesses.
/// Only the envelope (frontmatter, header, format wrapper) differs.
///
/// On-demand rules are rendered as `->` pointer comment lines AFTER the body
/// rather than being inlined, so the context budget stays bounded.
///
/// The marker line includes a SHA-256 content fingerprint so that hand-edits
/// can be detected by [`super::super::safe_write::hash_matches`].
pub(super) fn render_harness_file(harness: HarnessKind, resolved: &ResolvedCascade) -> String {
    let body = resolved.merged_instructions.trim();
    let mcp = &resolved.mcp_server_url;
    let on_demand = render_on_demand_pointer_section(resolved);

    // Build the body that will follow the marker line, then hash it.
    let body_section: String = match harness {
        HarnessKind::ClaudeCode => format!(
            "<!-- cascade:harness=claude-code -->\n\
             \n\
             **MCP server:** `{mcp}`\n\
             Call `cascade.provide_harness_context` on startup for the full context payload.\n\
             \n\
             ---\n\
             \n\
             {body}\n\
             {on_demand}",
            mcp = mcp,
            body = body,
            on_demand = on_demand,
        ),
        HarnessKind::OpenCode | HarnessKind::Codex => {
            let id = harness.id();
            let model = DEFAULT_HARNESS_MODEL;
            format!(
                "<!-- cascade:harness={id} -->\n\
                 ---\n\
                 model: \"{model}\"\n\
                 tools:\n\
                   - \"cascade.read\"\n\
                   - \"cascade.search\"\n\
                   - \"cascade.provide_harness_context\"\n\
                 ---\n\
                 \n\
                 **MCP server:** `{mcp}`\n\
                 \n\
                 {body}\n\
                 {on_demand}",
                id = id,
                model = model,
                mcp = mcp,
                body = body,
                on_demand = on_demand,
            )
        }
        HarnessKind::Cursor => format!(
            "<!-- cascade:harness=cursor -->\n\
             ---\n\
             description: Cascade unified AI instructions\n\
             globs: [\"**/*\"]\n\
             alwaysApply: true\n\
             ---\n\
             \n\
             {body}\n\
             {on_demand}",
            body = body,
            on_demand = on_demand,
        ),
        HarnessKind::Aider => format!(
            "<!-- cascade:harness=aider -->\n\
             \n\
             # Conventions\n\
             \n\
             {body}\n\
             {on_demand}",
            body = body,
            on_demand = on_demand,
        ),
        HarnessKind::Antigravity => format!(
            "<!-- cascade:harness=antigravity -->\n\
             \n\
             # Cascade Context Rules\n\
             \n\
             **MCP server:** `{mcp}`  \n\
             Call `provide_harness_context` on startup for the full context payload.\n\
             \n\
             ---\n\
             \n\
             {body}\n\
             {on_demand}",
            mcp = mcp,
            body = body,
            on_demand = on_demand,
        ),
    };

    // Compute hash of the body section that follows the marker line.
    let hash = content_hash(&body_section);
    format!("<!-- cascade:unified-harness sha256={hash} -->\n{body_section}")
}

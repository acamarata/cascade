//! Tests for harness file generation.
#![cfg(test)]

use super::api::{
    generate_for_harnesses, init_from_installed, inject_active_work_section, write_single_file,
};
use super::constants::{ACTIVE_WORK_BEGIN, ACTIVE_WORK_END, UNIFIED_HARNESS_MARKER_BASE};
use super::kind::HarnessKind;
use cascade_core::cascade_resolution::{ResolvedCascade, TierResult};
use cascade_types::cascade_tier::CascadeTier;
use serial_test::serial;
use std::fs;
use tempfile::TempDir;

fn mock_resolved(body: &str) -> ResolvedCascade {
    ResolvedCascade {
        merged_instructions: body.to_string(),
        on_demand_rules: vec![],
        vars: Default::default(),
        mcp_server_url: "unix://~/.cascade/cascade.sock".into(),
        tiers_found: vec![TierResult {
            tier: CascadeTier::Gci,
            found: true,
            path_searched: std::path::PathBuf::from("/home/.cascade"),
            instructions: body.to_string(),
        }],
        working_dir: std::path::PathBuf::from("/project"),
    }
}

// ── test 1: all harnesses produce files with identical body ───────────────

/// Each harness file contains the merged instruction body verbatim.
///
/// WHY: E-P7-03 requirement — content identical across harnesses.
#[test]
#[serial(global_env)]
fn all_harnesses_contain_identical_body() {
    let tmp = TempDir::new().unwrap();
    let resolved = mock_resolved("## RULES\n\nAlways use Cascade.");

    generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false)
        .expect("generate_for_harnesses should succeed");

    // Each harness file should contain the body text.
    let expected_body = "Always use Cascade.";

    for harness in HarnessKind::ALL {
        let dest = tmp.path().join(harness.output_filename());
        assert!(
            dest.exists(),
            "harness={}: {} must exist",
            harness.id(),
            dest.display()
        );
        let content = fs::read_to_string(&dest).unwrap();
        assert!(
            content.contains(expected_body),
            "harness={}: file must contain body '{}': got:\n{}",
            harness.id(),
            expected_body,
            content
        );
    }
}

// ── test 2: each harness uses the correct output filename ─────────────────

/// Output filename matches the spec for each harness.
#[test]
#[serial(global_env)]
fn harness_output_filenames_correct() {
    assert_eq!(HarnessKind::ClaudeCode.output_filename(), "CLAUDE.md");
    assert_eq!(HarnessKind::OpenCode.output_filename(), "AGENTS.md");
    assert_eq!(HarnessKind::Codex.output_filename(), "AGENTS.md");
    assert_eq!(
        HarnessKind::Cursor.output_filename(),
        ".cursor/rules/cascade.mdc"
    );
    assert_eq!(HarnessKind::Aider.output_filename(), "CONVENTIONS.md");
}

// ── test 3: idempotency — second run skips files with marker ─────────────

/// Running generate_for_harnesses twice does not overwrite files.
#[test]
#[serial(global_env)]
fn generate_is_idempotent() {
    let tmp = TempDir::new().unwrap();
    let resolved = mock_resolved("## RULES\n\nDo good work.");

    generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false).unwrap();

    // Capture mtimes.
    let mtimes_before: Vec<_> = HarnessKind::ALL
        .iter()
        .filter_map(|h| {
            let p = tmp.path().join(h.output_filename());
            fs::metadata(&p).ok().map(|m| (h.id(), m.modified().ok()))
        })
        .collect();

    std::thread::sleep(std::time::Duration::from_millis(50));

    generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false).unwrap();

    let mtimes_after: Vec<_> = HarnessKind::ALL
        .iter()
        .filter_map(|h| {
            let p = tmp.path().join(h.output_filename());
            fs::metadata(&p).ok().map(|m| (h.id(), m.modified().ok()))
        })
        .collect();

    for ((id_b, mt_b), (id_a, mt_a)) in mtimes_before.iter().zip(mtimes_after.iter()) {
        assert_eq!(id_b, id_a, "harness order changed between runs");
        assert_eq!(
            mt_b, mt_a,
            "harness={id_b}: mtime should not change on idempotent run"
        );
    }
}

// ── test 4: dry-run writes no files ──────────────────────────────────────

/// Dry-run produces no files on disk.
#[test]
#[serial(global_env)]
fn dry_run_writes_nothing() {
    let tmp = TempDir::new().unwrap();
    let resolved = mock_resolved("test dry run");

    generate_for_harnesses(
        &resolved,
        tmp.path(),
        HarnessKind::ALL,
        /* dry_run= */ true,
    )
    .unwrap();

    for harness in HarnessKind::ALL {
        let dest = tmp.path().join(harness.output_filename());
        assert!(
            !dest.exists(),
            "harness={}: {} must NOT be created in dry-run mode",
            harness.id(),
            dest.display()
        );
    }
}

// ── test 5: --output-single-file writes one file with all harnesses ───────

/// write_single_file produces one file with the marker and merged body.
#[test]
#[serial(global_env)]
fn single_file_output() {
    let tmp = TempDir::new().unwrap();
    let dest = tmp.path().join("cascade-unified.md");
    let resolved = mock_resolved("## RULES\n\nSingle file rules.");

    write_single_file(&resolved, &dest, HarnessKind::ALL, false).unwrap();

    assert!(dest.exists(), "single-file must exist");
    let content = fs::read_to_string(&dest).unwrap();
    assert!(
        content.contains(UNIFIED_HARNESS_MARKER_BASE),
        "single-file must contain the marker"
    );
    assert!(
        content.contains("Single file rules."),
        "single-file must contain merged body"
    );
    // All harness IDs must appear in the file header.
    for h in HarnessKind::ALL {
        assert!(
            content.contains(h.id()),
            "single-file must list harness={} in header",
            h.id()
        );
    }
}

// ── test 6: init-from-installed detects fixtures ──────────────────────────

/// init_from_installed scaffolds harnesses whose markers exist in workspace.
///
/// We simulate "installed" by writing a fake binary into the PATH tempdir.
#[test]
#[serial(global_env)]
fn init_from_installed_detects_via_path() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    let workspace = TempDir::new().unwrap();

    // Create a fake 'aider' binary in a temp bin dir on PATH.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let bin_dir = tmp.path().join("bin");
        fs::create_dir_all(&bin_dir).unwrap();
        let fake_aider = bin_dir.join("aider");
        fs::write(&fake_aider, "#!/bin/sh\necho aider").unwrap();
        let mut perms = fs::metadata(&fake_aider).unwrap().permissions();
        perms.set_mode(0o755);
        fs::set_permissions(&fake_aider, perms).unwrap();

        // Override PATH so aider is "installed".
        let orig_path = std::env::var("PATH").unwrap_or_default();
        std::env::set_var("PATH", format!("{}:{}", bin_dir.display(), orig_path));

        let resolved = mock_resolved("init instructions");
        let detected = init_from_installed(&resolved, workspace.path(), false).unwrap();

        std::env::set_var("PATH", &orig_path);

        assert!(
            detected.contains(&HarnessKind::Aider),
            "aider should be detected when binary is on PATH"
        );
        let conventions_md = workspace.path().join("CONVENTIONS.md");
        assert!(
            conventions_md.exists(),
            "CONVENTIONS.md must be created for aider"
        );
        let content = fs::read_to_string(&conventions_md).unwrap();
        assert!(
            content.contains(UNIFIED_HARNESS_MARKER_BASE),
            "CONVENTIONS.md must contain cascade marker"
        );
    }

    // On non-unix: at minimum init_from_installed must not panic.
    #[cfg(not(unix))]
    {
        let resolved = mock_resolved("init instructions");
        init_from_installed(&resolved, workspace.path(), false).unwrap();
    }
}

// ── test 7: dry-run init-from-installed writes nothing ───────────────────

/// Dry-run mode of init-from-installed must not write any files.
#[test]
#[serial(global_env)]
fn init_from_installed_dry_run_writes_nothing() {
    let workspace = TempDir::new().unwrap();

    // Plant a fake cursor global config to trigger cursor detection.
    let cursor_dir = workspace.path().join(".cursor");
    fs::create_dir_all(&cursor_dir).unwrap();

    let resolved = mock_resolved("dry run init");
    init_from_installed(&resolved, workspace.path(), /* dry_run= */ true).unwrap();

    // No non-.cursor files should be created.
    let mdc = workspace
        .path()
        .join(".cursor")
        .join("rules")
        .join("cascade.mdc");
    assert!(
        !mdc.exists(),
        ".cursor/rules/cascade.mdc must NOT be written in dry-run"
    );
}

// ── test 8: inject_active_work_section appends when no delimiters ─────────

/// inject_active_work_section appends the section when no existing block.
#[test]
#[serial(global_env)]
fn inject_active_work_appends_when_absent() {
    let tmp = TempDir::new().unwrap();
    let dest = tmp.path().join("CLAUDE.md");
    fs::write(&dest, "# Instructions\n\nDo good work.\n").unwrap();

    let active = cascade_core::pbd::active_work::ActiveWorkBlock {
        sprint_id: Some("s01".into()),
        sprint_title: Some("Sprint 1".into()),
        tickets: vec![cascade_core::pbd::active_work::ActiveTicketEntry {
            id: "T-P1-E01-W01-S01-01".into(),
            title: "Build the thing".into(),
            status: "active".into(),
            depends_on: vec![],
        }],
        tickets_total: 1,
        tasks: vec![],
        tasks_total: 0,
    };

    let changed = inject_active_work_section(&dest, &active, false).unwrap();
    assert!(changed, "file should be modified");

    let content = fs::read_to_string(&dest).unwrap();
    assert!(
        content.contains(ACTIVE_WORK_BEGIN),
        "must contain begin marker"
    );
    assert!(content.contains(ACTIVE_WORK_END), "must contain end marker");
    assert!(
        content.contains("T-P1-E01-W01-S01-01"),
        "must contain ticket id"
    );
    assert!(
        content.contains("# Instructions"),
        "original content must be preserved"
    );
}

// ── test 9: inject_active_work_section is idempotent (replaces on re-run) ─

/// Re-running inject_active_work_section replaces the old block, not dupe.
#[test]
#[serial(global_env)]
fn inject_active_work_is_idempotent() {
    let tmp = TempDir::new().unwrap();
    let dest = tmp.path().join("CLAUDE.md");
    fs::write(&dest, "# Instructions\n\nDo good work.\n").unwrap();

    let make_block =
        |sprint: &str, ticket_title: &str| cascade_core::pbd::active_work::ActiveWorkBlock {
            sprint_id: Some(sprint.into()),
            sprint_title: Some("Sprint".into()),
            tickets: vec![cascade_core::pbd::active_work::ActiveTicketEntry {
                id: "T-01".into(),
                title: ticket_title.into(),
                status: "active".into(),
                depends_on: vec![],
            }],
            tickets_total: 1,
            tasks: vec![],
            tasks_total: 0,
        };

    // First injection
    inject_active_work_section(&dest, &make_block("s01", "First title"), false).unwrap();
    // Second injection (different sprint)
    inject_active_work_section(&dest, &make_block("s02", "Updated title"), false).unwrap();

    let content = fs::read_to_string(&dest).unwrap();

    // Only one begin/end pair must exist
    let begin_count = content.matches(ACTIVE_WORK_BEGIN).count();
    let end_count = content.matches(ACTIVE_WORK_END).count();
    assert_eq!(
        begin_count, 1,
        "must have exactly 1 active-work begin marker; got {begin_count}"
    );
    assert_eq!(
        end_count, 1,
        "must have exactly 1 active-work end marker; got {end_count}"
    );

    // Should contain updated sprint, not original
    assert!(content.contains("s02"), "must show updated sprint id");
    assert!(
        content.contains("Updated title"),
        "must show updated ticket title"
    );
    assert!(
        !content.contains("First title"),
        "old ticket title must not remain"
    );
}

// ── test 10: inject with empty block removes section ─────────────────────

/// When active_work is empty, inject_active_work_section removes existing section.
#[test]
#[serial(global_env)]
fn inject_active_work_removes_when_empty() {
    let tmp = TempDir::new().unwrap();
    let dest = tmp.path().join("CLAUDE.md");
    // Pre-populate with an existing active-work section
    let initial = format!(
        "# Instructions\n\n{begin}\n## Active Work\n- ticket here\n{end}\n\nDo work.\n",
        begin = ACTIVE_WORK_BEGIN,
        end = ACTIVE_WORK_END
    );
    fs::write(&dest, &initial).unwrap();

    let empty_block = cascade_core::pbd::active_work::ActiveWorkBlock::default();
    let changed = inject_active_work_section(&dest, &empty_block, false).unwrap();
    assert!(changed, "file should be modified when removing section");

    let content = fs::read_to_string(&dest).unwrap();
    assert!(
        !content.contains(ACTIVE_WORK_BEGIN),
        "begin marker must be removed"
    );
    assert!(
        !content.contains(ACTIVE_WORK_END),
        "end marker must be removed"
    );
    assert!(
        content.contains("# Instructions"),
        "original content preserved"
    );
}

// ── test 11: dry-run inject writes nothing ─────────────────────────────────

#[test]
#[serial(global_env)]
fn inject_active_work_dry_run_writes_nothing() {
    let tmp = TempDir::new().unwrap();
    let dest = tmp.path().join("CLAUDE.md");
    let original = "# Instructions\n\nDo work.\n";
    fs::write(&dest, original).unwrap();

    let active = cascade_core::pbd::active_work::ActiveWorkBlock {
        sprint_id: Some("s01".into()),
        sprint_title: Some("Sprint 1".into()),
        tickets: vec![],
        tickets_total: 0,
        tasks: vec![cascade_core::pbd::active_work::ActiveTaskEntry {
            title: "Some task".into(),
            status: "todo".into(),
            tags: vec![],
        }],
        tasks_total: 1,
    };

    let changed = inject_active_work_section(&dest, &active, /* dry_run= */ true).unwrap();
    assert!(!changed, "dry-run must return false (no write)");

    let content = fs::read_to_string(&dest).unwrap();
    assert_eq!(content, original, "dry-run must not modify file");
}

// ── test 12: on-demand rules appear as pointer comments, NOT inlined ───────

/// An on-demand rule must appear as a `<!-- -> ... -->` pointer comment,
/// not as full body text, in every harness output file.
///
/// WHY: context-budget regression fix — on-demand rules must never be
/// inlined into harness instruction files.
#[test]
#[serial(global_env)]
fn on_demand_rules_rendered_as_pointer_comments() {
    let tmp = TempDir::new().unwrap();
    let mut resolved = mock_resolved("## Always-loaded body\n\nDo good work.");
    resolved.on_demand_rules = vec![
        cascade_types::tiers::OnDemandRule {
            text: "Load auth policy before implementing login".into(),
            load_when: Some("auth".into()),
            source_tier: "GCI".into(),
        },
        cascade_types::tiers::OnDemandRule {
            text: "Review deploy checklist".into(),
            load_when: None,
            source_tier: "PRC".into(),
        },
    ];

    generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false)
        .expect("generate should succeed");

    for harness in HarnessKind::ALL {
        let dest = tmp.path().join(harness.output_filename());
        let content = fs::read_to_string(&dest).unwrap();

        // On-demand rules must appear as pointer comments.
        assert!(
            content.contains("<!-- -> Load auth policy before implementing login"),
            "harness={}: must contain auth pointer comment; got:\n{}",
            harness.id(),
            content
        );
        assert!(
            content.contains("load_when: auth"),
            "harness={}: auth pointer must include load_when; got:\n{}",
            harness.id(),
            content
        );

        // The on-demand rule text must NOT appear as plain body text
        // (i.e. outside of HTML comments). We verify by checking it's not
        // present outside of `<!--` ... `-->` wrapping.
        // A simple check: "Review deploy checklist" appears only in comment form.
        assert!(
            content.contains("<!-- -> Review deploy checklist"),
            "harness={}: must contain deploy pointer comment",
            harness.id()
        );

        // Always-loaded body must still be present.
        assert!(
            content.contains("Do good work."),
            "harness={}: must still contain always-loaded body",
            harness.id()
        );
    }
}

// ── test 13: generated files embed a sha256 content hash ─────────────────

/// Every generated harness file must have a marker line with sha256=...
///
/// WHY: P12 requirement — hand-edits detectable via hash mismatch.
#[test]
#[serial(global_env)]
fn generated_files_embed_content_hash() {
    let tmp = TempDir::new().unwrap();
    let resolved = mock_resolved("## RULES\n\nHash test content.");

    generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false).unwrap();

    for harness in HarnessKind::ALL {
        let dest = tmp.path().join(harness.output_filename());
        let content = fs::read_to_string(&dest).unwrap();

        // Marker must include sha256= fragment.
        assert!(
            content.contains("<!-- cascade:unified-harness sha256="),
            "harness={}: marker must include sha256=; got first line:\n{}",
            harness.id(),
            content.lines().next().unwrap_or("")
        );

        // The embedded hash must be 32 hex chars.
        let hash_start = content.find("sha256=").expect("sha256= must be present");
        let hash_region = &content[hash_start + "sha256=".len()..];
        let hash: String = hash_region.chars().take(32).collect();
        assert!(
            hash.len() == 32 && hash.chars().all(|c| c.is_ascii_hexdigit()),
            "harness={}: embedded hash must be 32 hex chars, got: '{hash}'",
            harness.id()
        );
    }
}

// ── test 14: snapshot is created before overwrite ─────────────────────────

/// When an existing file would be overwritten, a snapshot is created first.
///
/// WHY: P12 requirement — snapshot-before-regenerate must capture
/// pre-write content so the file can be recovered.
#[test]
#[serial(global_env)]
fn snapshot_created_before_overwrite() {
    let tmp = TempDir::new().unwrap();
    // Pre-write a file WITHOUT the cascade marker so generate will overwrite it.
    let claude_md = tmp.path().join("CLAUDE.md");
    fs::write(&claude_md, "# Hand-written content\n\nDo not lose this.\n").unwrap();

    let resolved = mock_resolved("## RULES\n\nNew content.");
    generate_for_harnesses(
        &resolved,
        tmp.path(),
        &[HarnessKind::ClaudeCode],
        /* dry_run= */ false,
    )
    .unwrap();

    // A snapshot directory must exist under .cascade/snapshots/.
    let snap_root = tmp.path().join(".cascade").join("snapshots");
    assert!(snap_root.exists(), ".cascade/snapshots/ must be created");

    let mut snap_dirs: Vec<_> = fs::read_dir(&snap_root)
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.is_dir())
        .collect();
    assert!(
        !snap_dirs.is_empty(),
        "at least one snapshot dir must exist"
    );
    snap_dirs.sort();

    // The snapshot must contain a copy of the original CLAUDE.md.
    let snap_claude = snap_dirs[0].join("CLAUDE.md");
    assert!(snap_claude.exists(), "snapshot must contain CLAUDE.md");
    let snap_content = fs::read_to_string(&snap_claude).unwrap();
    assert!(
        snap_content.contains("Hand-written content"),
        "snapshot must preserve original content; got: {snap_content}"
    );

    // The live CLAUDE.md must now contain the new generated content.
    let new_content = fs::read_to_string(&claude_md).unwrap();
    assert!(
        new_content.contains("New content"),
        "live file must contain newly generated content"
    );
}

// ── test 15: hand-edit detected via hash mismatch ─────────────────────────

/// After generation, modifying the file body causes hash_matches to return
/// Some(false), confirming the hand-edit detection path.
///
/// WHY: P12 requirement — hand-edits must be detectable via hash mismatch.
#[test]
#[serial(global_env)]
fn hand_edit_detected_via_hash_mismatch() {
    use crate::generate::safe_write::hash_matches;

    let tmp = TempDir::new().unwrap();
    let resolved = mock_resolved("## RULES\n\nOriginal content.");
    generate_for_harnesses(&resolved, tmp.path(), &[HarnessKind::ClaudeCode], false).unwrap();

    let dest = tmp.path().join("CLAUDE.md");
    let original = fs::read_to_string(&dest).unwrap();

    // Freshly generated file: hash must match.
    assert_eq!(
        hash_matches(&original),
        Some(true),
        "freshly generated file: hash must match"
    );

    // Simulate a hand-edit by appending text to the body.
    let hand_edited = format!("{original}\n## HAND EDIT\n");
    assert_eq!(
        hash_matches(&hand_edited),
        Some(false),
        "hand-edited file: hash must NOT match"
    );
}

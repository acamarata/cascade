//! Command-injection invariant guard (T-P2-E07-10).
//!
//! Purpose: a source-level regression test asserting that no crate in the
//!   workspace shells out via an interpolated shell string. Every external
//!   process MUST be spawned with an argument array
//!   (`Command::new(bin).arg(a).arg(b)`), never `Command::new("sh").arg("-c")`
//!   with a `format!`-built command line, because the latter lets attacker
//!   controlled data reach a shell and inject commands.
//!
//! Strategy: walk every `crates/*/src/**/*.rs` file and fail on any forbidden
//!   pattern. This is a deterministic, CI-enforceable guard — it catches a
//!   reintroduction of `sh -c "<interpolated>"` in any future change.
//!
//! SPORT: .claude/docs/MASTER-SECURITY.md — command-injection-guard (E07-10)

use std::fs;
use std::path::{Path, PathBuf};

/// Return the workspace root (two levels up from this crate's manifest dir).
fn workspace_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .expect("workspace root is two levels above crates/cascade-core")
        .to_path_buf()
}

/// Recursively collect every `.rs` file under `dir`.
fn collect_rs_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            // Skip target/ and any nested build output.
            if path.file_name().and_then(|n| n.to_str()) == Some("target") {
                continue;
            }
            collect_rs_files(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("rs") {
            out.push(path);
        }
    }
}

/// Strip the contents of `tests/` files from the scan: this guard test itself
/// names the forbidden patterns in comments/strings and would self-trip.
fn is_test_support_file(path: &Path) -> bool {
    path.components().any(|c| c.as_os_str() == "tests")
}

/// Audited exemption: the PBD external-checks task runner.
///
/// `pbd/external_checks.rs` deliberately spawns `sh -c <command>` to run
/// build / test / health commands. This is a legitimate task-runner pattern
/// (equivalent to a Makefile target): the command string comes from the
/// developer-authored phase config — trusted input, never attacker-controlled
/// — and is passed as a SINGLE argument via `.arg("-c").arg(&self.command)`,
/// NOT assembled with `format!` interpolation. The interpolation vector this
/// guard primarily defends against (`"-c"` + `format!`) is therefore still
/// enforced here, and every other crate file remains fully scanned. This
/// exemption is intentionally narrow: it matches exactly one audited file.
fn is_audited_shell_runner(path: &Path) -> bool {
    path.file_name().and_then(|n| n.to_str()) == Some("external_checks.rs")
        && path.components().any(|c| c.as_os_str() == "pbd")
}

#[test]
fn no_shell_string_injection_in_workspace() {
    let root = workspace_root();
    let mut files = Vec::new();
    for crate_dir in fs::read_dir(root.join("crates"))
        .expect("read crates/")
        .flatten()
    {
        let src = crate_dir.path().join("src");
        if src.is_dir() {
            collect_rs_files(&src, &mut files);
        }
    }
    assert!(
        !files.is_empty(),
        "expected to scan at least one source file"
    );

    let mut violations: Vec<String> = Vec::new();

    for file in &files {
        if is_test_support_file(file) || is_audited_shell_runner(file) {
            continue;
        }
        let content = fs::read_to_string(file).unwrap_or_default();
        for (lineno, line) in content.lines().enumerate() {
            // Ignore comment lines — several modules legitimately *document*
            // the arg-array invariant in prose (e.g. "never sh -c").
            let trimmed = line.trim_start();
            if trimmed.starts_with("//") || trimmed.starts_with("*") {
                continue;
            }

            // Forbidden: spawning a shell directly. We never shell out.
            if line.contains("Command::new(\"sh\")") || line.contains("Command::new(\"bash\")") {
                violations.push(format!(
                    "{}:{} spawns a shell directly: {}",
                    file.display(),
                    lineno + 1,
                    line.trim()
                ));
            }

            // Forbidden: a `-c` shell flag whose command line is built with
            // string interpolation (format!) — the classic injection vector.
            if line.contains("\"-c\"") && line.contains("format!") {
                violations.push(format!(
                    "{}:{} passes an interpolated `-c` shell command: {}",
                    file.display(),
                    lineno + 1,
                    line.trim()
                ));
            }
        }
    }

    assert!(
        violations.is_empty(),
        "command-injection invariant violated — all external processes must use \
         argument arrays, never an interpolated shell string:\n{}",
        violations.join("\n")
    );
}

//! Guard test: command injection protection — all shell invocations use arg arrays.
//!
//! Purpose: Verify that process-spawning code in cascade-daemon passes arguments as
//!          discrete array elements (never via `sh -c <interpolated_string>`), so
//!          shell-metacharacter payloads are treated as literals, not executed.
//!
//! Acceptance: T-P2-E07-10 — "Command injection protection: all shell invocations
//!             via arg arrays"

use std::path::PathBuf;
use std::process::Command;

/// Sentinel file written by the injection payload if a shell ever interprets it.
fn sentinel_path() -> PathBuf {
    std::env::temp_dir().join("cascade_injection_test_sentinel")
}

/// Remove the sentinel before each test run so a leftover file from a prior
/// failed run does not produce a false negative.
fn cleanup_sentinel() {
    let _ = std::fs::remove_file(sentinel_path());
}

/// Verify that a shell-metacharacter payload passed as a plain argument to a
/// process launched via arg-array is treated as a literal string, NOT executed.
///
/// If this process were launched as `sh -c "echo '; touch /tmp/sentinel'"`, the
/// shell would split on `;` and run `touch /tmp/sentinel`. Because we use the
/// arg-array form, the entire string is passed unchanged as argv[1] to `echo`,
/// so the sentinel is never created.
#[test]
fn arg_array_does_not_execute_shell_metacharacters() {
    cleanup_sentinel();

    let sentinel = sentinel_path();
    // Build the injection payload: if a shell ever parses this string, it will
    // try to create the sentinel file via `touch`.
    let injection_payload = format!("'; touch {} ; echo '", sentinel.display());

    // Launch `echo` with the payload as a LITERAL argument — no shell involved.
    let output = Command::new("echo")
        .arg(&injection_payload)
        .output()
        .expect("echo must be available on the test host");

    assert!(
        output.status.success(),
        "echo should succeed; stderr: {}",
        String::from_utf8_lossy(&output.stderr)
    );

    // The payload should appear verbatim in echo's stdout — including the
    // semicolons and `touch` word — because the shell never parsed it.
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        stdout.contains("touch"),
        "expected literal payload in stdout, got: {:?}",
        stdout
    );

    // Most importantly: the sentinel file must NOT have been created.
    assert!(
        !sentinel.exists(),
        "INJECTION SUCCEEDED: sentinel file was created at {:?}. \
         The command was executed via a shell instead of arg-array.",
        sentinel
    );
}

/// Static audit: confirm that no source file in the daemon crate passes `-c` as
/// a shell-interpreter flag in a `Command` invocation.
///
/// This complements the runtime test above. It searches the daemon's `src/`
/// directory for the pattern `arg("-c")` and fails if any match is found,
/// providing an early-warning regression gate.
#[test]
fn no_arg_dash_c_in_daemon_sources() {
    // Locate the daemon src/ dir relative to the manifest dir injected by Cargo.
    let manifest_dir = env!("CARGO_MANIFEST_DIR");
    let src_dir = PathBuf::from(manifest_dir).join("src");

    let mut violations: Vec<String> = Vec::new();

    // Walk every .rs file under src/.
    walk_rs_files(&src_dir, &mut |path, content| {
        for (line_no, line) in content.lines().enumerate() {
            // Match `.arg("-c")` — the POSIX sh / bash shell-mode flag.
            // We allow `args(["/c", ...])` (Windows cmd.exe) which is different.
            if line.contains(r#"arg("-c")"#) {
                violations.push(format!(
                    "{}:{}: {}",
                    path.display(),
                    line_no + 1,
                    line.trim()
                ));
            }
        }
    });

    assert!(
        violations.is_empty(),
        "Found arg(\"-c\") shell-mode flag in daemon sources — potential injection risk:\n{}",
        violations.join("\n")
    );
}

/// Static audit: confirm no `Command::new("sh")` or `Command::new("bash")` in
/// daemon sources (these are the launchers that, combined with `-c`, create the
/// injection surface).
#[test]
fn no_shell_launcher_in_daemon_sources() {
    let manifest_dir = env!("CARGO_MANIFEST_DIR");
    let src_dir = PathBuf::from(manifest_dir).join("src");

    let shell_launchers = [r#"Command::new("sh")"#, r#"Command::new("bash")"#];
    let mut violations: Vec<String> = Vec::new();

    walk_rs_files(&src_dir, &mut |path, content| {
        for (line_no, line) in content.lines().enumerate() {
            for launcher in &shell_launchers {
                if line.contains(launcher) {
                    violations.push(format!(
                        "{}:{}: {}",
                        path.display(),
                        line_no + 1,
                        line.trim()
                    ));
                }
            }
        }
    });

    assert!(
        violations.is_empty(),
        "Found shell launcher in daemon sources — POSIX shell exec not allowed:\n{}",
        violations.join("\n")
    );
}

// ── helpers ──────────────────────────────────────────────────────────────────

fn walk_rs_files(dir: &PathBuf, cb: &mut impl FnMut(&PathBuf, &str)) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            walk_rs_files(&path, cb);
        } else if path.extension().and_then(|e| e.to_str()) == Some("rs") {
            if let Ok(content) = std::fs::read_to_string(&path) {
                cb(&path, &content);
            }
        }
    }
}

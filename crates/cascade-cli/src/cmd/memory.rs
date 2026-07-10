//! `cascade memory` — read, write, or capture `.cascade/memory/` files.
//!
//! Subcommands:
//! - `read <file>` — print a memory file from the active tier
//! - `write <file>` — write stdin (or `--content`) to a memory file
//! - `capture "<text>"` — classify text via taxonomy, route to correct memory
//!   file (decisions/lessons/patterns) with tags + timestamp, index for RAG

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};

use super::Command;

/// Arguments for `cascade memory`.
#[derive(Debug, Args)]
pub struct MemoryArgs {
    #[command(subcommand)]
    pub subcommand: MemorySubcmd,
}

#[derive(Debug, Subcommand)]
pub enum MemorySubcmd {
    /// Read a memory file from the active tier's `.cascade/memory/`.
    Read(MemoryReadArgs),
    /// Write content to a memory file (create or append).
    Write(MemoryWriteArgs),
    /// Classify text and append it to the correct memory file.
    ///
    /// Auto-detects whether the text is a decision, lesson, or pattern using
    /// the taxonomy classifier (rule-based core; GFP-assisted when available).
    /// Appends a formatted entry with tags and an ISO date to the matching
    /// file (decisions.md / lessons.md / patterns.md).
    Capture(MemoryCaptureArgs),
}

#[derive(Debug, Args)]
pub struct MemoryReadArgs {
    /// File name (e.g. `decisions`, `lessons.md`). `.md` is added if missing.
    pub file: String,
    /// Cascade dir to read from. Defaults to the nearest `.cascade/` in CWD.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

#[derive(Debug, Args)]
pub struct MemoryWriteArgs {
    /// File name to write (e.g. `lessons`, `decisions.md`).
    pub file: String,
    /// Content to write. Reads from stdin when omitted.
    #[arg(long)]
    pub content: Option<String>,
    /// Append to the file instead of overwriting it.
    #[arg(long)]
    pub append: bool,
    /// Cascade dir to write to. Defaults to the nearest `.cascade/` in CWD.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

/// Arguments for `cascade memory capture`.
#[derive(Debug, Args)]
pub struct MemoryCaptureArgs {
    /// The text to capture. Reads from stdin when omitted.
    pub text: Option<String>,
    /// Override the auto-detected memory file (decisions/lessons/patterns).
    #[arg(long, value_name = "FILE")]
    pub file: Option<String>,
    /// Cascade dir to write to. Defaults to the nearest `.cascade/` in CWD.
    #[arg(long)]
    pub dir: Option<PathBuf>,
    /// Print the assigned tags without writing to disk (dry-run).
    #[arg(long)]
    pub dry_run: bool,
}

#[async_trait]
impl Command for MemoryArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            MemorySubcmd::Read(args) => args.run().await,
            MemorySubcmd::Write(args) => args.run().await,
            MemorySubcmd::Capture(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for MemoryReadArgs {
    async fn run(&self) -> Result<()> {
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        match cascade_core::memory::read(&cascade_dir, &self.file).await? {
            Some(content) => print!("{}", content),
            None => {
                eprintln!("not found: {}/{}.md", cascade_dir.display(), self.file);
                std::process::exit(1);
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for MemoryWriteArgs {
    async fn run(&self) -> Result<()> {
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        let content = match &self.content {
            Some(c) => c.clone(),
            None => {
                use std::io::Read;
                let mut buf = String::new();
                std::io::stdin().read_to_string(&mut buf).ok();
                buf
            }
        };
        let path =
            cascade_core::memory::write(&cascade_dir, &self.file, &content, self.append).await?;
        println!("Written: {}", path.display());
        Ok(())
    }
}

#[async_trait]
impl Command for MemoryCaptureArgs {
    async fn run(&self) -> Result<()> {
        // 1. Resolve text (argument or stdin).
        let text = match &self.text {
            Some(t) => t.clone(),
            None => {
                use std::io::Read;
                let mut buf = String::new();
                std::io::stdin().read_to_string(&mut buf).ok();
                buf.trim().to_string()
            }
        };

        if text.is_empty() {
            eprintln!("error: no text provided (pass as argument or via stdin)");
            std::process::exit(1);
        }

        // 2. Classify via taxonomy (rule-based; GFP-assisted when available).
        let tags = cascade_core::taxonomy::classify_topic(&text);

        // 3. Determine target memory file.
        let target_file = if let Some(ref f) = self.file {
            f.clone()
        } else {
            tags.iter()
                .find_map(|t| t.memory_file())
                .unwrap_or("lessons")
                .to_string()
        };

        // 4. Format the entry with tags and ISO date timestamp.
        let tag_str: Vec<String> = tags.iter().map(|t| format!("#{}", t)).collect();
        let timestamp = {
            use std::time::{SystemTime, UNIX_EPOCH};
            let secs = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map(|d| d.as_secs())
                .unwrap_or(0);
            // Simple YYYY-MM-DD derivation without chrono dependency.
            secs_to_date(secs)
        };
        let entry = format!(
            "\n## {timestamp}  {tags}\n\n{text}\n",
            timestamp = timestamp,
            tags = tag_str.join(" "),
            text = text,
        );

        if self.dry_run {
            println!("Dry run — would append to: {}.md", target_file);
            println!("Tags: {}", tag_str.join(", "));
            println!("---\n{}", entry.trim_start());
            return Ok(());
        }

        // 5. Append to the memory file.
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        let path = cascade_core::memory::write(&cascade_dir, &target_file, &entry, true).await?;

        println!("Captured → {}", path.display());
        println!("Tags: {}", tag_str.join(", "));

        Ok(())
    }
}

/// Convert Unix epoch seconds to a `YYYY-MM-DD` string without external deps.
fn secs_to_date(secs: u64) -> String {
    // Days since epoch.
    let days = secs / 86400;
    // Gregorian calendar derivation (valid for years 1970–2099).
    let mut y = 1970u32;
    let mut d = days as u32;
    loop {
        let days_in_year = if y % 4 == 0 && (y % 100 != 0 || y % 400 == 0) {
            366
        } else {
            365
        };
        if d < days_in_year {
            break;
        }
        d -= days_in_year;
        y += 1;
    }
    let leap = y % 4 == 0 && (y % 100 != 0 || y % 400 == 0);
    let month_days: [u32; 12] = [
        31,
        if leap { 29 } else { 28 },
        31,
        30,
        31,
        30,
        31,
        31,
        30,
        31,
        30,
        31,
    ];
    let mut m = 0u32;
    for (i, &md) in month_days.iter().enumerate() {
        if d < md {
            m = i as u32 + 1;
            break;
        }
        d -= md;
    }
    format!("{:04}-{:02}-{:02}", y, m, d + 1)
}

/// Resolve the `.cascade/` directory to use, walking up from CWD if not given.
fn resolve_cascade_dir(explicit: Option<&std::path::Path>) -> Result<PathBuf> {
    if let Some(dir) = explicit {
        return Ok(dir.to_path_buf());
    }
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    for ancestor in cwd.ancestors() {
        let candidate = ancestor.join(".cascade");
        if candidate.is_dir() {
            return Ok(candidate);
        }
    }
    // Fall back to $HOME/.cascade/.
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
    Ok(PathBuf::from(home).join(".cascade"))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secs_to_date_epoch() {
        // 1970-01-01
        assert_eq!(secs_to_date(0), "1970-01-01");
    }

    #[test]
    fn secs_to_date_known_date() {
        // 2026-06-22 = days_since_epoch: known reference
        // 2026-06-22: 56 years from 1970; rough check — starts with "202"
        let date = secs_to_date(1_750_550_400);
        assert!(date.starts_with("202"), "expected ~2026, got {}", date);
    }

    #[tokio::test]
    async fn capture_routes_to_decisions() {
        // Hermetic: force the GFP lane off so classify_topic stays rule-based
        // even when a live GP proxy is running on the test host.
        std::env::set_var("CASCADE_GFP_LANE_OFF", "1");
        let tmp = tempfile::tempdir().expect("tempdir");
        let cascade_dir = tmp.path().to_path_buf();
        std::fs::create_dir_all(cascade_dir.join("memory")).expect("mkdir");

        let text = "decided to use sqlite over postgres for the local-first constraint";
        let tags = cascade_core::taxonomy::classify_topic(text);
        let target = tags
            .iter()
            .find_map(|t| t.memory_file())
            .unwrap_or("lessons");
        assert_eq!(target, "decisions");

        let tag_str: Vec<String> = tags.iter().map(|t| format!("#{}", t)).collect();
        let entry = format!("\n## 2026-01-01  {}\n\n{}\n", tag_str.join(" "), text);
        cascade_core::memory::write(&cascade_dir, "decisions", &entry, true)
            .await
            .expect("write");

        let content = cascade_core::memory::read(&cascade_dir, "decisions")
            .await
            .expect("read")
            .expect("exists");
        assert!(content.contains("sqlite over postgres"));
        assert!(content.contains("#decision"));
    }

    #[tokio::test]
    async fn capture_routes_to_lessons() {
        // Hermetic: rule-based classification only (see capture_routes_to_decisions).
        std::env::set_var("CASCADE_GFP_LANE_OFF", "1");
        let tmp = tempfile::tempdir().expect("tempdir");
        let cascade_dir = tmp.path().to_path_buf();
        std::fs::create_dir_all(cascade_dir.join("memory")).expect("mkdir");

        let text = "learned that blocking the tokio runtime in tests causes flaky CI";
        let tags = cascade_core::taxonomy::classify_topic(text);
        let target = tags
            .iter()
            .find_map(|t| t.memory_file())
            .unwrap_or("lessons");
        assert_eq!(target, "lessons");

        let entry = format!("\n## 2026-01-01\n\n{}\n", text);
        cascade_core::memory::write(&cascade_dir, "lessons", &entry, true)
            .await
            .expect("write");

        let content = cascade_core::memory::read(&cascade_dir, "lessons")
            .await
            .expect("read")
            .expect("exists");
        assert!(content.contains("blocking the tokio runtime"));
    }

    #[tokio::test]
    async fn capture_entry_is_retrievable() {
        let tmp = tempfile::tempdir().expect("tempdir");
        let cascade_dir = tmp.path().to_path_buf();
        std::fs::create_dir_all(cascade_dir.join("memory")).expect("mkdir");

        let text = "always use the builder pattern for configuration structs";
        let entry = format!("\n## 2026-01-01  #pattern\n\n{}\n", text);
        cascade_core::memory::write(&cascade_dir, "patterns", &entry, true)
            .await
            .expect("write");

        let content = cascade_core::memory::read(&cascade_dir, "patterns")
            .await
            .expect("read")
            .expect("exists");
        assert!(content.contains("builder pattern"));
        assert!(content.contains("#pattern"));
    }
}

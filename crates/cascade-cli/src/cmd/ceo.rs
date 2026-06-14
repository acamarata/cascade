//! `cascade ceo` — T0 CEO/Founder Orchestrator CLI.
//!
//! Purpose: Submit directives to the CEO orchestrator, query session status,
//!   list pending approval requests, and approve/deny individual actions.
//!
//! Inputs:
//!   - `cascade ceo directive "<goal>"` — submit a Founder directive
//!   - `cascade ceo status`             — show the active session report
//!   - `cascade ceo approvals`          — list pending approval requests
//!   - `cascade ceo approve <id> [--note <text>]` — approve an action
//!   - `cascade ceo deny <id> [--note <text>]`    — deny an action
//!
//! Outputs:
//!   - Formatted tables / JSON to stdout.
//!   - Error message + exit 1 on daemon unavailability or logic errors.
//!
//! Constraints:
//!   - NEVER auto-approves Outbound actions — those are always parked.
//!   - All IPC calls use snake_case method names (`ceo_directive`, etc.).
//!   - Daemon must be running; graceful error on connection failure.
//!
//! SPORT: MASTER-CLI.md → cascade ceo * (E-P6-04)

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};
use serde::Deserialize;
use serde_json::json;

use super::Command;
use crate::ipc_client::IpcClient;

// ── Top-level args ────────────────────────────────────────────────────────────

/// `cascade ceo` — submit directives to the Founder AI orchestrator.
#[derive(Debug, Args)]
pub struct CeoArgs {
    #[command(subcommand)]
    pub subcommand: CeoSubcommand,
}

/// Subcommands under `cascade ceo`.
#[derive(Debug, Subcommand)]
pub enum CeoSubcommand {
    /// Submit a new directive to the CEO orchestrator.
    Directive(CeoDirectiveArgs),
    /// Show the active CEO session report.
    Status(CeoStatusArgs),
    /// List actions pending Founder approval.
    Approvals(CeoApprovalsArgs),
    /// Approve a pending action.
    Approve(CeoApproveArgs),
    /// Deny a pending action.
    Deny(CeoDenyArgs),
}

// ── Sub-arg structs ───────────────────────────────────────────────────────────

/// Arguments for `cascade ceo directive "<goal>"`.
#[derive(Debug, Args)]
pub struct CeoDirectiveArgs {
    /// The Founder's goal (free text). Wrap in quotes for multi-word directives.
    pub goal: String,

    /// Optional constraint (repeatable). Example: `--constraint "no external APIs"`.
    #[arg(long = "constraint", short = 'c', value_name = "TEXT")]
    pub constraints: Vec<String>,

    /// Output raw JSON instead of formatted text.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade ceo status`.
#[derive(Debug, Args)]
pub struct CeoStatusArgs {
    /// Output raw JSON instead of formatted text.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade ceo approvals`.
#[derive(Debug, Args)]
pub struct CeoApprovalsArgs {
    /// Output raw JSON instead of formatted text.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade ceo approve <task-id>`.
#[derive(Debug, Args)]
pub struct CeoApproveArgs {
    /// The task ID to approve (from `cascade ceo approvals`).
    pub task_id: String,

    /// Optional approval note.
    #[arg(long, value_name = "TEXT")]
    pub note: Option<String>,
}

/// Arguments for `cascade ceo deny <task-id>`.
#[derive(Debug, Args)]
pub struct CeoDenyArgs {
    /// The task ID to deny (from `cascade ceo approvals`).
    pub task_id: String,

    /// Optional denial note.
    #[arg(long, value_name = "TEXT")]
    pub note: Option<String>,
}

// ── IPC response types ────────────────────────────────────────────────────────

/// Mirrors `FounderReport` from cascade-agents (only fields we display).
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct FounderReport {
    session_id: String,
    directive_goal: String,
    summary: String,
    sub_tasks: Vec<SubTaskOutcome>,
    pending_approvals: Vec<ApprovalSummary>,
    generated_at: String,
}

/// One sub-task in the report.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct SubTaskOutcome {
    task_id: String,
    goal: String,
    status: String,
    #[serde(default)]
    summary: Option<String>,
}

/// Approval summary from `ceo_approvals`.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ApprovalSummary {
    task_id: String,
    description: String,
    access_level: String,
}

// ── Command dispatch ──────────────────────────────────────────────────────────

#[async_trait]
impl Command for CeoArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            CeoSubcommand::Directive(args) => run_directive(args).await,
            CeoSubcommand::Status(args) => run_status(args).await,
            CeoSubcommand::Approvals(args) => run_approvals(args).await,
            CeoSubcommand::Approve(args) => run_approve(args).await,
            CeoSubcommand::Deny(args) => run_deny(args).await,
        }
    }
}

// ── directive ─────────────────────────────────────────────────────────────────

async fn run_directive(args: &CeoDirectiveArgs) -> Result<()> {
    let client = IpcClient::new()?;

    let params = json!({
        "goal": args.goal,
        "constraints": args.constraints,
    });

    let report: FounderReport = client.send("ceo_directive", params).await?;

    if args.json {
        let raw = serde_json::to_string_pretty(&json!({
            "sessionId": report.session_id,
            "directiveGoal": report.directive_goal,
            "summary": report.summary,
            "subTaskCount": report.sub_tasks.len(),
            "pendingApprovals": report.pending_approvals.len(),
            "generatedAt": report.generated_at,
        }))
        .unwrap_or_default();
        println!("{raw}");
    } else {
        println!("CEO session started: {}", report.session_id);
        println!("Goal: {}", report.directive_goal);
        println!();
        println!("{}", report.summary);
        if !report.sub_tasks.is_empty() {
            println!();
            println!("Sub-tasks ({}):", report.sub_tasks.len());
            for t in &report.sub_tasks {
                let summary = t.summary.as_deref().unwrap_or("—");
                println!("  [{}] {} — {}", t.status, t.task_id, t.goal);
                if summary != "—" {
                    println!("       {summary}");
                }
            }
        }
        if !report.pending_approvals.is_empty() {
            println!();
            println!(
                "{} action(s) pending approval — run `cascade ceo approvals` to review.",
                report.pending_approvals.len()
            );
        }
    }
    Ok(())
}

// ── status ────────────────────────────────────────────────────────────────────

async fn run_status(args: &CeoStatusArgs) -> Result<()> {
    let client = IpcClient::new()?;

    let report: FounderReport = client.send("ceo_status", serde_json::Value::Null).await?;

    if args.json {
        let raw = serde_json::to_string_pretty(&json!({
            "sessionId": report.session_id,
            "directiveGoal": report.directive_goal,
            "summary": report.summary,
            "subTasks": report.sub_tasks.iter().map(|t| json!({
                "taskId": t.task_id,
                "goal": t.goal,
                "status": t.status,
                "summary": t.summary,
            })).collect::<Vec<_>>(),
            "pendingApprovals": report.pending_approvals.iter().map(|a| json!({
                "taskId": a.task_id,
                "description": a.description,
                "accessLevel": a.access_level,
            })).collect::<Vec<_>>(),
            "generatedAt": report.generated_at,
        }))
        .unwrap_or_default();
        println!("{raw}");
    } else {
        println!("Session:  {}", report.session_id);
        println!("Goal:     {}", report.directive_goal);
        println!("At:       {}", report.generated_at);
        println!();
        println!("{}", report.summary);
        if !report.sub_tasks.is_empty() {
            println!();
            println!("{:<40} {:<12} {}", "Task", "Status", "Summary");
            println!("{}", "-".repeat(70));
            for t in &report.sub_tasks {
                let summary = t.summary.as_deref().unwrap_or("—");
                println!("{:<40} {:<12} {}", t.goal, t.status, summary);
            }
        }
        if !report.pending_approvals.is_empty() {
            println!();
            println!(
                "{} action(s) pending — run `cascade ceo approvals`.",
                report.pending_approvals.len()
            );
        }
    }
    Ok(())
}

// ── approvals ─────────────────────────────────────────────────────────────────

async fn run_approvals(args: &CeoApprovalsArgs) -> Result<()> {
    let client = IpcClient::new()?;

    let approvals: Vec<ApprovalSummary> = client
        .send("ceo_approvals", serde_json::Value::Null)
        .await?;

    if args.json {
        let raw = serde_json::to_string_pretty(
            &approvals
                .iter()
                .map(|a| {
                    json!({
                        "taskId": a.task_id,
                        "description": a.description,
                        "accessLevel": a.access_level,
                    })
                })
                .collect::<Vec<_>>(),
        )
        .unwrap_or_default();
        println!("{raw}");
    } else if approvals.is_empty() {
        println!("No actions pending approval.");
    } else {
        println!("{:<36} {:<12} {}", "Task ID", "Access", "Description");
        println!("{}", "-".repeat(70));
        for a in &approvals {
            println!("{:<36} {:<12} {}", a.task_id, a.access_level, a.description);
        }
        println!();
        println!("Use `cascade ceo approve <id>` or `cascade ceo deny <id>` to act.");
    }
    Ok(())
}

// ── approve ───────────────────────────────────────────────────────────────────

async fn run_approve(args: &CeoApproveArgs) -> Result<()> {
    let client = IpcClient::new()?;

    let params = json!({
        "taskId": args.task_id,
        "note": args.note,
    });

    let result: serde_json::Value = client.send("ceo_approve", params).await?;

    let approved = result
        .get("approved")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    if approved {
        println!("Approved: {}", args.task_id);
    } else {
        println!("Approval result: {result}");
    }
    Ok(())
}

// ── deny ──────────────────────────────────────────────────────────────────────

async fn run_deny(args: &CeoDenyArgs) -> Result<()> {
    let client = IpcClient::new()?;

    let params = json!({
        "taskId": args.task_id,
        "note": args.note,
    });

    let result: serde_json::Value = client.send("ceo_deny", params).await?;

    let denied = result
        .get("denied")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    if denied {
        println!("Denied: {}", args.task_id);
    } else {
        println!("Denial result: {result}");
    }
    Ok(())
}

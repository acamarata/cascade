//! End-to-end validation of the REAL fleet dispatch path on a toy phase
//! (T-P7-E03-06).
//!
//! The engine's fleet path — prompt construction, retry/backoff,
//! `CASCADE_STEP_COMPLETE` marker parsing, step transitions, gate checks —
//! executes for real, but the CLI shell-out is replaced by a scripted
//! [`FleetRunner`] injected through `BuildEngine::with_fleet_runner`. This is
//! deterministic, needs no network and no live CLI, and therefore runs in
//! normal `cargo test`. The one genuinely-live variant is `#[ignore]`d and
//! additionally gated on `CASCADE_E2E_LIVE=1`.

use std::sync::{Arc, Mutex};

use cascade_core::{
    build::{BuildConfig, BuildEngine, FleetDispatcher, FleetOutcome, FleetRunner},
    pbd::{
        Epic, EpicStatus, NoExternalChecks, PbdStore, Phase, PhaseStatus, Sprint, SprintStatus,
        Step, StepStatus, Ticket, TicketStatus, Wave, WaveStatus,
    },
    routing::TaskClass,
};
use tempfile::TempDir;

/// Scripted answer for one dispatch: `(task_class, prompt) -> outcome`.
type Script = Box<dyn Fn(TaskClass, &str) -> FleetOutcome + Send + Sync>;

/// Scripted [`FleetRunner`]: records every `(TaskClass, prompt)` the real
/// dispatch path hands it, then answers from `script`.
struct ScriptedRunner {
    script: Script,
    calls: Mutex<Vec<(TaskClass, String)>>,
}

impl ScriptedRunner {
    fn new<F>(script: F) -> Arc<Self>
    where
        F: Fn(TaskClass, &str) -> FleetOutcome + Send + Sync + 'static,
    {
        Arc::new(Self {
            script: Box::new(script),
            calls: Mutex::new(Vec::new()),
        })
    }

    fn call_count(&self) -> usize {
        self.calls
            .lock()
            .expect("scripted runner calls mutex")
            .len()
    }
}

impl FleetRunner for ScriptedRunner {
    fn run(&self, task_class: TaskClass, prompt: &str) -> FleetOutcome {
        self.calls
            .lock()
            .expect("scripted runner calls mutex")
            .push((task_class, prompt.to_string()));
        (self.script)(task_class, prompt)
    }
}

/// Behave like a well-behaved fleet CLI: read the step list out of the
/// engine-built prompt (`- <step-id>: <title>` lines) and print one
/// `CASCADE_STEP_COMPLETE:<step-id>` line per step — exactly the documented
/// marker contract from `fleet_prompt`.
fn cooperative_cli(_task_class: TaskClass, prompt: &str) -> FleetOutcome {
    let markers: Vec<String> = prompt
        .lines()
        .filter_map(|line| line.trim().strip_prefix("- "))
        .filter_map(|entry| entry.split_once(": "))
        .map(|(step_id, _title)| format!("CASCADE_STEP_COMPLETE:{step_id}"))
        .collect();
    FleetOutcome::Success {
        stdout: format!("scripted fleet CLI finished:\n{}\n", markers.join("\n")),
    }
}

/// Seed a toy phase: 1 epic → 1 wave → 1 sprint → 4 XS/S tickets.
///
/// t01 (S, two steps) has no deps; t02 (S) and t04 (XS) depend on t01; t03
/// (S) depends on t02. The weight mix exercises the real `classify_ticket`
/// path: S → `Cheap`, XS → default `BulkExec`.
fn seed_toy_phase(store: &PbdStore) {
    store
        .save_phase(&Phase {
            id: "p-toy".into(),
            title: "Real dispatch toy phase".into(),
            status: PhaseStatus::Building,
            epics: vec!["e-toy".into()],
            started_at: None,
            closed_at: None,
            note: None,
        })
        .expect("save toy phase");
    store
        .save_epic(
            "p-toy",
            &Epic {
                id: "e-toy".into(),
                phase_id: "p-toy".into(),
                title: "Toy epic".into(),
                status: EpicStatus::Active,
                waves: vec!["w-toy".into()],
                depends_on: vec![],
                note: None,
            },
        )
        .expect("save toy epic");
    store
        .save_wave(
            "p-toy",
            "e-toy",
            &Wave {
                id: "w-toy".into(),
                epic_id: "e-toy".into(),
                title: "Toy wave".into(),
                status: WaveStatus::Active,
                sprints: vec!["s-toy".into()],
                note: None,
            },
        )
        .expect("save toy wave");
    store
        .save_sprint(
            "p-toy",
            "e-toy",
            "w-toy",
            &Sprint {
                id: "s-toy".into(),
                wave_id: "w-toy".into(),
                title: "Toy sprint".into(),
                status: SprintStatus::Active,
                tickets: vec!["t01".into(), "t02".into(), "t03".into(), "t04".into()],
                note: None,
            },
        )
        .expect("save toy sprint");

    let specs = [
        ("t01", "S", vec![] as Vec<&str>, 2),
        ("t02", "S", vec!["t01"], 1),
        ("t03", "S", vec!["t02"], 1),
        ("t04", "XS", vec!["t01"], 1),
    ];
    for (ticket_id, weight, depends_on, step_count) in specs {
        let steps = (1..=step_count)
            .map(|n| Step {
                id: format!("step-{n:02}"),
                title: format!("Toy step {n}"),
                status: StepStatus::Pending,
                note: None,
            })
            .collect();
        store
            .create_ticket(
                "p-toy",
                "e-toy",
                "w-toy",
                &Ticket {
                    id: ticket_id.into(),
                    sprint_id: "s-toy".into(),
                    title: format!("Toy ticket {ticket_id}"),
                    status: TicketStatus::Queue,
                    steps,
                    depends_on: depends_on.iter().map(|id| (*id).to_string()).collect(),
                    repo: None,
                    weight: Some(weight.into()),
                    note: None,
                    blocked_reason: None,
                },
            )
            .expect("create toy ticket");
    }
}

fn load_ticket(phases_root: &std::path::Path, ticket_id: &str) -> Ticket {
    PbdStore::new(phases_root)
        .load_ticket("p-toy", "e-toy", "w-toy", "s-toy", ticket_id)
        .unwrap_or_else(|_| panic!("load toy ticket {ticket_id}"))
}

fn seeded_store() -> (TempDir, std::path::PathBuf, PbdStore) {
    let temp = TempDir::new().expect("toy phase tempdir");
    let phases_root = temp.path().join("phases");
    let store = PbdStore::new(&phases_root);
    store.init().expect("initialize toy store");
    seed_toy_phase(&store);
    (temp, phases_root.clone(), store)
}

#[tokio::test]
async fn real_dispatch_path_completes_every_toy_ticket_with_scripted_runner() {
    let (_temp, phases_root, store) = seeded_store();
    let runner = ScriptedRunner::new(cooperative_cli);
    let engine = BuildEngine::new(
        store,
        FleetDispatcher,
        NoExternalChecks,
        BuildConfig::default(),
    )
    .with_fleet_runner(runner.clone());

    let result = engine.run_phase("p-toy").await.expect("real path run");

    assert!(
        result.success,
        "phase gate should pass: {:?}",
        result.errors
    );
    for ticket_id in ["t01", "t02", "t03", "t04"] {
        let ticket = load_ticket(&phases_root, ticket_id);
        assert_eq!(ticket.status, TicketStatus::Done, "{ticket_id} status");
        assert!(
            ticket
                .steps
                .iter()
                .all(|step| step.status == StepStatus::Passed),
            "{ticket_id} steps: {:?}",
            ticket.steps
        );
    }

    // One CLI invocation per ticket, no retries on success.
    assert_eq!(runner.call_count(), 4, "one call per ticket");
    let calls = runner
        .calls
        .lock()
        .expect("scripted runner calls mutex")
        .clone();
    let cheap = calls
        .iter()
        .filter(|(class, _)| *class == TaskClass::Cheap)
        .count();
    let bulk = calls
        .iter()
        .filter(|(class, _)| *class == TaskClass::BulkExec)
        .count();
    assert_eq!(
        (cheap, bulk),
        (3, 1),
        "S→Cheap, XS→BulkExec via real classify"
    );

    // Every prompt is the real engine-built fleet_prompt: it names the ticket
    // and teaches the CASCADE_STEP_COMPLETE marker contract.
    for (_class, prompt) in &calls {
        assert!(
            prompt.contains("Execute the self-contained ticket"),
            "engine-built prompt expected: {prompt}"
        );
        assert!(
            prompt.contains("CASCADE_STEP_COMPLETE:"),
            "prompt must state the marker contract: {prompt}"
        );
    }
}

#[tokio::test]
async fn failed_outcome_surfaces_error_after_bounded_retries_and_leaves_ticket_undone() {
    let (_temp, phases_root, store) = seeded_store();
    let runner = ScriptedRunner::new(|_task_class, _prompt| FleetOutcome::Failed {
        message: "claude exit 75: scripted failure".into(),
    });
    let engine = BuildEngine::new(
        store,
        FleetDispatcher,
        NoExternalChecks,
        BuildConfig::default(),
    )
    .with_fleet_runner(runner.clone());

    let error = engine
        .run_phase("p-toy")
        .await
        .expect_err("non-zero CLI outcome must surface an error");

    let message = error.to_string();
    assert!(
        message.contains("FleetDispatcher: CLI failed for ticket"),
        "error should name the CLI failure: {message}"
    );
    assert!(
        message.contains("after 3 attempts"),
        "error should report the bounded retry count: {message}"
    );
    assert_eq!(runner.call_count(), 3, "retries are bounded at 3");

    let t01 = load_ticket(&phases_root, "t01");
    assert_eq!(t01.status, TicketStatus::Active, "ticket stays Active");
    assert_ne!(t01.status, TicketStatus::Done);
    assert!(
        t01.steps
            .iter()
            .all(|step| step.status == StepStatus::Failed),
        "steps should be Failed: {:?}",
        t01.steps
    );
}

#[tokio::test]
async fn unavailable_outcome_surfaces_distinct_error_without_retry() {
    let (_temp, phases_root, store) = seeded_store();
    let runner = ScriptedRunner::new(|_task_class, _prompt| FleetOutcome::Unavailable {
        reason: "claude binary not found in PATH: scripted".into(),
    });
    let engine = BuildEngine::new(
        store,
        FleetDispatcher,
        NoExternalChecks,
        BuildConfig::default(),
    )
    .with_fleet_runner(runner.clone());

    let error = engine
        .run_phase("p-toy")
        .await
        .expect_err("missing CLI must surface an error");

    let message = error.to_string();
    assert!(
        message.contains("FleetDispatcher: no CLI available for ticket"),
        "distinct Unavailable error expected: {message}"
    );
    assert!(
        !message.contains("CLI failed"),
        "Unavailable must not be reported as a CLI failure: {message}"
    );
    assert_eq!(runner.call_count(), 1, "Unavailable is not retried");

    let t01 = load_ticket(&phases_root, "t01");
    assert_eq!(t01.status, TicketStatus::Active, "ticket stays Active");
    assert!(
        t01.steps
            .iter()
            .all(|step| step.status == StepStatus::Failed),
        "steps should be Failed: {:?}",
        t01.steps
    );
}

#[tokio::test]
async fn exit_zero_without_completion_marker_never_marks_ticket_done() {
    let (_temp, phases_root, store) = seeded_store();
    let runner = ScriptedRunner::new(|_task_class, _prompt| FleetOutcome::Success {
        stdout: "worked hard, printed no markers\n".into(),
    });
    let engine = BuildEngine::new(
        store,
        FleetDispatcher,
        NoExternalChecks,
        BuildConfig::default(),
    )
    .with_fleet_runner(runner.clone());

    let error = engine
        .run_phase("p-toy")
        .await
        .expect_err("marker-less success must fail the run");

    // The existing marker contract: exit 0 alone is NOT authoriative —
    // the CASCADE_STEP_COMPLETE markers are.
    assert!(
        error.to_string().contains("omitted completion markers"),
        "error should report the omitted markers: {error}"
    );
    assert_eq!(runner.call_count(), 1, "success is not retried");

    let t01 = load_ticket(&phases_root, "t01");
    assert_ne!(t01.status, TicketStatus::Done, "ticket must NOT be Done");
    assert!(
        t01.steps
            .iter()
            .all(|step| step.status == StepStatus::Failed),
        "marker-less steps must be Failed: {:?}",
        t01.steps
    );
}

#[tokio::test]
#[ignore = "shells real fleet CLIs over the network; opt in with CASCADE_E2E_LIVE=1 and run via cargo test -- --ignored"]
async fn live_real_fleet_runner_end_to_end_toy_phase() {
    if std::env::var("CASCADE_E2E_LIVE").ok().as_deref() != Some("1") {
        eprintln!("skipping live E2E: set CASCADE_E2E_LIVE=1 to opt in");
        return;
    }

    let (_temp, _phases_root, store) = seeded_store();
    // Default construction — RealFleetRunner shells whatever claude/opencode
    // is on PATH, exactly like `cascade build run --real --skip-externals`.
    let result = BuildEngine::new(
        store,
        FleetDispatcher,
        NoExternalChecks,
        BuildConfig::default(),
    )
    .run_phase("p-toy")
    .await
    .expect("live fleet CLI run");

    assert!(
        result.success,
        "live phase gate should pass: {:?}",
        result.errors
    );
}

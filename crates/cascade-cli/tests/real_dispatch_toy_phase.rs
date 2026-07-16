use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
};

#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;

use cascade_core::pbd::{
    Epic, EpicStatus, PbdStore, Phase, PhaseStatus, Sprint, SprintStatus, Step, StepStatus, Ticket,
    TicketStatus, Wave, WaveStatus,
};
use serial_test::serial;
use tempfile::TempDir;

fn cascade_bin() -> PathBuf {
    PathBuf::from(env!("CARGO_BIN_EXE_cascade"))
}

fn shell_quote(path: &Path) -> String {
    format!("'{}'", path.display().to_string().replace('\'', "'\\''"))
}

fn seed_real_toy_phase(store: &PbdStore, evidence_root: &Path) -> Vec<(String, PathBuf)> {
    store
        .save_phase(&Phase {
            id: "p-real-toy".into(),
            title: "Isolated real dispatcher test phase".into(),
            status: PhaseStatus::Building,
            epics: vec!["e-toy".into()],
            started_at: None,
            closed_at: None,
            note: None,
        })
        .expect("save real toy phase");
    store
        .save_epic(
            "p-real-toy",
            &Epic {
                id: "e-toy".into(),
                phase_id: "p-real-toy".into(),
                title: "Real toy epic".into(),
                status: EpicStatus::Active,
                waves: vec!["w-toy".into()],
                depends_on: vec![],
                note: None,
            },
        )
        .expect("save real toy epic");
    store
        .save_wave(
            "p-real-toy",
            "e-toy",
            &Wave {
                id: "w-toy".into(),
                epic_id: "e-toy".into(),
                title: "Real toy wave".into(),
                status: WaveStatus::Active,
                sprints: vec!["s-toy".into()],
                note: None,
            },
        )
        .expect("save real toy wave");
    store
        .save_sprint(
            "p-real-toy",
            "e-toy",
            "w-toy",
            &Sprint {
                id: "s-toy".into(),
                wave_id: "w-toy".into(),
                title: "Real toy sprint".into(),
                status: SprintStatus::Active,
                tickets: vec!["t01".into(), "t02".into(), "t03".into()],
                note: None,
            },
        )
        .expect("save real toy sprint");

    ["t01", "t02", "t03"]
        .iter()
        .enumerate()
        .map(|(index, ticket_id)| {
            let evidence_path = evidence_root.join(format!("{ticket_id}.txt"));
            let expected = format!("real-agent-evidence:{ticket_id}");
            let note = format!(
                "Security boundary: this is an isolated test fixture. Create only the file {} with exact content `{expected}`. Do not read or modify repository files, production phase files, credentials, configuration, or any other path.",
                evidence_path.display()
            );
            store
                .create_ticket(
                    "p-real-toy",
                    "e-toy",
                    "w-toy",
                    &Ticket {
                        id: (*ticket_id).into(),
                        sprint_id: "s-toy".into(),
                        title: format!("Create isolated evidence {ticket_id}"),
                        status: TicketStatus::Queue,
                        steps: vec![Step {
                            id: "produce-evidence".into(),
                            title: format!(
                                "Create {} containing {expected}",
                                evidence_path.display()
                            ),
                            status: StepStatus::Pending,
                            note: None,
                        }],
                        depends_on: if index == 0 {
                            vec![]
                        } else {
                            vec![format!("t{:02}", index)]
                        },
                        repo: None,
                        weight: Some("S".into()),
                        note: Some(note),
                        blocked_reason: None,
                    },
                )
                .expect("create real toy ticket");
            (expected, evidence_path)
        })
        .collect()
}

#[test]
#[ignore = "explicit opt-in: dispatches three real cheap-tier agents"]
#[serial(real_agent_e2e)]
#[cfg(unix)]
fn real_cli_dispatches_isolated_three_ticket_toy_phase() {
    assert_eq!(
        std::env::var("CASCADE_RUN_REAL_AGENT_E2E").as_deref(),
        Ok("1"),
        "set CASCADE_RUN_REAL_AGENT_E2E=1 to acknowledge real agent dispatch"
    );
    let home = PathBuf::from(std::env::var_os("HOME").expect("HOME is required"));
    let pooled_runner = home.join("bin/claude-run-pooled");
    let pooled_help = Command::new(&pooled_runner)
        .arg("--model")
        .arg("haiku")
        .output()
        .expect("run the canonical pooled Claude runner");
    assert!(
        String::from_utf8_lossy(&pooled_help.stderr).contains("--prompt-file required"),
        "canonical pooled Claude runner is unavailable at {}",
        pooled_runner.display()
    );

    let temp = TempDir::new().expect("isolated real toy tempdir");
    let phases_root = temp.path().join("phases");
    let evidence_root = temp.path().join("agent-evidence");
    let agent_bin_root = temp.path().join("agent-bin");
    let agent_runtime_root = temp.path().join("agent-runtime");
    fs::create_dir_all(&evidence_root).expect("create isolated evidence directory");
    fs::create_dir_all(&agent_bin_root).expect("create isolated agent binary directory");
    fs::create_dir_all(&agent_runtime_root).expect("create isolated agent runtime directory");
    let opencode_wrapper = agent_bin_root.join("opencode");
    fs::write(
        &opencode_wrapper,
        format!(
            "#!/bin/sh\n\
             if [ \"$1\" = \"run\" ]; then shift; fi\n\
             runtime={}\n\
             prompt_file=\"$runtime/prompt-$$.txt\"\n\
             log_file=\"$runtime/claude-$$.log\"\n\
             printf '%s\\n' \"$1\" > \"$prompt_file\"\n\
             if {} --model haiku --prompt-file \"$prompt_file\" --cwd {} --log \"$log_file\" --perm acceptEdits --max-turns 4 --timeout 180 --max-attempts 1; then\n\
               /bin/cat \"$log_file\"\n\
             else\n\
               code=$?\n\
               /bin/cat \"$log_file\" >&2\n\
               exit \"$code\"\n\
             fi\n",
            shell_quote(&agent_runtime_root),
            shell_quote(&pooled_runner),
            shell_quote(temp.path())
        ),
    )
    .expect("write isolated cheap-tier opencode wrapper");
    let mut wrapper_permissions = fs::metadata(&opencode_wrapper)
        .expect("read opencode wrapper metadata")
        .permissions();
    wrapper_permissions.set_mode(0o700);
    fs::set_permissions(&opencode_wrapper, wrapper_permissions)
        .expect("make opencode wrapper executable");
    let isolated_path = std::env::join_paths(
        std::iter::once(agent_bin_root.clone()).chain(std::env::split_paths(
            &std::env::var_os("PATH").expect("PATH is required"),
        )),
    )
    .expect("construct isolated PATH");
    let store = PbdStore::new(&phases_root);
    store.init().expect("initialize isolated real toy store");
    let expected_evidence = seed_real_toy_phase(&store, &evidence_root);

    let output = Command::new(cascade_bin())
        .args([
            "build",
            "run",
            "p-real-toy",
            "--real",
            "--phases",
        ])
        .arg(&phases_root)
        .env("PATH", isolated_path)
        .output()
        .expect("execute cascade build run --real");
    assert!(
        output.status.success(),
        "real toy phase failed; stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        String::from_utf8_lossy(&output.stdout).contains("phase p-real-toy complete"),
        "CLI did not report phase completion: {}",
        String::from_utf8_lossy(&output.stdout)
    );
    println!(
        "real dispatcher CLI: {}",
        String::from_utf8_lossy(&output.stdout).trim()
    );

    for (index, (expected, path)) in expected_evidence.iter().enumerate() {
        let ticket_id = format!("t{:02}", index + 1);
        let evidence = fs::read_to_string(path)
            .unwrap_or_else(|error| panic!("real agent evidence missing at {}: {error}", path.display()));
        assert_eq!(evidence.trim(), expected);
        let ticket = store
            .load_ticket(
                "p-real-toy",
                "e-toy",
                "w-toy",
                "s-toy",
                &ticket_id,
            )
            .expect("load real-dispatched toy ticket");
        assert_eq!(ticket.status, TicketStatus::Done);
        assert_eq!(ticket.steps[0].status, StepStatus::Passed);
        println!(
            "verified {ticket_id}: evidence={expected}, ticket=done, step=passed"
        );
    }
}

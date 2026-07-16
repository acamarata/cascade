use cascade_core::{
    build::{BuildEngine, MockDispatcher},
    pbd::{
        Epic, EpicStatus, NoExternalChecks, PbdStore, Phase, PhaseStatus, Sprint, SprintStatus,
        Step, StepStatus, Ticket, TicketStatus, Wave, WaveStatus,
    },
};
use tempfile::TempDir;

fn seed_toy_phase(store: &PbdStore) {
    store
        .save_phase(&Phase {
            id: "p-toy".into(),
            title: "Isolated dispatcher test phase".into(),
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
                tickets: vec!["t01".into(), "t02".into(), "t03".into()],
                note: None,
            },
        )
        .expect("save toy sprint");

    for (index, ticket_id) in ["t01", "t02", "t03"].iter().enumerate() {
        store
            .create_ticket(
                "p-toy",
                "e-toy",
                "w-toy",
                &Ticket {
                    id: (*ticket_id).into(),
                    sprint_id: "s-toy".into(),
                    title: format!("Toy ticket {ticket_id}"),
                    status: TicketStatus::Queue,
                    steps: vec![Step {
                        id: "produce-evidence".into(),
                        title: "Produce isolated evidence".into(),
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
                    note: None,
                    blocked_reason: None,
                },
            )
            .expect("create toy ticket");
    }
}

#[tokio::test]
async fn mock_dispatcher_remains_green_on_three_ticket_toy_phase() {
    let temp = TempDir::new().expect("toy phase tempdir");
    let phases_root = temp.path().join("phases");
    let store = PbdStore::new(&phases_root);
    store.init().expect("initialize toy store");
    seed_toy_phase(&store);

    let result = BuildEngine::new(store, MockDispatcher, NoExternalChecks, Default::default())
        .run_phase("p-toy")
        .await
        .expect("run toy phase through mock dispatcher");

    assert!(result.success, "phase gate errors: {:?}", result.errors);
    let persisted = PbdStore::new(phases_root);
    for ticket_id in ["t01", "t02", "t03"] {
        let ticket = persisted
            .load_ticket("p-toy", "e-toy", "w-toy", "s-toy", ticket_id)
            .expect("load completed toy ticket");
        assert_eq!(ticket.status, TicketStatus::Done);
        assert_eq!(ticket.steps[0].status, StepStatus::Passed);
    }
}

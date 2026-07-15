use crate::pbd::{
    schema::{
        Epic, EpicStatus, Phase, PhaseStatus, Sprint, SprintStatus, Step, StepStatus, Ticket,
        TicketStatus, Wave, WaveStatus,
    },
    store::PbdStore,
};
use tempfile::TempDir;

pub(super) fn mk_store(tmp: &TempDir) -> PbdStore {
    let root = tmp.path().join("phases");
    let store = PbdStore::new(&root);
    store.init().expect("init");
    store
}

/// Build a minimal phase: 1 epic → 1 wave → 1 sprint → 3 tickets (t3 depends on t1).
pub(super) fn seed_phase(store: &PbdStore) {
    let phase = Phase {
        id: "p1".into(),
        title: "Test Phase".into(),
        status: PhaseStatus::Building,
        epics: vec!["e01".into()],
        started_at: None,
        closed_at: None,
        note: None,
    };
    store.save_phase(&phase).expect("save phase");

    let epic = Epic {
        id: "e01".into(),
        phase_id: "p1".into(),
        title: "Epic 01".into(),
        status: EpicStatus::Active,
        waves: vec!["w01".into()],
        depends_on: vec![],
        note: None,
    };
    store.save_epic("p1", &epic).expect("save epic");

    let wave = Wave {
        id: "w01".into(),
        epic_id: "e01".into(),
        title: "Wave 01".into(),
        status: WaveStatus::Active,
        sprints: vec!["s01".into()],
        note: None,
    };
    store.save_wave("p1", "e01", &wave).expect("save wave");

    let sprint = Sprint {
        id: "s01".into(),
        wave_id: "w01".into(),
        title: "Sprint 01".into(),
        status: SprintStatus::Active,
        tickets: vec!["t01".into(), "t02".into(), "t03".into()],
        note: None,
    };
    store
        .save_sprint("p1", "e01", "w01", &sprint)
        .expect("save sprint");

    let step = Step {
        id: "step-01".into(),
        title: "Implement".into(),
        status: StepStatus::Pending,
        note: None,
    };

    for ticket_id in &["t01", "t02"] {
        let ticket = Ticket {
            id: (*ticket_id).into(),
            sprint_id: "s01".into(),
            title: format!("Ticket {ticket_id}"),
            status: TicketStatus::Queue,
            steps: vec![step.clone()],
            depends_on: vec![],
            repo: None,
            weight: Some("M".into()),
            note: None,
            blocked_reason: None,
        };
        store
            .create_ticket("p1", "e01", "w01", &ticket)
            .expect("create ticket");
    }

    // t03 depends on t01
    let t03 = Ticket {
        id: "t03".into(),
        sprint_id: "s01".into(),
        title: "Ticket t03".into(),
        status: TicketStatus::Queue,
        steps: vec![step],
        depends_on: vec!["t01".into()],
        repo: None,
        weight: Some("S".into()),
        note: None,
        blocked_reason: None,
    };
    store
        .create_ticket("p1", "e01", "w01", &t03)
        .expect("create t03");
}

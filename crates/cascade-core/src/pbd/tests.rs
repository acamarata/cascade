//! PBD engine integration tests.
//!
//! Uses tempdir for all filesystem operations.
//! All tests are deterministic and serialized via `#[serial(global_env)]`.

#[cfg(test)]
mod tests {
    use serial_test::serial;
    use tempfile::TempDir;

    use crate::pbd::{
        protocol::{run_eoe, run_eop, run_eos, run_eot, run_eow, NoExternalChecks},
        schema::{
            Epic, EpicStatus, Phase, PhaseStatus, Sprint, SprintStatus, Step, StepStatus, Ticket,
            TicketStatus, Wave, WaveStatus,
        },
        store::PbdStore,
    };

    // ── Helpers ───────────────────────────────────────────────────────────────

    fn mk_store(tmp: &TempDir) -> PbdStore {
        let root = tmp.path().join("phases");
        let store = PbdStore::new(&root);
        store.init().expect("init");
        store
    }

    fn mk_phase(id: &str) -> Phase {
        Phase {
            id: id.to_string(),
            title: format!("Phase {id}"),
            status: PhaseStatus::Planning,
            epics: vec![],
            started_at: None,
            closed_at: None,
            note: None,
        }
    }

    fn mk_epic(id: &str, phase_id: &str) -> Epic {
        Epic {
            id: id.to_string(),
            phase_id: phase_id.to_string(),
            title: format!("Epic {id}"),
            status: EpicStatus::Planned,
            waves: vec![],
            depends_on: vec![],
            note: None,
        }
    }

    fn mk_wave(id: &str, epic_id: &str) -> Wave {
        Wave {
            id: id.to_string(),
            epic_id: epic_id.to_string(),
            title: format!("Wave {id}"),
            status: WaveStatus::Queued,
            sprints: vec![],
            note: None,
        }
    }

    fn mk_sprint(id: &str, wave_id: &str) -> Sprint {
        Sprint {
            id: id.to_string(),
            wave_id: wave_id.to_string(),
            title: format!("Sprint {id}"),
            status: SprintStatus::Queued,
            tickets: vec![],
            note: None,
        }
    }

    fn mk_ticket(id: &str, sprint_id: &str) -> Ticket {
        Ticket {
            id: id.to_string(),
            sprint_id: sprint_id.to_string(),
            title: format!("Ticket {id}"),
            status: TicketStatus::Planned,
            steps: vec![],
            depends_on: vec![],
            repo: None,
            weight: None,
            note: None,
            blocked_reason: None,
        }
    }

    fn mk_step(id: &str) -> Step {
        Step {
            id: id.to_string(),
            title: format!("Step {id}"),
            status: StepStatus::Pending,
            note: None,
        }
    }

    /// Build a full minimal hierarchy in the store.
    fn build_hierarchy(store: &PbdStore) {
        let phase = mk_phase("p1");
        store.create_phase(&phase).expect("create phase");

        let epic = mk_epic("e01", "p1");
        store.create_epic(&epic).expect("create epic");

        let wave = mk_wave("w01", "e01");
        store.create_wave("p1", &wave).expect("create wave");

        let sprint = mk_sprint("s01", "w01");
        store.create_sprint("p1", "e01", &sprint).expect("create sprint");

        let ticket = mk_ticket("t01", "s01");
        store.create_ticket("p1", "e01", "w01", &ticket).expect("create ticket");
    }

    // ── Tests ─────────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn test_create_hierarchy() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        let phases = store.list_phases().expect("list phases");
        assert_eq!(phases.len(), 1);
        assert_eq!(phases[0].id, "p1");

        let epics = store.list_epics("p1").expect("list epics");
        assert_eq!(epics.len(), 1);

        let waves = store.list_waves("p1", "e01").expect("list waves");
        assert_eq!(waves.len(), 1);

        let sprints = store.list_sprints("p1", "e01", "w01").expect("list sprints");
        assert_eq!(sprints.len(), 1);

        let tickets = store.list_tickets("p1", "e01", "w01", "s01").expect("list tickets");
        assert_eq!(tickets.len(), 1);
        assert_eq!(tickets[0].status, TicketStatus::Planned);
    }

    #[test]
    #[serial(global_env)]
    fn test_status_transitions_valid() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Phase: planning → ready_to_build → building
        store.transition_phase("p1", PhaseStatus::ReadyToBuild).expect("phase transition");
        let p = store.load_phase("p1").unwrap();
        assert_eq!(p.status, PhaseStatus::ReadyToBuild);

        store.transition_phase("p1", PhaseStatus::Building).expect("phase to building");
        let p = store.load_phase("p1").unwrap();
        assert_eq!(p.status, PhaseStatus::Building);

        // Ticket: planned → queue → active → done
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .expect("to queue");
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .expect("to active");
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Done, None)
            .expect("to done");

        let t = store.load_ticket("p1", "e01", "w01", "s01", "t01").unwrap();
        assert_eq!(t.status, TicketStatus::Done);
    }

    #[test]
    #[serial(global_env)]
    fn test_bad_transition_rejected() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Phase: planning → qa is invalid (must go via ready_to_build → building → qa)
        let result = store.transition_phase("p1", PhaseStatus::Qa);
        assert!(
            result.is_err(),
            "planning → qa should be rejected"
        );

        // Ticket: planned → done is invalid
        let result = store.transition_ticket(
            "p1",
            "e01",
            "w01",
            "s01",
            "t01",
            TicketStatus::Done,
            None,
        );
        assert!(result.is_err(), "planned → done should be rejected");
    }

    #[test]
    #[serial(global_env)]
    fn test_events_jsonl_append_only() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Transition ticket twice → 2 events beyond the created events
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .expect("queue");
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .expect("active");

        let events = store.read_events().expect("read events");
        // At minimum the two transitions were appended
        assert!(events.len() >= 2, "events should be appended, got {}", events.len());

        // Verify ordering: earlier events have earlier or equal timestamps
        for i in 1..events.len() {
            assert!(
                events[i - 1].ts <= events[i].ts,
                "events out of order at index {i}"
            );
        }
    }

    #[test]
    #[serial(global_env)]
    fn test_index_regen() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        let index = store.read_index().expect("read index");
        let kinds: Vec<&str> = index.entries.iter().map(|e| e.kind.as_str()).collect();
        assert!(kinds.contains(&"phase"), "index missing phase");
        assert!(kinds.contains(&"epic"), "index missing epic");
        assert!(kinds.contains(&"wave"), "index missing wave");
        assert!(kinds.contains(&"sprint"), "index missing sprint");
        assert!(kinds.contains(&"ticket"), "index missing ticket");
    }

    #[test]
    #[serial(global_env)]
    fn test_current_pointers() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Activate the hierarchy
        store.transition_phase("p1", PhaseStatus::ReadyToBuild).unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store.transition_epic("p1", "e01", EpicStatus::Active).unwrap();
        store.transition_wave("p1", "e01", "w01", WaveStatus::Active).unwrap();
        store.transition_sprint("p1", "e01", "w01", "s01", SprintStatus::Active).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        let ptr = store.read_current().expect("read current");
        assert_eq!(ptr.active_phase.as_deref(), Some("p1"));
        assert_eq!(ptr.active_epic.as_deref(), Some("e01"));
        assert_eq!(ptr.active_wave.as_deref(), Some("w01"));
        assert_eq!(ptr.active_sprint.as_deref(), Some("s01"));
        assert!(ptr.active_tickets.contains(&"t01".to_string()));
    }

    #[test]
    #[serial(global_env)]
    fn test_eot_fails_with_pending_steps() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Add a pending step to the ticket
        let mut ticket = store.load_ticket("p1", "e01", "w01", "s01", "t01").unwrap();
        ticket.steps.push(mk_step("step1"));
        store.save_ticket("p1", "e01", "w01", &ticket).unwrap();

        // Activate through the hierarchy
        store.transition_phase("p1", PhaseStatus::ReadyToBuild).unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        // EOT should fail because step1 is still pending
        let result = run_eot(&store, "p1", "e01", "w01", "s01", "t01").expect("run eot");
        assert!(!result.success, "eot should fail with pending step");
        assert!(
            result.errors.iter().any(|e| e.contains("step1")),
            "error should mention step1"
        );
    }

    #[test]
    #[serial(global_env)]
    fn test_eot_succeeds_with_all_passed_steps() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Add a passed step
        let mut ticket = store.load_ticket("p1", "e01", "w01", "s01", "t01").unwrap();
        let mut step = mk_step("step1");
        step.status = StepStatus::Passed;
        ticket.steps.push(step);
        store.save_ticket("p1", "e01", "w01", &ticket).unwrap();

        store.transition_phase("p1", PhaseStatus::ReadyToBuild).unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        let result = run_eot(&store, "p1", "e01", "w01", "s01", "t01").expect("run eot");
        assert!(result.success, "eot should succeed: {:?}", result.errors);

        let t = store.load_ticket("p1", "e01", "w01", "s01", "t01").unwrap();
        assert_eq!(t.status, TicketStatus::Done);
    }

    #[test]
    #[serial(global_env)]
    fn test_eos_fails_with_undone_tickets() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Sprint has ticket t01 still in planned state
        let result = run_eos(&store, "p1", "e01", "w01", "s01").expect("run eos");
        assert!(!result.success, "eos should fail with undone ticket");
    }

    #[test]
    #[serial(global_env)]
    fn test_full_lifecycle_eot_eos_eow_eoe_eop() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Activate hierarchy
        store.transition_phase("p1", PhaseStatus::ReadyToBuild).unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store.transition_epic("p1", "e01", EpicStatus::Active).unwrap();
        store.transition_wave("p1", "e01", "w01", WaveStatus::Active).unwrap();
        store.transition_sprint("p1", "e01", "w01", "s01", SprintStatus::Active).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        // EOT
        let eot = run_eot(&store, "p1", "e01", "w01", "s01", "t01").unwrap();
        assert!(eot.success, "eot: {:?}", eot.errors);

        // EOS
        let eos = run_eos(&store, "p1", "e01", "w01", "s01").unwrap();
        assert!(eos.success, "eos: {:?}", eos.errors);

        // EOW
        let eow = run_eow(&store, "p1", "e01", "w01").unwrap();
        assert!(eow.success, "eow: {:?}", eow.errors);

        // EOE
        let eoe = run_eoe(&store, "p1", "e01").unwrap();
        assert!(eoe.success, "eoe: {:?}", eoe.errors);

        // EOP
        let eop = run_eop(&store, "p1", &NoExternalChecks).unwrap();
        assert!(eop.success, "eop: {:?}", eop.errors);

        // Verify final phase status
        let phase = store.load_phase("p1").unwrap();
        assert_eq!(phase.status, PhaseStatus::Shipped);
    }

    #[test]
    #[serial(global_env)]
    fn test_step_transition_validation() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Add a pending step
        let mut ticket = store.load_ticket("p1", "e01", "w01", "s01", "t01").unwrap();
        ticket.steps.push(mk_step("step1"));
        store.save_ticket("p1", "e01", "w01", &ticket).unwrap();

        // pending → running is valid
        store
            .transition_step("p1", "e01", "w01", "s01", "t01", "step1", StepStatus::Running)
            .expect("pending → running");

        // running → passed is valid
        store
            .transition_step("p1", "e01", "w01", "s01", "t01", "step1", StepStatus::Passed)
            .expect("running → passed");

        // passed → running is INVALID
        let bad = store.transition_step(
            "p1",
            "e01",
            "w01",
            "s01",
            "t01",
            "step1",
            StepStatus::Running,
        );
        assert!(bad.is_err(), "passed → running should be rejected");
    }

    #[test]
    #[serial(global_env)]
    fn test_events_ordering_after_multiple_writes() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);

        // Multiple phases in quick succession
        let p1 = mk_phase("p1");
        store.create_phase(&p1).unwrap();
        let p2 = mk_phase("p2");
        store.create_phase(&p2).unwrap();

        let events = store.read_events().unwrap();
        assert!(events.len() >= 2);

        // All events should parse correctly (no corrupt lines)
        for ev in &events {
            assert!(!ev.id.is_empty());
            assert!(!ev.from.is_empty());
            assert!(!ev.to.is_empty());
        }
    }
}

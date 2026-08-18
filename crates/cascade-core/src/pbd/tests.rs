//! PBD engine integration tests.
//!
//! Uses tempdir for all filesystem operations.
//! All tests are deterministic and serialized via `#[serial(global_env)]`.

#[cfg(test)]
#[allow(clippy::module_inception)]
mod tests {
    use serial_test::serial;
    use tempfile::TempDir;

    use crate::pbd::{
        external_checks::{BuildCheck, RealExternalChecks},
        protocol::{run_eoe, run_eop, run_eos, run_eot, run_eow, ExternalChecks, NoExternalChecks},
        schema::{
            Epic, EpicStatus, EventLevel, Phase, PhaseStatus, Sprint, SprintStatus, Step,
            StepStatus, Ticket, TicketStatus, Wave, WaveStatus,
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
        store
            .create_sprint("p1", "e01", &sprint)
            .expect("create sprint");

        let ticket = mk_ticket("t01", "s01");
        store
            .create_ticket("p1", "e01", "w01", &ticket)
            .expect("create ticket");
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

        let sprints = store
            .list_sprints("p1", "e01", "w01")
            .expect("list sprints");
        assert_eq!(sprints.len(), 1);

        let tickets = store
            .list_tickets("p1", "e01", "w01", "s01")
            .expect("list tickets");
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
        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .expect("phase transition");
        let p = store.load_phase("p1").unwrap();
        assert_eq!(p.status, PhaseStatus::ReadyToBuild);

        store
            .transition_phase("p1", PhaseStatus::Building)
            .expect("phase to building");
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

        // Phase: planning → wrapup is invalid (must go via ready_to_build → building → wrapup)
        let result = store.transition_phase("p1", PhaseStatus::Wrapup);
        assert!(result.is_err(), "planning → wrapup should be rejected");

        // Ticket: planned → done is invalid
        let result =
            store.transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Done, None);
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
        assert!(
            events.len() >= 2,
            "events should be appended, got {}",
            events.len()
        );

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
        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_epic("p1", "e01", EpicStatus::Active)
            .unwrap();
        store
            .transition_wave("p1", "e01", "w01", WaveStatus::Active)
            .unwrap();
        store
            .transition_sprint("p1", "e01", "w01", "s01", SprintStatus::Active)
            .unwrap();
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
        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        // EOT should fail because step1 is still pending
        let result =
            run_eot(&store, "p1", "e01", "w01", "s01", "t01", &NoExternalChecks).expect("run eot");
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

        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        let result =
            run_eot(&store, "p1", "e01", "w01", "s01", "t01", &NoExternalChecks).expect("run eot");
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
        let result =
            run_eos(&store, "p1", "e01", "w01", "s01", &NoExternalChecks).expect("run eos");
        assert!(!result.success, "eos should fail with undone ticket");
    }

    #[test]
    #[serial(global_env)]
    fn test_full_lifecycle_eot_eos_eow_eoe_eop() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Activate hierarchy
        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_epic("p1", "e01", EpicStatus::Active)
            .unwrap();
        store
            .transition_wave("p1", "e01", "w01", WaveStatus::Active)
            .unwrap();
        store
            .transition_sprint("p1", "e01", "w01", "s01", SprintStatus::Active)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        // EOT
        let eot = run_eot(&store, "p1", "e01", "w01", "s01", "t01", &NoExternalChecks).unwrap();
        assert!(eot.success, "eot: {:?}", eot.errors);

        // EOS
        let eos = run_eos(&store, "p1", "e01", "w01", "s01", &NoExternalChecks).unwrap();
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
        assert_eq!(phase.status, PhaseStatus::Wrapup);
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
            .transition_step(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step1",
                StepStatus::Running,
            )
            .expect("pending → running");

        // running → passed is valid
        store
            .transition_step(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step1",
                StepStatus::Passed,
            )
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
    fn test_step_transition_records_caller_supplied_actor() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Two pending steps on one ticket: one via the default path, one via
        // the actor-aware path.
        let mut ticket = store.load_ticket("p1", "e01", "w01", "s01", "t01").unwrap();
        ticket.steps.push(mk_step("step1"));
        ticket.steps.push(mk_step("step2"));
        store.save_ticket("p1", "e01", "w01", &ticket).unwrap();

        // Non-fleet caller (manual/CLI): default actor must be unchanged.
        store
            .transition_step(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step1",
                StepStatus::Running,
            )
            .expect("default transition_step");

        // Fleet caller: the supplied actor is recorded verbatim.
        store
            .transition_step_as(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step2",
                StepStatus::Running,
                "fleet/claude/BulkExec",
            )
            .expect("actor-aware transition_step_as");

        let step_actors: Vec<(String, String)> = store
            .read_events()
            .unwrap()
            .into_iter()
            .filter(|ev| ev.level == EventLevel::Step)
            .map(|ev| (ev.id, ev.actor))
            .collect();
        assert_eq!(
            step_actors,
            vec![
                ("step1".to_string(), "cascade-cli".to_string()),
                ("step2".to_string(), "fleet/claude/BulkExec".to_string()),
            ],
            "each step event must carry the actor of the path that ran it"
        );
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

    // ── ExternalChecks integration ────────────────────────────────────────────

    /// A mock that always injects a fixed error.
    struct AlwaysFailChecks {
        msg: &'static str,
    }

    impl ExternalChecks for AlwaysFailChecks {
        fn run_checks(&self, _context_id: &str) -> cascade_types::error::Result<Vec<String>> {
            Ok(vec![self.msg.to_string()])
        }
    }

    #[test]
    #[serial(global_env)]
    fn test_eot_external_check_failure_blocks_gate() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        let bad = AlwaysFailChecks {
            msg: "build failed",
        };
        let result = run_eot(&store, "p1", "e01", "w01", "s01", "t01", &bad).expect("run eot");
        assert!(!result.success, "external failure should block EOT");
        assert!(
            result.errors.iter().any(|e| e.contains("build failed")),
            "error should propagate: {:?}",
            result.errors
        );
    }

    #[test]
    #[serial(global_env)]
    fn test_eos_external_check_failure_blocks_gate() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Make ticket done so only external check can fail
        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();
        run_eot(&store, "p1", "e01", "w01", "s01", "t01", &NoExternalChecks).unwrap();

        let bad = AlwaysFailChecks {
            msg: "sprint check fail",
        };
        let result = run_eos(&store, "p1", "e01", "w01", "s01", &bad).expect("run eos");
        assert!(!result.success, "external failure should block EOS");
        assert!(
            result
                .errors
                .iter()
                .any(|e| e.contains("sprint check fail")),
            "error should propagate: {:?}",
            result.errors
        );
    }

    #[test]
    #[serial(global_env)]
    fn test_eop_external_check_failure_blocks_gate() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        // Complete the full hierarchy so only the external check can fail
        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_epic("p1", "e01", EpicStatus::Active)
            .unwrap();
        store
            .transition_wave("p1", "e01", "w01", WaveStatus::Active)
            .unwrap();
        store
            .transition_sprint("p1", "e01", "w01", "s01", SprintStatus::Active)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();
        run_eot(&store, "p1", "e01", "w01", "s01", "t01", &NoExternalChecks).unwrap();
        run_eos(&store, "p1", "e01", "w01", "s01", &NoExternalChecks).unwrap();
        run_eow(&store, "p1", "e01", "w01").unwrap();
        run_eoe(&store, "p1", "e01").unwrap();

        let bad = AlwaysFailChecks {
            msg: "phase check fail",
        };
        let result = run_eop(&store, "p1", &bad).expect("run eop");
        assert!(!result.success, "external failure should block EOP");
        assert!(
            result.errors.iter().any(|e| e.contains("phase check fail")),
            "error should propagate: {:?}",
            result.errors
        );
    }

    // ── pews-01: phase lifecycle / ready-gate tests ───────────────────────────

    #[test]
    #[serial(global_env)]
    fn test_illegal_transition_rejected() {
        // Opening is the new initial lifecycle step; Planning → Wrapup is illegal.
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);

        let phase = Phase {
            id: "p1".to_string(),
            title: "Phase 1".to_string(),
            status: PhaseStatus::Opening,
            epics: vec![],
            started_at: None,
            closed_at: None,
            note: None,
        };
        store.create_phase(&phase).expect("create phase");

        // Opening → Building is illegal (must go Opening → Planning → ReadyToBuild → Building).
        let result = store.transition_phase("p1", PhaseStatus::Building);
        assert!(result.is_err(), "Opening → Building should be rejected");

        // Opening → Planning is legal.
        store
            .transition_phase("p1", PhaseStatus::Planning)
            .expect("Opening → Planning should be allowed");
    }

    #[test]
    #[serial(global_env)]
    fn test_ready_gate_blocks_empty_tree() {
        // Planning → ReadyToBuild should be blocked when the phase has no epics.
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);

        // Phase with no epics.
        let phase = mk_phase("p1");
        store.create_phase(&phase).expect("create phase");

        let result = store.transition_phase("p1", PhaseStatus::ReadyToBuild);
        assert!(
            result.is_err(),
            "Planning → ReadyToBuild should be blocked when no epics exist"
        );
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("gate") || msg.contains("epic"),
            "error should mention gate/epic: {msg}"
        );
    }

    #[test]
    #[serial(global_env)]
    fn test_ready_gate_blocks_when_no_tickets() {
        // Planning → ReadyToBuild should be blocked when epics exist but have no tickets.
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);

        let phase = mk_phase("p1");
        store.create_phase(&phase).expect("create phase");
        let epic = mk_epic("e01", "p1");
        store.create_epic(&epic).expect("create epic");
        // No waves/sprints/tickets created.

        let result = store.transition_phase("p1", PhaseStatus::ReadyToBuild);
        assert!(
            result.is_err(),
            "Planning → ReadyToBuild should be blocked when no tickets exist"
        );
    }

    #[test]
    #[serial(global_env)]
    fn test_ready_gate_allows_populated_tree() {
        // Planning → ReadyToBuild should succeed when at least one ticket exists.
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store); // creates phase+epic+wave+sprint+ticket

        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .expect("Ready gate should pass with a populated epic/ticket tree");
        let p = store.load_phase("p1").unwrap();
        assert_eq!(p.status, PhaseStatus::ReadyToBuild);
    }

    #[test]
    #[serial(global_env)]
    fn test_old_serialized_statuses_still_deserialize() {
        // Old on-disk values (ready_to_build, building, qa, shipped) must still parse
        // via serde aliases.
        let cases = [
            ("ready_to_build", PhaseStatus::ReadyToBuild),
            ("ready", PhaseStatus::ReadyToBuild),
            ("building", PhaseStatus::Building),
            ("build", PhaseStatus::Building),
            ("qa", PhaseStatus::Wrapup),
            ("shipped", PhaseStatus::Wrapup),
            ("wrapup", PhaseStatus::Wrapup),
            ("opening", PhaseStatus::Opening),
            ("planning", PhaseStatus::Planning),
            ("archived", PhaseStatus::Archived),
        ];
        for (raw, expected) in &cases {
            let yaml = raw.to_string();
            let parsed: PhaseStatus = serde_yaml::from_str(&yaml)
                .unwrap_or_else(|e| panic!("Failed to parse '{raw}': {e}"));
            assert_eq!(parsed, *expected, "Mismatch for raw value '{raw}'");
        }
    }

    // Also locks `env_path`: this spawns a real `true` command resolved via
    // PATH — without it, a concurrently-running PATH-mutating test
    // (build/dispatchers.rs, build/engine.rs) can make the spawn fail.
    #[test]
    #[serial(global_env, env_path)]
    fn test_real_external_checks_passing_command_allows_gate() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_hierarchy(&store);

        store
            .transition_phase("p1", PhaseStatus::ReadyToBuild)
            .unwrap();
        store.transition_phase("p1", PhaseStatus::Building).unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Queue, None)
            .unwrap();
        store
            .transition_ticket("p1", "e01", "w01", "s01", "t01", TicketStatus::Active, None)
            .unwrap();

        // A passing `true` command should not block EOT
        let checks = RealExternalChecks::new().with_build(BuildCheck::new("smoke", "true"));
        let result = run_eot(&store, "p1", "e01", "w01", "s01", "t01", &checks).expect("run eot");
        assert!(
            result.success,
            "passing command should allow EOT: {:?}",
            result.errors
        );
    }
}

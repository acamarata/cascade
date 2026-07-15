use super::{
    engine::BuildEngine,
    test_support::{mk_store, seed_phase},
};
use tempfile::TempDir;

#[tokio::test]
async fn engine_runs_phase_to_completion() {
    let tmp = TempDir::new().expect("tmpdir");
    let store = mk_store(&tmp);
    seed_phase(&store);

    let engine = BuildEngine::new_mock(store);
    let result = engine.run_phase("p1").await.expect("run_phase");

    assert!(
        result.success,
        "EOP gate should pass; errors: {:?}",
        result.errors
    );
    assert_eq!(result.level, "phase");
}

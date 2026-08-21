//! Panic hook + supervised task spawning for daemon subsystems.
//!
//! Purpose: make a panic inside a spawned task VISIBLE and, for subsystems
//!   that can safely be restarted, RECOVERABLE. Before this module the daemon
//!   installed no `std::panic::set_hook` at all, and every subsystem was
//!   launched with a bare `tokio::spawn` whose `JoinHandle` was dropped — so a
//!   panic in the quota poller, the fleet poller or the RAG pipeline killed
//!   that subsystem silently. The daemon kept running, reported healthy, and
//!   simply stopped doing that piece of work (T-P7-E25-12).
//!
//! Inputs:  a subsystem name, a [`RestartPolicy`], a shutdown token, and a
//!          FACTORY that produces the subsystem future. A factory rather than a
//!          future is what makes restart possible: a future is consumed by the
//!          first run.
//! Outputs: a `JoinHandle<()>` for the supervising task.
//! Constraints:
//!   - Restarting is opt-in per subsystem. A task that owns exclusive external
//!     state (a bound listener, an installed hook) must use
//!     [`RestartPolicy::Never`] — restarting it would double-bind or
//!     double-register.
//!   - Supervision never masks shutdown: once the token is cancelled, a panic
//!     is logged but not restarted.
//!
//! SPORT: MASTER-DAEMON.md → task_supervision (T-P7-E25-12)

use std::future::Future;
use std::time::Duration;

use futures_util::FutureExt;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;
use tracing::{error, info, warn};

// ── Panic hook ────────────────────────────────────────────────────────────────

/// Install a process-wide panic hook that reports panics through `tracing`.
///
/// Without this, a panic prints to stderr in the default format and never
/// reaches the daemon's structured log — so a panic in a background task left
/// no record in the file the operator actually reads.
///
/// The previous hook is chained, so the standard message (and any
/// `RUST_BACKTRACE` output) still appears.
///
/// Safe to call more than once; only the first call installs.
pub fn install_panic_hook() {
    use std::sync::Once;
    static ONCE: Once = Once::new();

    ONCE.call_once(|| {
        let previous = std::panic::take_hook();
        std::panic::set_hook(Box::new(move |info| {
            let location = info
                .location()
                .map(|l| format!("{}:{}:{}", l.file(), l.line(), l.column()))
                .unwrap_or_else(|| "<unknown location>".to_owned());
            let thread = std::thread::current();
            let thread_name = thread.name().unwrap_or("<unnamed>").to_owned();

            error!(
                panic.location = %location,
                panic.thread = %thread_name,
                panic.message = %panic_message(info.payload()),
                "PANIC in daemon process"
            );

            // Chain to the default hook so stderr and RUST_BACKTRACE behave as
            // usual — this hook adds a record, it does not replace one.
            previous(info);
        }));
    });
}

/// Best-effort extraction of a panic payload as a string.
///
/// `panic!("literal")` yields `&str`; `panic!("{x}")` yields `String`; anything
/// else is opaque and reported as such rather than guessed at.
fn panic_message(payload: &(dyn std::any::Any + Send)) -> String {
    if let Some(s) = payload.downcast_ref::<&str>() {
        (*s).to_owned()
    } else if let Some(s) = payload.downcast_ref::<String>() {
        s.clone()
    } else {
        "<non-string panic payload>".to_owned()
    }
}

// ── Restart policy ────────────────────────────────────────────────────────────

/// What supervision does when a subsystem panics.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RestartPolicy {
    /// Log the panic and let the subsystem stay down.
    ///
    /// Correct for anything holding exclusive external state — a bound TCP
    /// listener, a registered hook — where a second run would conflict with
    /// the first.
    Never,

    /// Log the panic and restart, up to `max_restarts` times.
    ///
    /// `backoff` is waited before each restart so a subsystem that panics
    /// immediately cannot spin.
    OnPanic {
        /// Maximum restarts before giving up permanently.
        max_restarts: u32,
        /// Delay before each restart.
        backoff: Duration,
    },
}

impl RestartPolicy {
    /// A sensible default for pollers: a few retries with a short backoff.
    pub const fn poller() -> Self {
        Self::OnPanic {
            max_restarts: 5,
            backoff: Duration::from_secs(5),
        }
    }
}

// ── Supervised spawn ──────────────────────────────────────────────────────────

/// Spawn `factory()` under supervision.
///
/// The future is run inside `catch_unwind`, so a panic is caught, logged with
/// the subsystem name attached, and handled per `policy` instead of vanishing
/// with a dropped `JoinHandle`.
///
/// # Why a factory
///
/// A `Future` is consumed by the run that panicked, so restarting requires the
/// ability to build a fresh one. Callers clone whatever the subsystem needs
/// into the closure.
///
/// # Unwind safety
///
/// `AssertUnwindSafe` is required because most subsystem futures capture types
/// that are not `UnwindSafe` (channels, Arc'd config). This is sound HERE
/// because a panicking run is discarded entirely: the restart calls the factory
/// again and builds fresh state rather than reusing anything the panic may have
/// left half-updated. Shared state that outlives a panic is behind Mutexes that
/// recover from poisoning (T-P7-E25-05).
pub fn spawn_supervised<F, Fut>(
    name: &'static str,
    policy: RestartPolicy,
    shutdown: CancellationToken,
    mut factory: F,
) -> JoinHandle<()>
where
    F: FnMut() -> Fut + Send + 'static,
    Fut: Future<Output = ()> + Send + 'static,
{
    tokio::spawn(async move {
        let mut restarts: u32 = 0;

        loop {
            let result = std::panic::AssertUnwindSafe(factory()).catch_unwind().await;

            match result {
                Ok(()) => {
                    info!(subsystem = name, "subsystem exited normally");
                    return;
                }
                Err(payload) => {
                    error!(
                        subsystem = name,
                        restarts,
                        panic.message = %panic_message(payload.as_ref()),
                        "subsystem PANICKED"
                    );

                    // Never restart into a shutdown — the panic is recorded,
                    // but the daemon is on its way out.
                    if shutdown.is_cancelled() {
                        warn!(subsystem = name, "not restarting: shutdown in progress");
                        return;
                    }

                    match policy {
                        RestartPolicy::Never => {
                            warn!(
                                subsystem = name,
                                "not restarting: policy is Never (subsystem stays down)"
                            );
                            return;
                        }
                        RestartPolicy::OnPanic {
                            max_restarts,
                            backoff,
                        } => {
                            if restarts >= max_restarts {
                                error!(
                                    subsystem = name,
                                    max_restarts, "giving up: restart limit reached"
                                );
                                return;
                            }
                            restarts += 1;
                            warn!(
                                subsystem = name,
                                restarts,
                                backoff_secs = backoff.as_secs_f64(),
                                "restarting after panic"
                            );
                            tokio::select! {
                                _ = tokio::time::sleep(backoff) => {}
                                _ = shutdown.cancelled() => {
                                    warn!(subsystem = name, "shutdown during restart backoff");
                                    return;
                                }
                            }
                        }
                    }
                }
            }
        }
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;

    /// Silence the fixture panics so the test output stays readable, and
    /// restore the previous hook afterwards.
    /// The boxed hook type `std::panic::take_hook` hands back.
    type PanicHook = Box<dyn Fn(&std::panic::PanicHookInfo<'_>) + Sync + Send + 'static>;

    struct QuietPanics(Option<PanicHook>);

    impl QuietPanics {
        fn new() -> Self {
            let previous = std::panic::take_hook();
            std::panic::set_hook(Box::new(|_| {}));
            Self(Some(previous))
        }
    }

    impl Drop for QuietPanics {
        fn drop(&mut self) {
            if let Some(hook) = self.0.take() {
                std::panic::set_hook(hook);
            }
        }
    }

    #[tokio::test]
    async fn a_task_that_completes_is_not_restarted() {
        let _quiet = QuietPanics::new();
        let runs = Arc::new(AtomicU32::new(0));
        let r = Arc::clone(&runs);

        spawn_supervised(
            "clean",
            RestartPolicy::poller(),
            CancellationToken::new(),
            move || {
                let r = Arc::clone(&r);
                async move {
                    r.fetch_add(1, Ordering::SeqCst);
                }
            },
        )
        .await
        .expect("supervisor task should not itself panic");

        assert_eq!(
            runs.load(Ordering::SeqCst),
            1,
            "clean exit must not restart"
        );
    }

    #[tokio::test]
    async fn a_panicking_task_is_restarted_up_to_the_limit() {
        let _quiet = QuietPanics::new();
        let runs = Arc::new(AtomicU32::new(0));
        let r = Arc::clone(&runs);

        spawn_supervised(
            "flaky",
            RestartPolicy::OnPanic {
                max_restarts: 3,
                backoff: Duration::from_millis(1),
            },
            CancellationToken::new(),
            move || {
                let r = Arc::clone(&r);
                async move {
                    r.fetch_add(1, Ordering::SeqCst);
                    panic!("simulated subsystem panic");
                }
            },
        )
        .await
        .expect("a panicking subsystem must not take the supervisor down with it");

        // Initial run + 3 restarts.
        assert_eq!(runs.load(Ordering::SeqCst), 4);
    }

    #[tokio::test]
    async fn a_task_that_recovers_stops_restarting() {
        let _quiet = QuietPanics::new();
        let runs = Arc::new(AtomicU32::new(0));
        let r = Arc::clone(&runs);

        spawn_supervised(
            "recovers",
            RestartPolicy::OnPanic {
                max_restarts: 10,
                backoff: Duration::from_millis(1),
            },
            CancellationToken::new(),
            move || {
                let r = Arc::clone(&r);
                async move {
                    // Panic once, then succeed.
                    if r.fetch_add(1, Ordering::SeqCst) == 0 {
                        panic!("first run fails");
                    }
                }
            },
        )
        .await
        .expect("supervisor completes");

        assert_eq!(
            runs.load(Ordering::SeqCst),
            2,
            "one panic, one clean run, then stop"
        );
    }

    #[tokio::test]
    async fn restart_policy_never_leaves_the_subsystem_down() {
        let _quiet = QuietPanics::new();
        let runs = Arc::new(AtomicU32::new(0));
        let r = Arc::clone(&runs);

        spawn_supervised(
            "listener",
            RestartPolicy::Never,
            CancellationToken::new(),
            move || {
                let r = Arc::clone(&r);
                async move {
                    r.fetch_add(1, Ordering::SeqCst);
                    panic!("bound-listener subsystem panic");
                }
            },
        )
        .await
        .expect("supervisor completes");

        assert_eq!(
            runs.load(Ordering::SeqCst),
            1,
            "Never must not re-run a task that owns exclusive external state"
        );
    }

    #[tokio::test]
    async fn shutdown_stops_restarts() {
        let _quiet = QuietPanics::new();
        let runs = Arc::new(AtomicU32::new(0));
        let r = Arc::clone(&runs);
        let token = CancellationToken::new();
        token.cancel(); // already shutting down

        spawn_supervised(
            "during-shutdown",
            RestartPolicy::poller(),
            token,
            move || {
                let r = Arc::clone(&r);
                async move {
                    r.fetch_add(1, Ordering::SeqCst);
                    panic!("panic while shutting down");
                }
            },
        )
        .await
        .expect("supervisor completes");

        assert_eq!(
            runs.load(Ordering::SeqCst),
            1,
            "a panic during shutdown is logged, not restarted"
        );
    }

    #[test]
    fn panic_messages_are_extracted_from_both_payload_shapes() {
        let literal: Box<dyn std::any::Any + Send> = Box::new("static str panic");
        assert_eq!(panic_message(literal.as_ref()), "static str panic");

        let formatted: Box<dyn std::any::Any + Send> = Box::new(String::from("formatted panic"));
        assert_eq!(panic_message(formatted.as_ref()), "formatted panic");

        let opaque: Box<dyn std::any::Any + Send> = Box::new(42u32);
        assert_eq!(panic_message(opaque.as_ref()), "<non-string panic payload>");
    }

    #[test]
    fn installing_the_panic_hook_is_idempotent() {
        // Called twice must not panic or recurse; the Once guard means only
        // the first install takes effect.
        install_panic_hook();
        install_panic_hook();
    }
}

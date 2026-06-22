//! # task_class — task classification for the routing matrix
//!
//! ## Purpose
//!
//! Defines the [`TaskClass`] enum whose variants map 1:1 to rows in the
//! v1.2 routing matrix (CASCADE-COMPLETE-VISION.md §5). Each variant carries
//! the semantics that drive `Router::select` to the correct ordered preference
//! list of delegate lanes.
//!
//! ## Inputs
//!
//! Caller supplies a `TaskClass` to `Router::select()`.
//!
//! ## Outputs
//!
//! Used by the router to index the routing matrix.
//!
//! ## Constraints
//!
//! - No I/O, no async, no external dependencies.
//! - No `unwrap()` outside `#[cfg(test)]`.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing::task_class

use std::fmt;

/// Classification of a task for routing-matrix lookup.
///
/// Variants correspond to rows in the v1.2 matrix.  The router uses this to
/// select the correct ordered preference list of [`super::DelegateLane`] values.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum TaskClass {
    /// T0 interactive chat or final synthesis.
    ///
    /// Reserved for the main Claude account (T0). Never delegated to secondary
    /// lanes. If the main Claude is unavailable the request is rejected rather
    /// than downgraded.
    InteractiveChat,

    /// Bulk execution, code drafting, long-form generation.
    ///
    /// Preference: acc2+ Claude (PTY, drain first) → Codex → OC-Go.
    /// GFP is NOT used here — free Flash is reserved for cheap/trivial work.
    BulkExec,

    /// Cheap, trivial, pre/post-prompt, taxonomy, or classification tasks.
    ///
    /// Preference: GFP free Flash (max it) → local LLM.
    Cheap,

    /// Adversarial review — CR, QA, or research tasks that must be done by a
    /// **different model family** than the author to avoid blind spots.
    ///
    /// Preference: cross-family (GFP + agy + OC-Go + Codex, maximising free/cheap).
    AdversarialReview,

    /// Hard / final correctness gate requiring the highest-quality model.
    ///
    /// Preference: main Claude Opus (T0 reserved for this role alongside
    /// InteractiveChat).
    FinalGate,

    /// Content tagged Sensitive (PII / VA / custody / health / personal).
    ///
    /// **Firewall**: routes to Claude or local-only. External providers are
    /// blocked regardless of quota. The router delegates the policy check to
    /// `SensitivityPolicy`.
    Sensitive,
}

impl fmt::Display for TaskClass {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let s = match self {
            TaskClass::InteractiveChat => "InteractiveChat",
            TaskClass::BulkExec => "BulkExec",
            TaskClass::Cheap => "Cheap",
            TaskClass::AdversarialReview => "AdversarialReview",
            TaskClass::FinalGate => "FinalGate",
            TaskClass::Sensitive => "Sensitive",
        };
        write!(f, "{s}")
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn display_all_variants() {
        let cases = [
            (TaskClass::InteractiveChat, "InteractiveChat"),
            (TaskClass::BulkExec, "BulkExec"),
            (TaskClass::Cheap, "Cheap"),
            (TaskClass::AdversarialReview, "AdversarialReview"),
            (TaskClass::FinalGate, "FinalGate"),
            (TaskClass::Sensitive, "Sensitive"),
        ];
        for (class, expected) in &cases {
            assert_eq!(class.to_string(), *expected);
        }
    }

    #[test]
    fn task_class_debug_clone_eq() {
        let a = TaskClass::BulkExec;
        let b = a;
        assert_eq!(a, b);
        let _ = format!("{a:?}");
    }

    #[test]
    fn task_class_hash_usable_in_hashmap() {
        use std::collections::HashMap;
        let mut m: HashMap<TaskClass, &str> = HashMap::new();
        m.insert(TaskClass::Cheap, "cheap");
        assert_eq!(m[&TaskClass::Cheap], "cheap");
    }
}

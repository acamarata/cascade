//! Background task classification and capability gate.
//!
//! Purpose: classify background operations by safety class and gate writes/sends
//!   behind an explicit `CapabilityGrant`. Read-only operations (Analyze,
//!   Classify, Summarize) are permissionless; any operation that modifies user
//!   data or sends outbound messages requires a prior `CapabilityGrant`.
//!
//! Inputs:  `BackgroundTaskClass` — attached to every background task.
//! Outputs: `CapabilityGate::check` returns Ok(()) or Err(CapabilityDenied).
//! Constraints:
//!   - SAFE DEFAULT: anything not explicitly listed as read-only requires a grant.
//!   - No network calls here — purely in-memory type checks.
//!
//! SPORT: cascade-types / background_task — auto-01

use serde::{Deserialize, Serialize};

/// The safety class of a background operation.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BackgroundTaskClass {
    /// Read-only corpus analysis. Permitted without a CapabilityGrant.
    Analyze,
    /// Read-only classification / tagging. Permitted without a CapabilityGrant.
    Classify,
    /// Read-only summarization. Permitted without a CapabilityGrant.
    Summarize,
    /// Writes to user-visible files. Requires `CapabilityGrant::WriteUserData`.
    WriteUserData,
    /// Sends an outbound message. Requires `CapabilityGrant::SendOutbound`.
    SendOutbound,
    /// Cross-corpus read. Requires `CapabilityGrant::CrossCorpusRead`.
    CrossCorpusRead,
}

impl BackgroundTaskClass {
    /// Return true if this class is safe to run without any capability grant.
    pub fn is_permissionless(&self) -> bool {
        matches!(
            self,
            BackgroundTaskClass::Analyze
                | BackgroundTaskClass::Classify
                | BackgroundTaskClass::Summarize
        )
    }
}

/// A grant that allows a specific privileged background operation.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CapabilityGrant {
    WriteUserData,
    SendOutbound,
    CrossCorpusRead,
}

impl CapabilityGrant {
    /// Return the grant required for a given task class, if any.
    pub fn required_for(class: &BackgroundTaskClass) -> Option<Self> {
        match class {
            BackgroundTaskClass::Analyze
            | BackgroundTaskClass::Classify
            | BackgroundTaskClass::Summarize => None,
            BackgroundTaskClass::WriteUserData => Some(Self::WriteUserData),
            BackgroundTaskClass::SendOutbound => Some(Self::SendOutbound),
            BackgroundTaskClass::CrossCorpusRead => Some(Self::CrossCorpusRead),
        }
    }
}

/// Error returned when a background task lacks a required capability grant.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("capability denied: {class:?} requires grant {required:?}")]
pub struct CapabilityDenied {
    pub class: BackgroundTaskClass,
    pub required: CapabilityGrant,
}

/// In-process capability gate.
#[derive(Debug, Default)]
pub struct CapabilityGate {
    grants: std::collections::HashSet<CapabilityGrant>,
}

impl CapabilityGate {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn grant(&mut self, g: CapabilityGrant) {
        self.grants.insert(g);
    }

    pub fn revoke(&mut self, g: &CapabilityGrant) {
        self.grants.remove(g);
    }

    pub fn check(&self, class: &BackgroundTaskClass) -> Result<(), CapabilityDenied> {
        match CapabilityGrant::required_for(class) {
            None => Ok(()),
            Some(ref required) => {
                if self.grants.contains(required) {
                    Ok(())
                } else {
                    Err(CapabilityDenied {
                        class: class.clone(),
                        required: required.clone(),
                    })
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_analyze_is_permissionless() {
        let gate = CapabilityGate::new();
        assert!(gate.check(&BackgroundTaskClass::Analyze).is_ok());
        assert!(gate.check(&BackgroundTaskClass::Classify).is_ok());
        assert!(gate.check(&BackgroundTaskClass::Summarize).is_ok());
    }

    #[test]
    fn test_write_user_data_denied_without_grant() {
        let gate = CapabilityGate::new();
        let result = gate.check(&BackgroundTaskClass::WriteUserData);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert_eq!(err.required, CapabilityGrant::WriteUserData);
    }

    #[test]
    fn test_write_user_data_allowed_with_grant() {
        let mut gate = CapabilityGate::new();
        gate.grant(CapabilityGrant::WriteUserData);
        assert!(gate.check(&BackgroundTaskClass::WriteUserData).is_ok());
    }

    #[test]
    fn test_cross_corpus_read_denied_without_grant() {
        let gate = CapabilityGate::new();
        let result = gate.check(&BackgroundTaskClass::CrossCorpusRead);
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert_eq!(err.required, CapabilityGrant::CrossCorpusRead);
    }

    #[test]
    fn test_revoke_removes_grant() {
        let mut gate = CapabilityGate::new();
        gate.grant(CapabilityGrant::SendOutbound);
        assert!(gate.check(&BackgroundTaskClass::SendOutbound).is_ok());
        gate.revoke(&CapabilityGrant::SendOutbound);
        assert!(gate.check(&BackgroundTaskClass::SendOutbound).is_err());
    }

    #[test]
    fn test_analyze_class_is_permissionless_flag() {
        assert!(BackgroundTaskClass::Analyze.is_permissionless());
        assert!(BackgroundTaskClass::Classify.is_permissionless());
        assert!(BackgroundTaskClass::Summarize.is_permissionless());
        assert!(!BackgroundTaskClass::WriteUserData.is_permissionless());
        assert!(!BackgroundTaskClass::SendOutbound.is_permissionless());
        assert!(!BackgroundTaskClass::CrossCorpusRead.is_permissionless());
    }
}

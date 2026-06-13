//! Agent-to-agent inbox — per-agent message queue + handoff + approval channel.
//!
//! Purpose: provide a thread-safe, role-routable message bus so agents can
//!   send requests, responses, handoffs, notifications, and approval
//!   request/decisions to one another without coupling to the executor or
//!   the project-level cascade inbox.
//! Inputs: `AgentMessage` values produced by agents or the executor.
//! Outputs: messages delivered in send-order per recipient; handoffs transfer
//!   `AgentTask` ownership; approval decisions unblock parked executor steps.
//! Constraints:
//!   - Distinct from the filesystem-based project inbox (`cascade.inbox.*`).
//!   - Thread-safe via `Arc<RwLock<_>>` for the queue store.
//!   - Backpressure cap enforced per-recipient queue.
//!   - `InboxStore` is injectable for durable persistence (in-memory default).
//!   - Serde uses `camelCase` rename; all enum variants are struct-style.
//! SPORT: cascade-agents / inbox — E-P6-05

use std::collections::HashMap;
use std::sync::{Arc, RwLock};

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use thiserror::Error;
use uuid::Uuid;

use crate::spec::AgentRole;
use crate::task::AgentTask;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Default maximum number of messages in a single agent's queue.
pub const DEFAULT_INBOX_CAP: usize = 512;

// ── MessageKind ───────────────────────────────────────────────────────────────

/// The semantic kind of an `AgentMessage`.
///
/// All variants are struct-style (repo rule: no tuple variants in serde enums).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum MessageKind {
    /// Agent A requests work or information from agent B.
    Request {},
    /// Agent B replies to a prior `Request` from agent A.
    Response {},
    /// Agent A transfers ownership of a task to agent B.
    Handoff {},
    /// One-way notification; no reply expected.
    Notify {},
    /// Executor emits this when an `Outbound` tool call needs human approval.
    ApprovalRequest {},
    /// Founder/CEO responds to a prior `ApprovalRequest`.
    ApprovalDecision {
        /// `true` → approved; `false` → denied.
        approved: bool,
        /// Optional human-readable note attached to the decision.
        note: Option<String>,
    },
}

// ── MessageTarget ─────────────────────────────────────────────────────────────

/// Delivery target for an `AgentMessage`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "target")]
pub enum MessageTarget {
    /// Deliver to exactly one agent by id.
    Agent { agent_id: String },
    /// Deliver to every registered agent with this role.
    Role { role: AgentRole },
    /// Deliver to every agent in the system (use sparingly).
    Broadcast {},
}

// ── AgentMessage ──────────────────────────────────────────────────────────────

/// A message that travels between agents via the `AgentInbox`.
///
/// # serde contract
/// All fields serialize as `camelCase`. The `kind` field carries the enum tag.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentMessage {
    /// Unique message identifier (UUIDv4).
    pub id: String,

    /// The sending agent's id.
    pub from_agent: String,

    /// Delivery target: a specific agent, a role, or broadcast.
    pub to: MessageTarget,

    /// The semantic kind of this message.
    pub kind: MessageKind,

    /// Optional reference to the `AgentTask` this message concerns.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task_ref: Option<String>,

    /// Arbitrary structured body (JSON value).
    pub body: serde_json::Value,

    /// Wall-clock send timestamp.
    pub ts: DateTime<Utc>,
}

impl AgentMessage {
    /// Construct a new `AgentMessage` with a generated id and current timestamp.
    pub fn new(
        from_agent: impl Into<String>,
        to: MessageTarget,
        kind: MessageKind,
        task_ref: Option<String>,
        body: serde_json::Value,
    ) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            from_agent: from_agent.into(),
            to,
            kind,
            task_ref,
            body,
            ts: Utc::now(),
        }
    }
}

// ── InboxError ────────────────────────────────────────────────────────────────

/// Errors produced by `AgentInbox` operations.
#[derive(Debug, Error)]
pub enum InboxError {
    #[error("inbox for agent '{agent_id}' is at capacity ({cap})")]
    AtCapacity { agent_id: String, cap: usize },

    #[error("agent '{agent_id}' has no inbox (not registered)")]
    UnknownAgent { agent_id: String },

    #[error("no pending approval request for task '{task_id}'")]
    NoApprovalRequest { task_id: String },

    #[error("store error: {0}")]
    Store(String),
}

// ── InboxStore trait ──────────────────────────────────────────────────────────

/// Injectable persistence seam for `AgentInbox`.
///
/// Implement this trait to persist messages to a database or log.
/// The in-memory default (`MemoryInboxStore`) is used when no store is provided.
pub trait InboxStore: Send + Sync {
    /// Persist a message (fire-and-forget; errors are non-fatal).
    fn persist(&self, msg: &AgentMessage) -> Result<(), InboxError>;
    /// Mark a message delivered to `agent_id`.
    fn mark_delivered(&self, msg_id: &str, agent_id: &str) -> Result<(), InboxError>;
}

/// No-op in-memory store — used as the default.
#[derive(Default)]
pub struct MemoryInboxStore;

impl InboxStore for MemoryInboxStore {
    fn persist(&self, _msg: &AgentMessage) -> Result<(), InboxError> {
        Ok(())
    }
    fn mark_delivered(&self, _msg_id: &str, _agent_id: &str) -> Result<(), InboxError> {
        Ok(())
    }
}

// ── RoleResolver trait ────────────────────────────────────────────────────────

/// Injectable: resolve an `AgentRole` to the set of agent ids that carry it.
///
/// Tests inject a `StaticRoleResolver`; production wires to `AgentRegistry`.
pub trait RoleResolver: Send + Sync {
    /// Return all agent ids that have the given role.
    fn agents_for_role(&self, role: AgentRole) -> Vec<String>;
    /// Return all registered agent ids.
    fn all_agent_ids(&self) -> Vec<String>;
}

/// Static role resolver — maps roles to a pre-configured list of agent ids.
///
/// Used in tests and bootstrap scenarios.
pub struct StaticRoleResolver {
    map: HashMap<String, Vec<String>>,
    all: Vec<String>,
}

impl StaticRoleResolver {
    /// Build from an iterator of `(agent_id, role)` pairs.
    pub fn from_pairs(pairs: impl IntoIterator<Item = (String, AgentRole)>) -> Self {
        let mut map: HashMap<String, Vec<String>> = HashMap::new();
        let mut all: Vec<String> = Vec::new();
        for (id, role) in pairs {
            let key = format!("{role:?}");
            map.entry(key).or_default().push(id.clone());
            all.push(id);
        }
        Self { map, all }
    }
}

impl RoleResolver for StaticRoleResolver {
    fn agents_for_role(&self, role: AgentRole) -> Vec<String> {
        let key = format!("{role:?}");
        self.map.get(&key).cloned().unwrap_or_default()
    }

    fn all_agent_ids(&self) -> Vec<String> {
        self.all.clone()
    }
}

// ── AgentInbox ────────────────────────────────────────────────────────────────

/// Per-agent queue inner state.
struct AgentQueue {
    messages: Vec<AgentMessage>,
    cap: usize,
}

impl AgentQueue {
    fn new(cap: usize) -> Self {
        Self {
            messages: Vec::new(),
            cap,
        }
    }

    fn push(&mut self, msg: AgentMessage, agent_id: &str) -> Result<(), InboxError> {
        if self.messages.len() >= self.cap {
            return Err(InboxError::AtCapacity {
                agent_id: agent_id.to_owned(),
                cap: self.cap,
            });
        }
        self.messages.push(msg);
        Ok(())
    }
}

/// Inner mutable state for `AgentInbox`.
struct Inner {
    queues: HashMap<String, AgentQueue>,
    /// Pending approval requests keyed by task_id.
    pending_approvals: HashMap<String, AgentMessage>,
    /// Resolved approval decisions keyed by task_id.
    approval_decisions: HashMap<String, MessageKind>,
}

impl Inner {
    fn new() -> Self {
        Self {
            queues: HashMap::new(),
            pending_approvals: HashMap::new(),
            approval_decisions: HashMap::new(),
        }
    }
}

/// Thread-safe, per-agent message inbox with role-based routing.
///
/// # Overview
/// - Each agent registers a named queue via `register_agent`.
/// - `send` routes a message to one agent, all agents with a role, or all agents.
/// - `receive` drains the oldest message for a given agent.
/// - `peek` returns without removing.
/// - `drain` removes and returns all messages for a given agent.
/// - Handoff protocol transfers `AgentTask` ownership and delivers a `Handoff` message.
/// - Approval channel: `send_approval_request` parks the request; `resolve_approval`
///   posts the decision; `poll_approval_decision` checks whether one arrived.
///
/// # Thread safety
/// All mutation goes through `Arc<RwLock<Inner>>`.
#[derive(Clone)]
pub struct AgentInbox {
    inner: Arc<RwLock<Inner>>,
    store: Arc<dyn InboxStore>,
    cap: usize,
}

impl AgentInbox {
    /// Create an inbox with the default capacity and an in-memory store.
    pub fn new() -> Self {
        Self::with_store(Arc::new(MemoryInboxStore), DEFAULT_INBOX_CAP)
    }

    /// Create an inbox with a custom store and per-agent queue cap.
    pub fn with_store(store: Arc<dyn InboxStore>, cap: usize) -> Self {
        Self {
            inner: Arc::new(RwLock::new(Inner::new())),
            store,
            cap,
        }
    }

    /// Register a queue for `agent_id`.
    ///
    /// Idempotent — registering the same id twice is a no-op.
    pub fn register_agent(&self, agent_id: impl Into<String>) {
        let id = agent_id.into();
        let mut inner = self.inner.write().expect("inbox poisoned");
        inner.queues.entry(id).or_insert_with(|| AgentQueue::new(self.cap));
    }

    /// Unregister and discard the queue for `agent_id`.
    pub fn unregister_agent(&self, agent_id: &str) {
        let mut inner = self.inner.write().expect("inbox poisoned");
        inner.queues.remove(agent_id);
    }

    /// Send `msg` using the `RoleResolver` to expand role/broadcast targets.
    ///
    /// # Delivery semantics
    /// - `MessageTarget::Agent` — delivered to exactly one queue.
    /// - `MessageTarget::Role`  — delivered to every agent with that role.
    /// - `MessageTarget::Broadcast` — delivered to every registered agent.
    ///
    /// The message is cloned for each recipient. Each clone gets a fresh id so
    /// recipients can independently acknowledge or drain their copy.
    pub fn send(
        &self,
        msg: AgentMessage,
        resolver: &dyn RoleResolver,
    ) -> Result<(), InboxError> {
        let recipients = self.resolve_recipients(&msg.to, resolver);
        for agent_id in &recipients {
            let mut clone = msg.clone();
            clone.id = Uuid::new_v4().to_string();
            self.store.persist(&clone)?;
            let mut inner = self.inner.write().expect("inbox poisoned");
            let queue = inner
                .queues
                .get_mut(agent_id)
                .ok_or_else(|| InboxError::UnknownAgent { agent_id: agent_id.clone() })?;
            queue.push(clone.clone(), agent_id)?;
            drop(inner);
            self.store.mark_delivered(&clone.id, agent_id)?;
        }
        Ok(())
    }

    /// Peek at the oldest message in `agent_id`'s queue without removing it.
    pub fn peek(&self, agent_id: &str) -> Result<Option<AgentMessage>, InboxError> {
        let inner = self.inner.read().expect("inbox poisoned");
        match inner.queues.get(agent_id) {
            None => Err(InboxError::UnknownAgent { agent_id: agent_id.to_owned() }),
            Some(q) => Ok(q.messages.first().cloned()),
        }
    }

    /// Remove and return the oldest message from `agent_id`'s queue.
    pub fn receive(&self, agent_id: &str) -> Result<Option<AgentMessage>, InboxError> {
        let mut inner = self.inner.write().expect("inbox poisoned");
        match inner.queues.get_mut(agent_id) {
            None => Err(InboxError::UnknownAgent { agent_id: agent_id.to_owned() }),
            Some(q) => {
                if q.messages.is_empty() {
                    Ok(None)
                } else {
                    Ok(Some(q.messages.remove(0)))
                }
            }
        }
    }

    /// Drain all messages from `agent_id`'s queue, returning them in order.
    pub fn drain(&self, agent_id: &str) -> Result<Vec<AgentMessage>, InboxError> {
        let mut inner = self.inner.write().expect("inbox poisoned");
        match inner.queues.get_mut(agent_id) {
            None => Err(InboxError::UnknownAgent { agent_id: agent_id.to_owned() }),
            Some(q) => Ok(std::mem::take(&mut q.messages)),
        }
    }

    /// Returns how many messages are queued for `agent_id`.
    pub fn queue_len(&self, agent_id: &str) -> Result<usize, InboxError> {
        let inner = self.inner.read().expect("inbox poisoned");
        match inner.queues.get(agent_id) {
            None => Err(InboxError::UnknownAgent { agent_id: agent_id.to_owned() }),
            Some(q) => Ok(q.messages.len()),
        }
    }

    // ── Handoff protocol ──────────────────────────────────────────────────────

    /// Transfer ownership of `task` from `from_agent` to `to_agent`.
    ///
    /// Mutates `task.assigned_agent_id` in place and delivers a `Handoff`
    /// message to `to_agent`'s inbox.  The caller holds the `AgentTask`
    /// and should persist the updated value.
    ///
    /// # Idempotency
    /// If `task.assigned_agent_id` already equals `to_agent`, this is a no-op
    /// (returns `Ok(())` without delivering a duplicate message).
    pub fn handoff(
        &self,
        task: &mut AgentTask,
        from_agent: &str,
        to_agent: &str,
        resolver: &dyn RoleResolver,
        body: serde_json::Value,
    ) -> Result<(), InboxError> {
        if task.assigned_agent_id.as_deref() == Some(to_agent) {
            return Ok(());
        }
        task.assigned_agent_id = Some(to_agent.to_owned());
        let msg = AgentMessage::new(
            from_agent,
            MessageTarget::Agent { agent_id: to_agent.to_owned() },
            MessageKind::Handoff {},
            Some(task.id.clone()),
            body,
        );
        self.send(msg, resolver)
    }

    // ── Approval channel ──────────────────────────────────────────────────────

    /// Park an approval request for `task_id`.
    ///
    /// The `msg` must carry `MessageKind::ApprovalRequest`.
    /// The executor calls this when an `Outbound` tool call is detected.
    ///
    /// # Errors
    /// Returns `InboxError::Store` if persistence fails.
    pub fn send_approval_request(&self, msg: AgentMessage) -> Result<(), InboxError> {
        let task_id = msg
            .task_ref
            .clone()
            .ok_or_else(|| InboxError::Store("approval request missing task_ref".into()))?;
        self.store.persist(&msg)?;
        let mut inner = self.inner.write().expect("inbox poisoned");
        inner.pending_approvals.insert(task_id, msg);
        Ok(())
    }

    /// Post an `ApprovalDecision` for `task_id`.
    ///
    /// The executor or REST handler calls this when the founder/CEO responds.
    ///
    /// # Errors
    /// Returns `InboxError::NoApprovalRequest` if no request exists for `task_id`.
    pub fn resolve_approval(
        &self,
        task_id: &str,
        approved: bool,
        note: Option<String>,
    ) -> Result<(), InboxError> {
        let mut inner = self.inner.write().expect("inbox poisoned");
        if inner.pending_approvals.remove(task_id).is_none() {
            return Err(InboxError::NoApprovalRequest { task_id: task_id.to_owned() });
        }
        inner
            .approval_decisions
            .insert(task_id.to_owned(), MessageKind::ApprovalDecision { approved, note });
        Ok(())
    }

    /// Poll for an `ApprovalDecision` for `task_id`.
    ///
    /// Returns `Some(kind)` and removes the decision if one exists; `None` if
    /// the request is still pending.
    pub fn poll_approval_decision(&self, task_id: &str) -> Option<MessageKind> {
        let mut inner = self.inner.write().expect("inbox poisoned");
        inner.approval_decisions.remove(task_id)
    }

    /// Returns `true` if there is a pending (unresolved) approval request for `task_id`.
    pub fn has_pending_approval(&self, task_id: &str) -> bool {
        let inner = self.inner.read().expect("inbox poisoned");
        inner.pending_approvals.contains_key(task_id)
    }

    // ── Internal helpers ──────────────────────────────────────────────────────

    fn resolve_recipients(
        &self,
        target: &MessageTarget,
        resolver: &dyn RoleResolver,
    ) -> Vec<String> {
        match target {
            MessageTarget::Agent { agent_id } => vec![agent_id.clone()],
            MessageTarget::Role { role } => resolver.agents_for_role(*role),
            MessageTarget::Broadcast {} => resolver.all_agent_ids(),
        }
    }
}

impl Default for AgentInbox {
    fn default() -> Self {
        Self::new()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::spec::AgentRole;
    use crate::task::AgentTask;

    fn make_resolver() -> StaticRoleResolver {
        StaticRoleResolver::from_pairs(vec![
            ("agent.ceo".to_owned(), AgentRole::Ceo),
            ("agent.coder1".to_owned(), AgentRole::Coder),
            ("agent.coder2".to_owned(), AgentRole::Coder),
        ])
    }

    fn make_inbox_with_agents() -> AgentInbox {
        let inbox = AgentInbox::new();
        inbox.register_agent("agent.ceo");
        inbox.register_agent("agent.coder1");
        inbox.register_agent("agent.coder2");
        inbox
    }

    fn simple_msg(from: &str, to_agent: &str) -> AgentMessage {
        AgentMessage::new(
            from,
            MessageTarget::Agent { agent_id: to_agent.to_owned() },
            MessageKind::Request {},
            None,
            serde_json::json!({"note": "hello"}),
        )
    }

    // ── send / receive in order ───────────────────────────────────────────────

    #[test]
    fn send_receive_ordered() {
        let inbox = make_inbox_with_agents();
        let resolver = make_resolver();

        let m1 = simple_msg("agent.ceo", "agent.coder1");
        let m2 = simple_msg("agent.ceo", "agent.coder1");
        let body1 = m1.body.clone();
        let body2 = m2.body.clone();

        inbox.send(m1, &resolver).unwrap();
        inbox.send(m2, &resolver).unwrap();

        let r1 = inbox.receive("agent.coder1").unwrap().unwrap();
        let r2 = inbox.receive("agent.coder1").unwrap().unwrap();
        assert_eq!(r1.body, body1);
        assert_eq!(r2.body, body2);
        assert!(inbox.receive("agent.coder1").unwrap().is_none());
    }

    // ── broadcast by role ─────────────────────────────────────────────────────

    #[test]
    fn broadcast_by_role_delivers_to_all_matching_agents() {
        let inbox = make_inbox_with_agents();
        let resolver = make_resolver();

        let msg = AgentMessage::new(
            "agent.ceo",
            MessageTarget::Role { role: AgentRole::Coder },
            MessageKind::Notify {},
            None,
            serde_json::json!({"action": "start"}),
        );

        inbox.send(msg, &resolver).unwrap();

        // Both coders receive the message
        assert_eq!(inbox.queue_len("agent.coder1").unwrap(), 1);
        assert_eq!(inbox.queue_len("agent.coder2").unwrap(), 1);
        // CEO does not
        assert_eq!(inbox.queue_len("agent.ceo").unwrap(), 0);
    }

    // ── handoff transfers ownership + delivers Handoff msg ────────────────────

    #[test]
    fn handoff_transfers_ownership_and_delivers_message() {
        let inbox = make_inbox_with_agents();
        let resolver = make_resolver();

        let mut task = AgentTask::new_root("write tests", AgentRole::Coder);
        task.assigned_agent_id = Some("agent.ceo".to_owned());

        inbox
            .handoff(
                &mut task,
                "agent.ceo",
                "agent.coder1",
                &resolver,
                serde_json::json!({"reason": "delegation"}),
            )
            .unwrap();

        // Ownership transferred
        assert_eq!(task.assigned_agent_id.as_deref(), Some("agent.coder1"));

        // coder1's inbox has the Handoff message
        let msg = inbox.receive("agent.coder1").unwrap().unwrap();
        assert_eq!(msg.kind, MessageKind::Handoff {});
        assert_eq!(msg.task_ref.as_deref(), Some(task.id.as_str()));
    }

    #[test]
    fn handoff_is_idempotent() {
        let inbox = make_inbox_with_agents();
        let resolver = make_resolver();

        let mut task = AgentTask::new_root("idempotent handoff", AgentRole::Coder);
        task.assigned_agent_id = Some("agent.coder1".to_owned());

        // Handoff to the already-assigned agent is a no-op
        inbox
            .handoff(
                &mut task,
                "agent.ceo",
                "agent.coder1",
                &resolver,
                serde_json::json!({}),
            )
            .unwrap();

        // No message delivered
        assert_eq!(inbox.queue_len("agent.coder1").unwrap(), 0);
    }

    // ── approval request / decision round-trip ────────────────────────────────

    #[test]
    fn approval_request_decision_round_trip() {
        let inbox = AgentInbox::new();

        let task_id = Uuid::new_v4().to_string();
        let req = AgentMessage::new(
            "executor",
            MessageTarget::Broadcast {},
            MessageKind::ApprovalRequest {},
            Some(task_id.clone()),
            serde_json::json!({"tool": "email.send"}),
        );

        inbox.send_approval_request(req).unwrap();
        assert!(inbox.has_pending_approval(&task_id));
        assert!(inbox.poll_approval_decision(&task_id).is_none());

        inbox.resolve_approval(&task_id, true, Some("looks good".into())).unwrap();
        assert!(!inbox.has_pending_approval(&task_id));

        let decision = inbox.poll_approval_decision(&task_id).unwrap();
        match decision {
            MessageKind::ApprovalDecision { approved, note } => {
                assert!(approved);
                assert_eq!(note.as_deref(), Some("looks good"));
            }
            _ => panic!("expected ApprovalDecision"),
        }

        // Second poll returns None (consumed)
        assert!(inbox.poll_approval_decision(&task_id).is_none());
    }

    #[test]
    fn resolve_approval_without_prior_request_errors() {
        let inbox = AgentInbox::new();
        let err = inbox.resolve_approval("nonexistent-task", true, None).unwrap_err();
        assert!(matches!(err, InboxError::NoApprovalRequest { .. }));
    }

    // ── cap / backpressure ────────────────────────────────────────────────────

    #[test]
    fn inbox_cap_backpressure() {
        let inbox = AgentInbox::with_store(Arc::new(MemoryInboxStore), 2);
        inbox.register_agent("agent.ceo");
        inbox.register_agent("agent.coder1");
        let resolver = make_resolver();

        inbox.send(simple_msg("agent.ceo", "agent.coder1"), &resolver).unwrap();
        inbox.send(simple_msg("agent.ceo", "agent.coder1"), &resolver).unwrap();

        let err = inbox
            .send(simple_msg("agent.ceo", "agent.coder1"), &resolver)
            .unwrap_err();
        assert!(matches!(err, InboxError::AtCapacity { cap: 2, .. }));
    }

    // ── drain ─────────────────────────────────────────────────────────────────

    #[test]
    fn drain_returns_all_messages_in_order() {
        let inbox = make_inbox_with_agents();
        let resolver = make_resolver();

        for i in 0..3_u32 {
            let msg = AgentMessage::new(
                "agent.ceo",
                MessageTarget::Agent { agent_id: "agent.coder1".to_owned() },
                MessageKind::Notify {},
                None,
                serde_json::json!({"seq": i}),
            );
            inbox.send(msg, &resolver).unwrap();
        }

        let drained = inbox.drain("agent.coder1").unwrap();
        assert_eq!(drained.len(), 3);
        for (i, msg) in drained.iter().enumerate() {
            assert_eq!(msg.body["seq"], i as u64);
        }
        assert_eq!(inbox.queue_len("agent.coder1").unwrap(), 0);
    }

    // ── peek does not consume ──────────────────────────────────────────────────

    #[test]
    fn peek_does_not_consume_message() {
        let inbox = make_inbox_with_agents();
        let resolver = make_resolver();

        inbox.send(simple_msg("agent.ceo", "agent.coder1"), &resolver).unwrap();

        let peeked = inbox.peek("agent.coder1").unwrap().unwrap();
        let received = inbox.receive("agent.coder1").unwrap().unwrap();
        assert_eq!(peeked.id, received.id);
        // Now empty
        assert!(inbox.receive("agent.coder1").unwrap().is_none());
    }

    // ── serde camelCase round-trip ─────────────────────────────────────────────

    #[test]
    fn agent_message_serde_camel_case() {
        let msg = AgentMessage::new(
            "agent.ceo",
            MessageTarget::Agent { agent_id: "agent.coder1".to_owned() },
            MessageKind::Request {},
            Some("task-123".to_owned()),
            serde_json::json!({"x": 1}),
        );
        let json = serde_json::to_string(&msg).unwrap();
        assert!(json.contains("\"fromAgent\""), "expected camelCase fromAgent in: {json}");
        assert!(json.contains("\"taskRef\""), "expected camelCase taskRef in: {json}");
        let decoded: AgentMessage = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.from_agent, "agent.ceo");
        assert_eq!(decoded.task_ref.as_deref(), Some("task-123"));
    }

    // ── unknown agent returns error ────────────────────────────────────────────

    #[test]
    fn receive_unknown_agent_returns_error() {
        let inbox = AgentInbox::new();
        let err = inbox.receive("no-such-agent").unwrap_err();
        assert!(matches!(err, InboxError::UnknownAgent { .. }));
    }
}

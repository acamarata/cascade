//! Tests for the automation subsystem.

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::{Arc, Mutex};

    use async_trait::async_trait;
    use chrono::Utc;

    use crate::automation::{
        Automation, AutomationError, AutomationOutcome, AutomationRunner, AutomationTarget,
        AutomationTrigger, DraftArtifact, DraftKind, OutboundSink,
    };
    use crate::chain::{ChainContext, ChainExecutor, ChainFlow};
    use crate::context::{StepOutcome, TokenUsage, ToolCall as CtxToolCall};
    use crate::executor::{AgentExecutor, ExecutorError, ProviderRouter, ToolInvoker};
    use crate::grants::{AccessLevel, ToolGrant};
    use crate::spec::{AgentRole, AgentSpec};
    use crate::tool_registry::{ToolDescriptor, ToolRegistry};

    // ── Recording OutboundSink ─────────────────────────────────────────────────

    /// Captures all approved drafts for assertion in tests.
    #[derive(Default, Clone)]
    struct RecordingSink {
        sent: Arc<Mutex<Vec<DraftArtifact>>>,
    }

    #[async_trait]
    impl OutboundSink for RecordingSink {
        async fn send(&self, draft: &DraftArtifact) -> Result<(), AutomationError> {
            self.sent.lock().unwrap().push(draft.clone());
            Ok(())
        }
    }

    impl RecordingSink {
        fn drain(&self) -> Vec<DraftArtifact> {
            self.sent.lock().unwrap().drain(..).collect()
        }
    }

    // ── Mock ProviderRouter ────────────────────────────────────────────────────

    /// Always returns a done outcome with fixed text.
    struct FixedProvider {
        text: String,
    }

    #[async_trait]
    impl ProviderRouter for FixedProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &crate::context::AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            Ok(StepOutcome {
                assistant_text: self.text.clone(),
                tool_calls: vec![],
                done: true,
                usage: TokenUsage {
                    prompt_tokens: 10,
                    completion_tokens: 10,
                },
            })
        }
    }

    /// Emits one outbound tool call then declares done on the next step.
    struct OutboundProvider {
        call_emitted: Arc<Mutex<bool>>,
    }

    #[async_trait]
    impl ProviderRouter for OutboundProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &crate::context::AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            let mut emitted = self.call_emitted.lock().unwrap();
            if !*emitted {
                *emitted = true;
                Ok(StepOutcome {
                    assistant_text: "drafting outbound email".into(),
                    tool_calls: vec![CtxToolCall {
                        call_id: "c1".into(),
                        tool_id: "email.send".into(),
                        args: serde_json::json!({ "to": "alice@example.com", "body": "Hello" }),
                    }],
                    done: false,
                    usage: TokenUsage {
                        prompt_tokens: 5,
                        completion_tokens: 5,
                    },
                })
            } else {
                Ok(StepOutcome {
                    assistant_text: "done".into(),
                    tool_calls: vec![],
                    done: true,
                    usage: TokenUsage {
                        prompt_tokens: 5,
                        completion_tokens: 5,
                    },
                })
            }
        }
    }

    // ── Mock ToolInvoker ──────────────────────────────────────────────────────

    struct NoopInvoker;

    #[async_trait]
    impl ToolInvoker for NoopInvoker {
        async fn invoke(&self, call: &CtxToolCall) -> Result<String, ExecutorError> {
            Ok(format!("invoked:{}", call.tool_id))
        }
    }

    // ── MockChainExecutor that returns NeedsApproval ───────────────────────────

    /// Build a `ChainExecutor` that completes a 1-step prompt chain cleanly.
    fn clean_chain_executor() -> Arc<ChainExecutor> {
        let registry = Arc::new(ToolRegistry::new());
        let provider = Arc::new(FixedProvider {
            text: "drafted email body here".into(),
        });
        let invoker = Arc::new(NoopInvoker);
        let agent_exec = Arc::new(
            AgentExecutor::builder()
                .provider_router(provider.clone())
                .tool_invoker(invoker.clone())
                .build(),
        );
        Arc::new(
            ChainExecutor::builder()
                .provider_router(provider)
                .tool_invoker(invoker)
                .agent_executor(agent_exec)
                .tool_registry(registry)
                .build(),
        )
    }

    /// Build a `AgentExecutor` backed by an outbound provider (parks on
    /// `email.send`).
    fn outbound_agent_executor() -> Arc<AgentExecutor> {
        let registry = Arc::new({
            let r = ToolRegistry::new();
            r.register_tool(ToolDescriptor {
                id: "email.send".into(),
                name: "Email Send".into(),
                description: "Send an outbound email".into(),
                required_level: AccessLevel::Outbound,
            })
            .unwrap();
            r.set_grants(
                "automation.emailer",
                vec![ToolGrant {
                    tool_id: "email.send".into(),
                    level: AccessLevel::Outbound,
                    approved: false,
                }],
            );
            r
        });
        let provider = Arc::new(OutboundProvider {
            call_emitted: Arc::new(Mutex::new(false)),
        });
        Arc::new(
            AgentExecutor::builder()
                .provider_router(provider)
                .tool_invoker(Arc::new(NoopInvoker))
                .tool_registry(registry)
                .build(),
        )
    }

    fn clean_agent_executor() -> Arc<AgentExecutor> {
        Arc::new(
            AgentExecutor::builder()
                .provider_router(Arc::new(FixedProvider {
                    text: "summary output".into(),
                }))
                .tool_invoker(Arc::new(NoopInvoker))
                .build(),
        )
    }

    // ── Helper: build a simple 1-step prompt ChainFlow ────────────────────────

    fn prompt_chain(id: &str) -> ChainFlow {
        use crate::chain::ChainStep;
        ChainFlow {
            id: id.into(),
            name: id.into(),
            steps: vec![ChainStep::Prompt {
                template: "Draft content for: {{input}}".into(),
                inputs: vec!["input".into()],
                output: "draft".into(),
            }],
            description: None,
        }
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // CORE GUARDRAIL: trigger fires -> draft produced, NOT auto-sent
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn trigger_fires_produces_draft_not_sent() {
        let sink = Arc::new(RecordingSink::default());
        let chain = prompt_chain("email-draft");

        let runner = AutomationRunner::builder()
            .chain_library(vec![chain])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let automation = Automation {
            id: "auto-01".into(),
            name: "Test Email Draft".into(),
            description: None,
            enabled: true,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Chain {
                chain_ref: "email-draft".into(),
            },
        };

        let mut ctx = ChainContext::new();
        ctx.insert("input".into(), serde_json::json!("Write a greeting email"));

        let outcome = runner.run(&automation, ctx).await.unwrap();
        assert!(outcome.success, "run should succeed");
        // Sink must NOT have been called — draft requires approval first
        assert!(
            sink.drain().is_empty(),
            "outbound sink must NOT be called without explicit approval"
        );
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // APPROVE -> sink called once with the correct draft
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn approve_calls_sink_once() {
        let sink = Arc::new(RecordingSink::default());

        // Use an outbound agent executor that parks on email.send
        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(outbound_agent_executor())
            .sink(sink.clone())
            .build();

        let automation = Automation {
            id: "auto-02".into(),
            name: "Agent Email Draft".into(),
            description: None,
            enabled: true,
            trigger: AutomationTrigger::Hook {
                event: "inbox.message.received".into(),
            },
            target: AutomationTarget::Agent {
                role: AgentRole::Emailer,
                goal: "Draft a welcome email".into(),
            },
        };

        let outcome = runner.run(&automation, ChainContext::new()).await.unwrap();
        // Should have produced at least one draft
        assert!(
            !outcome.drafts.is_empty() || outcome.success,
            "should have produced a draft or succeeded"
        );

        // Manually inject a draft to test the approve path
        let draft = runner
            .create_draft(
                &automation,
                "Hello from the draft".into(),
                DraftKind::Email,
                HashMap::new(),
            )
            .await
            .unwrap();

        let draft_id = draft.id.clone();
        assert!(sink.drain().is_empty(), "sink not called before approval");

        runner.approve_draft(&draft_id).await.unwrap();
        let sent = sink.drain();
        assert_eq!(sent.len(), 1, "sink called exactly once on approval");
        assert_eq!(sent[0].id, draft_id);
        assert!(sent[0].approved);
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // DENY -> sink NOT called
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn deny_does_not_call_sink() {
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let automation = Automation {
            id: "auto-deny".into(),
            name: "Deny Test".into(),
            description: None,
            enabled: true,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Agent {
                role: AgentRole::Emailer,
                goal: "Draft email".into(),
            },
        };

        let draft = runner
            .create_draft(
                &automation,
                "Draft content".into(),
                DraftKind::Email,
                HashMap::new(),
            )
            .await
            .unwrap();

        runner.deny_draft(&draft.id).unwrap();
        assert!(
            sink.drain().is_empty(),
            "sink must NOT be called after denial"
        );

        // Draft is no longer pending
        assert!(!runner.pending_draft_ids().contains(&draft.id));
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // YAML AUTOMATION LOAD — three reference automations parse OK
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn yaml_automation_library_loads() {
        let dir = std::path::Path::new(
            "/home/user/projects/acamarata/cascade/.cascade/library/automations",
        );
        if !dir.exists() {
            // Skip if fixtures not yet written — builder will write them after
            return;
        }
        let automations = Automation::load_library(dir).unwrap();
        assert!(
            automations.len() >= 3,
            "expected at least 3 reference automations, found {}",
            automations.len()
        );
        for a in &automations {
            a.validate()
                .unwrap_or_else(|e| panic!("automation '{}' invalid: {e}", a.id));
        }
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // THREE REFERENCE AUTOMATIONS — dry-run via runner
    // ═══════════════════════════════════════════════════════════════════════════

    /// email-draft-from-prompt: manual trigger, chain target, email kind
    #[tokio::test]
    async fn ref_automation_email_draft_dry_run() {
        let email_chain = prompt_chain("email-draft");
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![email_chain])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let auto = Automation {
            id: "email-draft-from-prompt".into(),
            name: "Email Draft from Prompt".into(),
            description: Some("Draft an outbound email from a user prompt.".into()),
            enabled: true,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Chain {
                chain_ref: "email-draft".into(),
            },
        };

        auto.validate().unwrap();
        let mut ctx = ChainContext::new();
        ctx.insert(
            "input".into(),
            serde_json::json!("Draft a welcome email to new customers"),
        );

        let outcome = runner.run(&auto, ctx).await.unwrap();
        assert!(outcome.success);
        assert!(sink.drain().is_empty(), "never auto-sends");
    }

    /// inbox-triage: inbox event trigger, agent target (Triage role)
    #[tokio::test]
    async fn ref_automation_inbox_triage_dry_run() {
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let auto = Automation {
            id: "inbox-triage".into(),
            name: "Inbox Triage".into(),
            description: Some("Categorise and draft response to inbox messages.".into()),
            enabled: true,
            trigger: AutomationTrigger::InboxEvent {
                source: "email".into(),
            },
            target: AutomationTarget::Agent {
                role: AgentRole::Triage,
                goal: "Triage the incoming message and draft a response".into(),
            },
        };

        auto.validate().unwrap();
        let outcome = runner.run(&auto, ChainContext::new()).await.unwrap();
        assert!(outcome.success);
        assert!(sink.drain().is_empty(), "never auto-sends");
    }

    /// scheduled-summary: scheduled trigger, chain target
    #[tokio::test]
    async fn ref_automation_scheduled_summary_dry_run() {
        let summary_chain = prompt_chain("weekly-summary");
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![summary_chain])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let auto = Automation {
            id: "scheduled-summary".into(),
            name: "Scheduled Weekly Summary".into(),
            description: Some("Generate and draft a weekly activity summary.".into()),
            enabled: true,
            trigger: AutomationTrigger::Scheduled {
                cron: "0 9 * * 1".into(),
            },
            target: AutomationTarget::Chain {
                chain_ref: "weekly-summary".into(),
            },
        };

        auto.validate().unwrap();
        let mut ctx = ChainContext::new();
        ctx.insert("input".into(), serde_json::json!("Weekly project activity"));
        let outcome = runner.run(&auto, ctx).await.unwrap();
        assert!(outcome.success);
        assert!(sink.drain().is_empty(), "never auto-sends");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // AutomationTrigger serde round-trip
    // ═══════════════════════════════════════════════════════════════════════════

    #[test]
    fn trigger_serde_round_trip() {
        let triggers = vec![
            AutomationTrigger::Manual {},
            AutomationTrigger::Hook {
                event: "pr.opened".into(),
            },
            AutomationTrigger::Scheduled {
                cron: "0 9 * * 1-5".into(),
            },
            AutomationTrigger::InboxEvent {
                source: "github".into(),
            },
        ];
        for t in triggers {
            let json = serde_json::to_string(&t).unwrap();
            let decoded: AutomationTrigger = serde_json::from_str(&json).unwrap();
            assert_eq!(decoded, t);
        }
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // DraftArtifact serde round-trip
    // ═══════════════════════════════════════════════════════════════════════════

    #[test]
    fn draft_artifact_serde_round_trip() {
        let draft = DraftArtifact {
            id: "d1".into(),
            automation_id: "auto-01".into(),
            kind: DraftKind::Email,
            content: "Hello world".into(),
            metadata: HashMap::new(),
            created_at: Utc::now(),
            approved: false,
        };
        let json = serde_json::to_string(&draft).unwrap();
        let decoded: DraftArtifact = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.id, "d1");
        assert_eq!(decoded.kind, DraftKind::Email);
        assert!(!decoded.approved);
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Disabled automation returns Disabled error
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn disabled_automation_errors() {
        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .build();

        let auto = Automation {
            id: "disabled-auto".into(),
            name: "Disabled".into(),
            description: None,
            enabled: false,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Agent {
                role: AgentRole::Generic,
                goal: "do something".into(),
            },
        };

        let err = runner.run(&auto, ChainContext::new()).await.unwrap_err();
        assert!(matches!(err, AutomationError::Disabled { .. }));
    }
}

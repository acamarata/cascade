//! End-to-end validation for the real executor tool loop behind `SafetyGate`.
//!
//! The scripted provider advances only after observing each real tool result in
//! the executor context. This covers the complete AgentExecutor -> SafetyGate ->
//! LocalToolInvoker -> AgentExecutor feedback path for file read, bash, and file
//! write operations without duplicating the adversarial gate unit tests.

use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use async_trait::async_trait;

use cascade_agents::context::{AgentRunContext, StepOutcome, TokenUsage, ToolCall};
use cascade_agents::executor::{AgentExecutor, ExecutorError, ProviderRouter};
use cascade_agents::grants::{AccessLevel, ToolGrant};
use cascade_agents::spec::{builtin_coder, AgentRole};
use cascade_agents::task::{AgentTask, TaskStatus};
use cascade_agents::tool_registry::{ToolDescriptor, ToolRegistry};
use cascade_daemon::automation_router::{
    LocalCapabilitySet, LocalToolInvoker, SafetyGate,
};

struct ToolLoopProvider {
    input_path: PathBuf,
    output_path: PathBuf,
    step: AtomicUsize,
}

impl ToolLoopProvider {
    fn new(input_path: PathBuf, output_path: PathBuf) -> Self {
        Self {
            input_path,
            output_path,
            step: AtomicUsize::new(0),
        }
    }

    fn calls(&self) -> usize {
        self.step.load(Ordering::SeqCst)
    }
}

#[async_trait]
impl ProviderRouter for ToolLoopProvider {
    async fn step(
        &self,
        _spec: &cascade_agents::spec::AgentSpec,
        ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError> {
        let step = self.step.fetch_add(1, Ordering::SeqCst);
        let context = ctx
            .messages
            .iter()
            .map(|message| message.content.as_str())
            .collect::<Vec<_>>()
            .join("\n");

        let outcome = match step {
            0 => tool_step(
                "cascade.fs.read",
                serde_json::json!({"path": self.input_path.to_string_lossy()}),
                "e2e-read",
            ),
            1 => {
                assert!(
                    context.contains("[tool_result call_id=e2e-read] firewall-input"),
                    "executor must feed the real file contents back to the provider; context: {context}"
                );
                tool_step(
                    "cascade.bash.exec",
                    serde_json::json!({"command": "printf 'bash-stage-ok\\n'"}),
                    "e2e-bash",
                )
            }
            2 => {
                assert!(
                    context.contains("[tool_result call_id=e2e-bash] bash-stage-ok"),
                    "executor must feed the real bash stdout back to the provider; context: {context}"
                );
                tool_step(
                    "cascade.fs.write",
                    serde_json::json!({
                        "path": self.output_path.to_string_lossy(),
                        "content": "firewall-input\nbash-stage-ok\n",
                    }),
                    "e2e-write",
                )
            }
            3 => {
                assert!(
                    context.contains("[tool_result call_id=e2e-write] wrote 29 bytes"),
                    "executor must feed the real write result back to the provider; context: {context}"
                );
                StepOutcome {
                    assistant_text: "tool loop complete".into(),
                    tool_calls: vec![],
                    done: true,
                    usage: usage(),
                }
            }
            other => panic!("unexpected provider step {other}"),
        };

        Ok(outcome)
    }
}

fn usage() -> TokenUsage {
    TokenUsage {
        prompt_tokens: 1,
        completion_tokens: 1,
    }
}

fn tool_step(tool_id: &str, args: serde_json::Value, call_id: &str) -> StepOutcome {
    StepOutcome {
        assistant_text: format!("calling {tool_id}"),
        tool_calls: vec![ToolCall {
            tool_id: tool_id.into(),
            args,
            call_id: call_id.into(),
        }],
        done: false,
        usage: usage(),
    }
}

fn register_tool(registry: &ToolRegistry, id: &str, level: AccessLevel) {
    registry
        .register_tool(ToolDescriptor {
            id: id.into(),
            name: id.into(),
            description: format!("integration test descriptor for {id}"),
            required_level: level,
        })
        .expect("tool descriptor should register");
}

#[tokio::test]
async fn executor_runs_real_file_bash_file_loop_under_safety_gate() {
    let sandbox = tempfile::tempdir().expect("sandbox tempdir");
    let input_path = sandbox.path().join("input.txt");
    let output_path = sandbox.path().join("output.txt");
    std::fs::write(&input_path, "firewall-input").expect("write input fixture");

    let registry = Arc::new(ToolRegistry::new());
    register_tool(&registry, "cascade.fs.read", AccessLevel::Read);
    register_tool(&registry, "cascade.bash.exec", AccessLevel::Write);
    register_tool(&registry, "cascade.fs.write", AccessLevel::Write);
    registry.set_grants(
        "cascade.coder",
        vec![
            ToolGrant {
                tool_id: "cascade.fs.read".into(),
                level: AccessLevel::Read,
                approved: false,
            },
            ToolGrant {
                tool_id: "cascade.bash.exec".into(),
                level: AccessLevel::Write,
                approved: false,
            },
            ToolGrant {
                tool_id: "cascade.fs.write".into(),
                level: AccessLevel::Write,
                approved: false,
            },
        ],
    );

    let provider = Arc::new(ToolLoopProvider::new(
        input_path.clone(),
        output_path.clone(),
    ));
    let local_invoker = Arc::new(LocalToolInvoker::new(LocalCapabilitySet::all()));
    let guarded_invoker = Arc::new(SafetyGate::new(
        local_invoker,
        sandbox.path(),
        "e2e-tool-loop-agent",
    ));
    let executor = AgentExecutor::builder()
        .provider_router(provider.clone())
        .tool_invoker(guarded_invoker)
        .tool_registry(registry)
        .max_steps(8)
        .token_budget(1_000)
        .build();

    let task = AgentTask::new_root("run the guarded tool loop", AgentRole::Coder);
    let result = executor
        .run_task(builtin_coder(), task)
        .await
        .expect("guarded tool loop should complete");

    assert_eq!(result.status, TaskStatus::Done);
    assert_eq!(provider.calls(), 4, "provider should run exactly four steps");
    assert_eq!(
        std::fs::read_to_string(output_path).expect("read written output"),
        "firewall-input\nbash-stage-ok\n"
    );
}

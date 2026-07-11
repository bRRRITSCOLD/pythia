//! Tool dispatch (Task 15): turns a `ToolCall` the provider requested into a journalled
//! `KernelEvent::ToolResult`, via `pythia_capability_host::execute()` (Task 9) directly — a
//! concrete dependency, no trait, per ADR-0001/plan §0.

use std::collections::HashMap;

use pythia_capability_host::{execute, ExecutionStatus};
use pythia_manifest::host_fn::{RESULT_TAG_ERR, RESULT_TAG_OK};
use pythia_manifest::{PolicyFile, SkillManifest};

use crate::event::{KernelEvent, ToolCall};

/// Static configuration for one dispatchable skill, wired by the composition root (`pythia-cli`,
/// Task 16): the compiled wasm module, the manifest `execute()` resolves grants against, and
/// whether this skill's output is untrusted-sourced.
///
/// `tainted_output` is a skill-author-declared property (data model doc §7), not something the
/// kernel infers from the bytes it gets back — e.g. `read-file` reads file content from outside
/// the sandbox and is unconditionally tainted; a skill that only echoes its own caller-supplied
/// arguments back would not be. This crate has no opinion of its own on which skills produce
/// tainted output; it only threads the caller's declaration through to the journalled event,
/// exactly like `pythia_capability_host::ExecutionResult::is_tainted`'s own doc explains for the
/// layer below this one.
#[derive(Debug, Clone)]
pub struct SkillConfig {
    pub manifest: SkillManifest,
    pub module_bytes: Vec<u8>,
    pub tainted_output: bool,
}

/// Dispatches `tool_call` through the capability host and returns the resulting `ToolResult`
/// event, ready to journal. Never panics: an unregistered tool name, a policy denial, and a
/// resource-limit kill all fold into a `ToolResult` with an appropriate `status` (data model doc
/// §4 — a denial is itself a recorded fact, not a separate event type), exactly like
/// `execute()`'s own three-status contract this function is built on top of.
///
/// `triggering_tainted` is the taint of the `LlmResponse` event that carried `tool_call` (the
/// caller — `drive_turn` — already holds it, since dispatch only ever follows reading that event
/// off the history). The unregistered-tool denial below embeds `tool_call.name` verbatim into its
/// `reason`, and that name is LLM-controlled: a `false` here would launder tainted, provider-
/// supplied bytes into an untainted event (SR-8, data model doc §7). The granted/denied/
/// resource-limit arms below don't take this parameter into account — their `tainted` already
/// derives from `result.is_tainted()`, itself seeded from the skill's own declared taint, which is
/// the correct source of truth once a real skill ran.
pub(crate) fn dispatch_tool(
    tool_call: &ToolCall,
    skills: &HashMap<String, SkillConfig>,
    policy: &PolicyFile,
    triggering_tainted: bool,
) -> KernelEvent {
    let Some(skill) = skills.get(&tool_call.name) else {
        return KernelEvent::ToolResult {
            tool: tool_call.name.clone(),
            status: "denied".to_string(),
            output: String::new(),
            reason: Some(format!("no skill registered for tool `{}`", tool_call.name)),
            tainted: triggering_tainted,
        };
    };

    let args = serde_json::to_vec(&tool_call.arguments).unwrap_or_default();
    let result = execute(
        &skill.module_bytes,
        &skill.manifest,
        policy,
        &args,
        skill.tainted_output,
    );

    match result.status() {
        ExecutionStatus::Ok => {
            let (status, output) = decode_skill_result(result.as_bytes());
            KernelEvent::ToolResult {
                tool: tool_call.name.clone(),
                status,
                output,
                reason: None,
                tainted: result.is_tainted(),
            }
        }
        ExecutionStatus::Denied => KernelEvent::ToolResult {
            tool: tool_call.name.clone(),
            status: "denied".to_string(),
            output: String::new(),
            reason: Some(String::from_utf8_lossy(result.as_bytes()).into_owned()),
            tainted: result.is_tainted(),
        },
        ExecutionStatus::ResourceLimitExceeded => KernelEvent::ToolResult {
            tool: tool_call.name.clone(),
            status: "resource_limit_exceeded".to_string(),
            output: String::new(),
            reason: Some(String::from_utf8_lossy(result.as_bytes()).into_owned()),
            tainted: result.is_tainted(),
        },
    }
}

/// Decodes the tag-prefixed bytes a skill's `run` export hands back (`pythia_skill_sdk::result`'s
/// `ok_result`/`err_result` convention, data model-adjacent shared vocabulary in
/// `pythia_manifest::host_fn`) into `(status, output)`. An `ExecutionStatus::Ok` result can still
/// carry the skill's own `err_result` tag (an application-level failure inside the sandbox, e.g.
/// malformed skill arguments) — kept distinct from the host-level `denied`/`resource_limit_exceeded`
/// statuses above, since it's a different fact: the skill *ran*, and *it* reported failure.
fn decode_skill_result(bytes: &[u8]) -> (String, String) {
    match bytes.split_first() {
        Some((&RESULT_TAG_OK, payload)) => (
            "ok".to_string(),
            String::from_utf8_lossy(payload).into_owned(),
        ),
        Some((&RESULT_TAG_ERR, payload)) => (
            "error".to_string(),
            String::from_utf8_lossy(payload).into_owned(),
        ),
        _ => (
            "error".to_string(),
            "malformed skill result: missing tag byte".to_string(),
        ),
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use std::path::PathBuf;
    use std::process::Command;

    use pythia_manifest::{Capability, Decision};

    use super::*;

    /// Builds the real `read-file` skill for `wasm32-wasip1` (same approach as
    /// `pythia-capability-host`'s own `tests/execute.rs`) and returns the compiled module bytes.
    /// A real skill, not a synthetic WAT fixture, is what makes
    /// `Dispatch_ReadFileSkillResult_TaintedFlagSetTrue` prove the taint invariant against the
    /// actual durability-demo skill (Task 12), not a stand-in.
    fn build_read_file_wasm() -> Vec<u8> {
        let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let repo_root = manifest_dir
            .parent()
            .expect("crates/kernel must have a parent (crates/)")
            .parent()
            .expect("crates/ must have a parent (the repo root)");
        let skills_workspace_dir = repo_root.join("skills");

        let status = Command::new(env!("CARGO"))
            .arg("build")
            .arg("--package")
            .arg("read-file")
            .arg("--target")
            .arg("wasm32-wasip1")
            .current_dir(&skills_workspace_dir)
            .status()
            .expect("failed to invoke cargo build for wasm32-wasip1 target");
        assert!(
            status.success(),
            "cargo build --package read-file --target wasm32-wasip1 must succeed"
        );

        let wasm_path = skills_workspace_dir
            .join("target")
            .join("wasm32-wasip1")
            .join("debug")
            .join("read-file.wasm");
        std::fs::read(&wasm_path).unwrap_or_else(|e| panic!("reading {wasm_path:?}: {e}"))
    }

    fn read_file_skills_and_policy(
        granted_dir: &std::path::Path,
    ) -> (HashMap<String, SkillConfig>, PolicyFile) {
        let capability = Capability::FsRead(granted_dir.to_path_buf());
        let manifest = SkillManifest {
            name: "read_file".to_string(),
            requested: vec![capability.clone()],
        };
        let module_bytes = build_read_file_wasm();
        let mut skills = HashMap::new();
        skills.insert(
            "read_file".to_string(),
            SkillConfig {
                manifest,
                module_bytes,
                tainted_output: true,
            },
        );
        let mut policy_skills = HashMap::new();
        policy_skills.insert(
            "read_file".to_string(),
            HashMap::from([(capability, Decision::Grant)]),
        );
        (
            skills,
            PolicyFile {
                skills: policy_skills,
            },
        )
    }

    #[test]
    fn Dispatch_ReadFileSkillResult_TaintedFlagSetTrue() {
        let dir = tempfile::tempdir().expect("tempdir creates");
        let file_path = dir.path().join("notes.txt");
        std::fs::write(&file_path, b"buy milk").expect("fixture file writes");

        let (skills, policy) = read_file_skills_and_policy(dir.path());

        let tool_call = ToolCall {
            name: "read_file".to_string(),
            arguments: serde_json::json!({"path": file_path.to_string_lossy()}),
        };

        let event = dispatch_tool(&tool_call, &skills, &policy, true);

        match event {
            KernelEvent::ToolResult {
                tool,
                status,
                output,
                tainted,
                reason,
            } => {
                assert_eq!(tool, "read_file");
                assert_eq!(status, "ok");
                assert_eq!(output, "buy milk");
                assert_eq!(reason, None);
                assert!(
                    tainted,
                    "read-file's output is unconditionally tainted (data model doc §7) — expected the \
                     kernel's SkillConfig-declared taint flag to survive into the journalled ToolResult"
                );
            }
            other => panic!("expected a ToolResult event, got {other:?}"),
        }
    }

    #[test]
    fn Dispatch_UnregisteredTool_DeniedResultNotPanic() {
        let skills = HashMap::new();
        let policy = PolicyFile::default();
        let tool_call = ToolCall {
            name: "no_such_tool".to_string(),
            arguments: serde_json::json!({}),
        };

        let event = dispatch_tool(&tool_call, &skills, &policy, false);

        match event {
            KernelEvent::ToolResult { status, .. } => assert_eq!(status, "denied"),
            other => panic!("expected a ToolResult event, got {other:?}"),
        }
    }

    /// SR-8 (data model doc §7): the unregistered-tool denial's `reason` embeds
    /// `tool_call.name` verbatim, and that name came from a tainted `LlmResponse` (the LLM is
    /// always an untrusted source). The denial must inherit that taint rather than hardcoding
    /// `tainted: false`, or a downstream taint pre-check would be fed laundered-clean data.
    #[test]
    fn Dispatch_UnregisteredTool_DenialInheritsTriggeringLlmResponseTaint() {
        let skills = HashMap::new();
        let policy = PolicyFile::default();
        let tool_call = ToolCall {
            name: "no_such_tool".to_string(),
            arguments: serde_json::json!({}),
        };

        let event = dispatch_tool(&tool_call, &skills, &policy, true);

        match event {
            KernelEvent::ToolResult {
                status, tainted, ..
            } => {
                assert_eq!(status, "denied");
                assert!(
                    tainted,
                    "denial reason embeds an LLM-controlled tool name; it must inherit the \
                     triggering LlmResponse's taint (SR-8) rather than hardcode false"
                );
            }
            other => panic!("expected a ToolResult event, got {other:?}"),
        }
    }
}

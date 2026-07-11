//! `pythia-provider` — the LLM provider seam.
//!
//! Owns the [`Provider`] trait and the wire-agnostic types (`Message`,
//! `ToolSchema`, `ToolCall`, `ResponseChunk`) that every provider
//! implementation (Ollama today; Anthropic/OpenRouter/OpenAI later) speaks,
//! so `pythia-kernel` never depends on a concrete provider (ADR-0001/0005).
//!
//! This crate also ships (behind the `test-util` feature) a `MockProvider`
//! test double and a reusable contract-test suite that any implementer of
//! `Provider` can run against itself to prove wire-compatibility with the
//! kernel's expectations before it is ever wired in.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

#[cfg(feature = "test-util")]
pub mod contract_tests;
#[cfg(feature = "test-util")]
pub mod mock;

/// Who authored a [`Message`] in a conversation.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Role {
    System,
    User,
    Assistant,
    Tool,
}

/// A single turn in the conversation sent to a provider. Wire-agnostic: it
/// carries no assumption about any specific provider's HTTP/JSON shape —
/// translating to/from that shape is the implementer's job (e.g.
/// `pythia-provider-ollama::wire`).
///
/// # Known deferral: tool-call/tool-result correlation (Task 15)
/// This shape is deliberately minimal for the current slice and does not
/// yet carry `tool_calls` on an assistant [`Message`] or a `tool_call_id`
/// on a [`Role::Tool`] message. The kernel turn loop (Task 15) must replay
/// "assistant requested tool X (id) → tool result for id" back to the
/// provider, and the OpenAI-compatible dialect (Task 10) requires
/// `assistant.tool_calls[]` / `tool.tool_call_id` on the wire. Extending
/// `Message` (e.g. `tool_calls: Vec<ToolCall>` on the assistant turn, or a
/// content enum) is expected before Task 15 lands, and should happen
/// TDD-first since this is the frozen seam the contract suite and every
/// provider implementer build against — do not let it slide silently into
/// Task 10/15.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Message {
    pub role: Role,
    pub content: String,
}

impl Message {
    pub fn new(role: Role, content: impl Into<String>) -> Self {
        Self {
            role,
            content: content.into(),
        }
    }

    pub fn system(content: impl Into<String>) -> Self {
        Self::new(Role::System, content)
    }

    pub fn user(content: impl Into<String>) -> Self {
        Self::new(Role::User, content)
    }

    pub fn assistant(content: impl Into<String>) -> Self {
        Self::new(Role::Assistant, content)
    }

    pub fn tool(content: impl Into<String>) -> Self {
        Self::new(Role::Tool, content)
    }
}

/// Describes a tool the provider may choose to call, in JSON-schema form.
/// Wire-agnostic: `parameters_schema` is a plain JSON Schema document;
/// translating it into a specific provider's tool-declaration wire shape is
/// the implementer's job.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ToolSchema {
    pub name: String,
    pub description: String,
    pub parameters_schema: serde_json::Value,
}

/// A tool invocation the provider requested.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    pub arguments: serde_json::Value,
}

/// One unit of a provider's response. A single [`Provider::request`] call
/// yields an ordered `Vec<ResponseChunk>` — the kernel needs the complete,
/// ordered turn output (there is no interactive UI to stream tokens to in
/// this headless engine), so "ordered collection" rather than an async
/// `Stream` is the simplest thing that satisfies the architecture (KISS);
/// implementers that talk to a chunked-HTTP wire (e.g. Ollama) collect their
/// own stream into this `Vec` before returning.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", content = "data", rename_all = "snake_case")]
pub enum ResponseChunk {
    Text(String),
    ToolCall(ToolCall),
}

/// Errors a [`Provider`] implementation can surface to the kernel.
#[derive(Debug, thiserror::Error)]
pub enum ProviderError {
    #[error("at least one message is required")]
    EmptyMessages,
    #[error("provider request failed: {0}")]
    RequestFailed(String),
}

/// The provider seam. One trait, multiple real implementers (Ollama now;
/// more BYO-key providers later) — this is what earns it trait status over
/// the event log / capability host, which have exactly one implementation
/// each in this slice (ADR-0005).
#[async_trait]
pub trait Provider: Send + Sync {
    /// Sends `messages` (the full turn context — compaction is the kernel's
    /// job, not the provider's) and the tools available for this turn, and
    /// returns the ordered chunks of the provider's response.
    ///
    /// # Errors
    /// Implementations MUST return [`ProviderError::EmptyMessages`] when
    /// `messages` is empty, without making a wire call — this is the one
    /// piece of validation every implementer owns identically, and is
    /// covered by the shared contract-test suite.
    async fn request(
        &self,
        messages: &[Message],
        tools: &[ToolSchema],
    ) -> Result<Vec<ResponseChunk>, ProviderError>;
}

#[cfg(test)]
mod wire_type_serde_tests {
    //! Round-trip and JSON-shape tests for the wire types' derived
    //! `Serialize`/`Deserialize` impls. These attributes (`rename_all`,
    //! `tag`, `content`) define the exact JSON contract the kernel persists
    //! as `payload_json` in the event log — a compiling derive is not
    //! evidence the shape is representable at runtime (see the
    //! `ResponseChunk::Text` internal-tagging bug this suite now guards
    //! against).

    use super::*;
    use serde_json::json;

    fn round_trip<T>(value: &T)
    where
        T: Serialize + for<'de> Deserialize<'de> + PartialEq + std::fmt::Debug,
    {
        let json = serde_json::to_string(value).expect("serialize should succeed");
        let back: T = serde_json::from_str(&json).expect("deserialize should succeed");
        assert_eq!(value, &back, "round-trip should preserve equality");
    }

    #[test]
    fn message_round_trips_through_json() {
        round_trip(&Message::user("hello"));
        round_trip(&Message::system("be helpful"));
        round_trip(&Message::assistant("hi there"));
        round_trip(&Message::tool("42"));
    }

    #[test]
    fn tool_schema_round_trips_through_json() {
        round_trip(&ToolSchema {
            name: "get_weather".to_string(),
            description: "fetch current weather".to_string(),
            parameters_schema: json!({"type": "object", "properties": {}}),
        });
    }

    #[test]
    fn tool_call_round_trips_through_json() {
        round_trip(&ToolCall {
            id: "call_1".to_string(),
            name: "get_weather".to_string(),
            arguments: json!({"city": "Seattle"}),
        });
    }

    #[test]
    fn response_chunk_text_round_trips_through_json() {
        round_trip(&ResponseChunk::Text("hello".to_string()));
    }

    #[test]
    fn response_chunk_tool_call_round_trips_through_json() {
        round_trip(&ResponseChunk::ToolCall(ToolCall {
            id: "call_1".to_string(),
            name: "get_weather".to_string(),
            arguments: json!({"city": "Seattle"}),
        }));
    }

    #[test]
    fn response_chunk_text_serializes_to_expected_shape() {
        let value = serde_json::to_value(ResponseChunk::Text("hello".to_string()))
            .expect("serialize should succeed");
        assert_eq!(value, json!({"kind": "text", "data": "hello"}));
    }

    #[test]
    fn response_chunk_tool_call_serializes_to_expected_shape() {
        let value = serde_json::to_value(ResponseChunk::ToolCall(ToolCall {
            id: "call_1".to_string(),
            name: "get_weather".to_string(),
            arguments: json!({"city": "Seattle"}),
        }))
        .expect("serialize should succeed");
        assert_eq!(
            value,
            json!({
                "kind": "tool_call",
                "data": {
                    "id": "call_1",
                    "name": "get_weather",
                    "arguments": {"city": "Seattle"}
                }
            })
        );
    }
}

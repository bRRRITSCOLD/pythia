//! The OpenAI-compatible request/response JSON shapes Ollama's
//! `/v1/chat/completions` endpoint speaks, private to this crate.
//!
//! Every Ollama-specific wire quirk (e.g. tool-call `arguments` sometimes
//! arriving as a JSON object and sometimes as a JSON-encoded string,
//! depending on model/version) is translated here so
//! `pythia_provider`'s wire-agnostic types stay clean (ADR-0005).

use pythia_provider::{Message, ResponseChunk, Role, ToolCall, ToolSchema};
use serde::{Deserialize, Serialize};

#[derive(Debug, Serialize)]
pub(crate) struct ChatRequest {
    pub model: String,
    pub messages: Vec<WireMessage>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<WireTool>,
    pub stream: bool,
}

#[derive(Debug, Serialize)]
pub(crate) struct WireMessage {
    pub role: &'static str,
    pub content: String,
}

impl From<&Message> for WireMessage {
    fn from(message: &Message) -> Self {
        Self {
            role: wire_role(message.role),
            content: message.content.clone(),
        }
    }
}

fn wire_role(role: Role) -> &'static str {
    match role {
        Role::System => "system",
        Role::User => "user",
        Role::Assistant => "assistant",
        Role::Tool => "tool",
    }
}

#[derive(Debug, Serialize)]
pub(crate) struct WireTool {
    #[serde(rename = "type")]
    pub kind: &'static str,
    pub function: WireFunction,
}

#[derive(Debug, Serialize)]
pub(crate) struct WireFunction {
    pub name: String,
    pub description: String,
    pub parameters: serde_json::Value,
}

impl From<&ToolSchema> for WireTool {
    fn from(schema: &ToolSchema) -> Self {
        Self {
            kind: "function",
            function: WireFunction {
                name: schema.name.clone(),
                description: schema.description.clone(),
                parameters: schema.parameters_schema.clone(),
            },
        }
    }
}

#[derive(Debug, Deserialize)]
pub(crate) struct ChatResponse {
    pub choices: Vec<WireChoice>,
}

#[derive(Debug, Deserialize)]
pub(crate) struct WireChoice {
    pub message: WireResponseMessage,
}

#[derive(Debug, Deserialize)]
pub(crate) struct WireResponseMessage {
    #[serde(default)]
    pub content: Option<String>,
    #[serde(default)]
    pub tool_calls: Vec<WireToolCall>,
}

#[derive(Debug, Deserialize)]
pub(crate) struct WireToolCall {
    #[serde(default)]
    pub id: Option<String>,
    pub function: WireToolCallFunction,
}

#[derive(Debug, Deserialize)]
pub(crate) struct WireToolCallFunction {
    pub name: String,
    #[serde(deserialize_with = "deserialize_arguments")]
    pub arguments: serde_json::Value,
}

/// Ollama has, across versions, sent tool-call `arguments` as either a JSON
/// object (matching the OpenAI dialect loosely) or a JSON-encoded string
/// (matching real OpenAI's strict wire contract). Accept either and
/// normalize to a `serde_json::Value` object so the kernel never sees the
/// discrepancy.
fn deserialize_arguments<'de, D>(deserializer: D) -> Result<serde_json::Value, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw = serde_json::Value::deserialize(deserializer)?;
    match raw {
        serde_json::Value::String(encoded) => {
            serde_json::from_str(&encoded).map_err(serde::de::Error::custom)
        }
        other => Ok(other),
    }
}

/// An error translating a parsed [`ChatResponse`] into wire-agnostic
/// [`ResponseChunk`]s (e.g. Ollama returned zero choices).
#[derive(Debug, thiserror::Error)]
pub(crate) enum WireTranslationError {
    #[error("ollama response contained no choices")]
    NoChoices,
}

impl ChatResponse {
    pub(crate) fn into_response_chunks(self) -> Result<Vec<ResponseChunk>, WireTranslationError> {
        let choice = self
            .choices
            .into_iter()
            .next()
            .ok_or(WireTranslationError::NoChoices)?;

        let mut chunks = Vec::new();
        if let Some(content) = choice.message.content {
            if !content.is_empty() {
                chunks.push(ResponseChunk::Text(content));
            }
        }
        for (index, tool_call) in choice.message.tool_calls.into_iter().enumerate() {
            chunks.push(ResponseChunk::ToolCall(ToolCall {
                id: tool_call.id.unwrap_or_else(|| format!("call_{index}")),
                name: tool_call.function.name,
                arguments: tool_call.function.arguments,
            }));
        }
        Ok(chunks)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn wire_tool_call_response_body_parses_into_tool_call_chunk() {
        let body = json!({
            "choices": [{
                "message": {
                    "role": "assistant",
                    "content": null,
                    "tool_calls": [{
                        "id": "call-1",
                        "type": "function",
                        "function": {
                            "name": "read_file",
                            "arguments": {"path": "/tmp/example.txt"}
                        }
                    }]
                }
            }]
        });

        let response: ChatResponse =
            serde_json::from_value(body).expect("valid response body should parse");
        let chunks = response
            .into_response_chunks()
            .expect("translation should succeed");

        assert_eq!(
            chunks,
            vec![ResponseChunk::ToolCall(ToolCall {
                id: "call-1".to_string(),
                name: "read_file".to_string(),
                arguments: json!({"path": "/tmp/example.txt"}),
            })]
        );
    }

    #[test]
    fn wire_tool_call_with_stringified_arguments_parses_into_object_arguments() {
        let body = json!({
            "choices": [{
                "message": {
                    "role": "assistant",
                    "content": null,
                    "tool_calls": [{
                        "id": "call-1",
                        "type": "function",
                        "function": {
                            "name": "read_file",
                            "arguments": "{\"path\": \"/tmp/example.txt\"}"
                        }
                    }]
                }
            }]
        });

        let response: ChatResponse =
            serde_json::from_value(body).expect("valid response body should parse");
        let chunks = response
            .into_response_chunks()
            .expect("translation should succeed");

        assert_eq!(
            chunks,
            vec![ResponseChunk::ToolCall(ToolCall {
                id: "call-1".to_string(),
                name: "read_file".to_string(),
                arguments: json!({"path": "/tmp/example.txt"}),
            })]
        );
    }

    #[test]
    fn wire_text_only_response_body_parses_into_text_chunk() {
        let body = json!({
            "choices": [{
                "message": {
                    "role": "assistant",
                    "content": "hello there"
                }
            }]
        });

        let response: ChatResponse =
            serde_json::from_value(body).expect("valid response body should parse");
        let chunks = response
            .into_response_chunks()
            .expect("translation should succeed");

        assert_eq!(chunks, vec![ResponseChunk::Text("hello there".to_string())]);
    }

    #[test]
    fn wire_no_choices_response_body_errors_not_panics() {
        let body = json!({ "choices": [] });
        let response: ChatResponse =
            serde_json::from_value(body).expect("valid response body should parse");

        let result = response.into_response_chunks();

        assert!(matches!(result, Err(WireTranslationError::NoChoices)));
    }
}

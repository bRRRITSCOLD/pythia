//! Live-model tests against a real local Ollama instance running
//! `qwen3.5`. `#[ignore]`-gated: these do not run in the default `cargo
//! test` merge-gate lane (which is MockProvider/mocked-HTTP only per the
//! stack profile) — run manually with:
//!
//! ```sh
//! cargo test -p pythia-provider-ollama --test live_ollama -- --ignored
//! ```
//!
//! requires `ollama serve` running locally with the `qwen3.5` model pulled.

use pythia_provider::{Message, Provider, ResponseChunk, ToolSchema};
use pythia_provider_ollama::OllamaProvider;
use serde_json::json;

fn local_provider() -> OllamaProvider {
    OllamaProvider::new("http://localhost:11434")
}

#[tokio::test]
#[ignore = "requires a live local Ollama server with qwen3.5 pulled"]
async fn live_ollama_simple_text_prompt_returns_non_empty_text() {
    let provider = local_provider();
    let messages = vec![Message::user(
        "Reply with a single short sentence confirming you are working.",
    )];

    let chunks = provider
        .request(&messages, &[])
        .await
        .expect("Live_Ollama_SimpleTextPrompt_ReturnsNonEmptyText: request should succeed");

    assert!(
        chunks.iter().any(
            |chunk| matches!(chunk, ResponseChunk::Text(text) if !text.trim().is_empty())
        ),
        "Live_Ollama_SimpleTextPrompt_ReturnsNonEmptyText: expected a non-empty text chunk, got {chunks:?}"
    );
}

#[tokio::test]
#[ignore = "requires a live local Ollama server with qwen3.5 pulled"]
async fn live_ollama_tool_schema_provided_returns_well_formed_tool_call() {
    let provider = local_provider();
    let messages = vec![Message::user(
        "Call the get_weather tool for the city of Seattle.",
    )];
    let tools = vec![ToolSchema {
        name: "get_weather".to_string(),
        description: "Get the current weather for a city".to_string(),
        parameters_schema: json!({
            "type": "object",
            "properties": { "city": { "type": "string" } },
            "required": ["city"]
        }),
    }];

    let chunks = provider
        .request(&messages, &tools)
        .await
        .expect("Live_Ollama_ToolSchemaProvided_ReturnsWellFormedToolCall: request should succeed");

    let tool_call = chunks.iter().find_map(|chunk| match chunk {
        ResponseChunk::ToolCall(tool_call) => Some(tool_call),
        _ => None,
    });

    let tool_call = tool_call.unwrap_or_else(|| {
        panic!(
            "Live_Ollama_ToolSchemaProvided_ReturnsWellFormedToolCall: expected a tool call chunk, got {chunks:?}"
        )
    });

    assert_eq!(tool_call.name, "get_weather");
    assert!(
        tool_call.arguments.is_object(),
        "Live_Ollama_ToolSchemaProvided_ReturnsWellFormedToolCall: arguments must be a JSON object, got {:?}",
        tool_call.arguments
    );
}

//! Runs the shared provider contract suite against `MockProvider` itself,
//! proving the harness is sound before any real implementer (e.g.
//! `pythia-provider-ollama`) uses it.

use pythia_provider::contract_tests::run_provider_contract_tests;
use pythia_provider::mock::{MockProvider, ScriptedResponse};
use pythia_provider::{Message, Provider, ResponseChunk, ToolCall};

/// The canonical 3-entry script the shared suite expects from a fresh
/// provider — see `pythia_provider::contract_tests` module docs.
fn canonical_script() -> Vec<ScriptedResponse> {
    vec![
        ScriptedResponse::text("hello from the provider"),
        ScriptedResponse::chunks(vec![ResponseChunk::ToolCall(ToolCall {
            id: "call-1".to_string(),
            name: "read_file".to_string(),
            arguments: serde_json::json!({ "path": "/tmp/example.txt" }),
        })]),
        ScriptedResponse::chunks(vec![
            ResponseChunk::Text("thinking...".to_string()),
            ResponseChunk::ToolCall(ToolCall {
                id: "call-2".to_string(),
                name: "read_file".to_string(),
                arguments: serde_json::json!({ "path": "/tmp/example.txt" }),
            }),
        ]),
    ]
}

#[tokio::test]
async fn contract_suite_passes_against_mock_provider() {
    run_provider_contract_tests(|| MockProvider::new(canonical_script())).await;
}

#[tokio::test]
async fn mock_scripted_sequence_returns_in_order() {
    let provider = MockProvider::new(canonical_script());
    let messages = vec![Message::user("hi")];

    let first = provider.request(&messages, &[]).await.unwrap();
    assert_eq!(
        first,
        vec![ResponseChunk::Text("hello from the provider".to_string())]
    );

    let second = provider.request(&messages, &[]).await.unwrap();
    assert!(matches!(second.as_slice(), [ResponseChunk::ToolCall(_)]));

    let third = provider.request(&messages, &[]).await.unwrap();
    assert_eq!(third.len(), 2);
}

#[tokio::test]
async fn mock_call_count_increments_exactly_once_per_request() {
    let provider = MockProvider::new(canonical_script());
    let messages = vec![Message::user("hi")];

    assert_eq!(provider.call_count(), 0);

    provider.request(&messages, &[]).await.unwrap();
    assert_eq!(provider.call_count(), 1);

    provider.request(&messages, &[]).await.unwrap();
    assert_eq!(provider.call_count(), 2);

    // an empty-messages call errors before scripting/recording, so the
    // count must not tick.
    let _ = provider.request(&[], &[]).await;
    assert_eq!(provider.call_count(), 2);

    assert_eq!(provider.calls(), vec![messages.clone(), messages]);
}

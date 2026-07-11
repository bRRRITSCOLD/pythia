//! A reusable contract-test suite any [`Provider`] implementer runs against
//! itself to prove wire-compatibility before the kernel ever depends on it.
//!
//! # Convention
//!
//! `make_provider` must return a **fresh** provider whose first three
//! consecutive [`Provider::request`] calls (given any non-empty `messages`)
//! yield, in order:
//!
//! 1. a single text chunk,
//! 2. a single tool-call chunk,
//! 3. two chunks, in a fixed relative order (text, then tool-call) —
//!    proving a multi-chunk response is not reordered or truncated.
//!
//! This lets one factory closure drive all three positive scenarios without
//! the suite needing implementer-specific scripting hooks: `MockProvider`
//! satisfies it with a 3-entry script (see `tests/contract_mock.rs`), and a
//! wiremock-backed HTTP double for a real implementer (e.g. Ollama, Task
//! 10) satisfies it with three sequential canned HTTP responses.
//!
//! The empty-messages case is universal validation every implementer owns
//! identically, so it is exercised on its own fresh provider instance.

use crate::{Message, Provider, ProviderError, ResponseChunk, ToolCall};

fn sample_messages() -> Vec<Message> {
    vec![Message::user("hello")]
}

async fn contract_text_only_response_yields_text_chunk<P: Provider>(provider: &P) {
    let response = provider
        .request(&sample_messages(), &[])
        .await
        .expect("Contract_TextOnlyResponse_YieldsTextChunk: request should succeed");

    assert_eq!(
        response.len(),
        1,
        "Contract_TextOnlyResponse_YieldsTextChunk: expected exactly one chunk"
    );
    assert!(
        matches!(response[0], ResponseChunk::Text(_)),
        "Contract_TextOnlyResponse_YieldsTextChunk: expected a Text chunk, got {:?}",
        response[0]
    );
}

async fn contract_tool_call_response_yields_tool_call_with_name_and_args<P: Provider>(
    provider: &P,
) {
    let response = provider
        .request(&sample_messages(), &[])
        .await
        .expect("Contract_ToolCallResponse_YieldsToolCallWithNameAndArgs: request should succeed");

    assert_eq!(
        response.len(),
        1,
        "Contract_ToolCallResponse_YieldsToolCallWithNameAndArgs: expected exactly one chunk"
    );
    match &response[0] {
        ResponseChunk::ToolCall(ToolCall { name, arguments, .. }) => {
            assert!(
                !name.is_empty(),
                "Contract_ToolCallResponse_YieldsToolCallWithNameAndArgs: tool call name must not be empty"
            );
            assert!(
                arguments.is_object(),
                "Contract_ToolCallResponse_YieldsToolCallWithNameAndArgs: tool call arguments must be a JSON object, got {arguments:?}"
            );
        }
        other => panic!(
            "Contract_ToolCallResponse_YieldsToolCallWithNameAndArgs: expected a ToolCall chunk, got {other:?}"
        ),
    }
}

async fn contract_multi_chunk_stream_preserves_order<P: Provider>(provider: &P) {
    let response = provider
        .request(&sample_messages(), &[])
        .await
        .expect("Contract_MultiChunkStream_PreservesOrder: request should succeed");

    assert_eq!(
        response.len(),
        2,
        "Contract_MultiChunkStream_PreservesOrder: expected exactly two chunks"
    );
    assert!(
        matches!(response[0], ResponseChunk::Text(_)),
        "Contract_MultiChunkStream_PreservesOrder: expected chunk 0 to be Text, got {:?}",
        response[0]
    );
    assert!(
        matches!(response[1], ResponseChunk::ToolCall(_)),
        "Contract_MultiChunkStream_PreservesOrder: expected chunk 1 to be ToolCall, got {:?}",
        response[1]
    );
}

async fn contract_empty_messages_returns_error<P: Provider>(provider: &P) {
    let result = provider.request(&[], &[]).await;
    assert!(
        matches!(result, Err(ProviderError::EmptyMessages)),
        "Contract_EmptyMessages_ReturnsError: expected ProviderError::EmptyMessages, got {result:?}"
    );
}

/// Runs the full contract suite against fresh providers built by
/// `make_provider`. See module docs for what "fresh" and "scripted" mean
/// here.
pub async fn run_provider_contract_tests<P, F>(make_provider: F)
where
    P: Provider,
    F: Fn() -> P,
{
    contract_text_only_response_yields_text_chunk(&make_provider()).await;
    contract_tool_call_response_yields_tool_call_with_name_and_args(&{
        let p = make_provider();
        // consume the first (text-only) scripted response to reach the
        // tool-call one.
        advance_one_call(&p).await;
        p
    })
    .await;
    contract_multi_chunk_stream_preserves_order(&{
        let p = make_provider();
        advance_one_call(&p).await;
        advance_one_call(&p).await;
        p
    })
    .await;
    contract_empty_messages_returns_error(&make_provider()).await;
}

/// Consumes exactly one scripted response by issuing a throwaway request,
/// so the suite can walk a fresh provider's canonical 3-entry script up to
/// the scenario under test.
async fn advance_one_call<P: Provider>(provider: &P) {
    provider
        .request(&sample_messages(), &[])
        .await
        .expect("contract suite: advancing the canonical script failed");
}

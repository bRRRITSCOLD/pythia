//! Runs the shared `pythia-provider` contract suite against `OllamaProvider`
//! pointed at a mocked HTTP server, proving wire-compatibility without a
//! live Ollama instance (CI-safe). Also covers the Ollama-specific
//! malformed-response case.

use std::sync::atomic::{AtomicUsize, Ordering};

use pythia_provider::contract_tests::run_provider_contract_tests;
use pythia_provider::{Message, Provider};
use pythia_provider_ollama::OllamaProvider;
use serde_json::{json, Value};
use wiremock::matchers::{method, path};
use wiremock::{Mock, MockServer, Request, Respond, ResponseTemplate};

/// Returns canned OpenAI-compatible response bodies in order, one per call
/// to `respond`, holding on the last one if called more times than
/// scripted — mirrors `MockProvider`'s "canonical 3-entry script"
/// convention from the shared contract-suite docs.
struct ScriptedResponder {
    bodies: Vec<Value>,
    calls: AtomicUsize,
}

impl ScriptedResponder {
    fn new(bodies: Vec<Value>) -> Self {
        Self {
            bodies,
            calls: AtomicUsize::new(0),
        }
    }
}

impl Respond for ScriptedResponder {
    fn respond(&self, _request: &Request) -> ResponseTemplate {
        let index = self.calls.fetch_add(1, Ordering::SeqCst);
        let body = self
            .bodies
            .get(index)
            .or_else(|| self.bodies.last())
            .cloned()
            .expect("scripted responder must have at least one body");
        ResponseTemplate::new(200).set_body_json(body)
    }
}

/// The canonical 3-entry script the shared contract suite expects from a
/// fresh provider (see `pythia_provider::contract_tests` module docs):
/// text chunk, then a tool-call chunk, then a two-chunk (text, tool-call)
/// response.
fn canonical_script_bodies() -> Vec<Value> {
    vec![
        json!({
            "choices": [{
                "message": { "role": "assistant", "content": "hello from ollama" }
            }]
        }),
        json!({
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
        }),
        json!({
            "choices": [{
                "message": {
                    "role": "assistant",
                    "content": "thinking...",
                    "tool_calls": [{
                        "id": "call-2",
                        "type": "function",
                        "function": {
                            "name": "read_file",
                            "arguments": {"path": "/tmp/example.txt"}
                        }
                    }]
                }
            }]
        }),
    ]
}

/// Starts a fresh mock server scripted with the canonical 3-entry response
/// sequence, independent of any other server the suite has already built —
/// this is what makes each `make_provider()` invocation "fresh" per the
/// contract suite's contract.
async fn start_scripted_server() -> MockServer {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(ScriptedResponder::new(canonical_script_bodies()))
        .mount(&server)
        .await;
    server
}

#[tokio::test]
async fn contract_suite_passes_against_ollama_provider_over_mocked_http() {
    // `run_provider_contract_tests` calls `make_provider()` exactly four
    // times (text-only, tool-call, multi-chunk, empty-messages scenarios).
    // Pre-start one scripted server per invocation so each fresh provider
    // gets its own script starting at index 0 — the closure itself must
    // stay synchronous, so server startup can't happen lazily inside it.
    let mut servers = Vec::new();
    for _ in 0..4 {
        servers.push(start_scripted_server().await);
    }

    let next_index = std::sync::Mutex::new(0usize);
    run_provider_contract_tests(|| {
        let mut index = next_index.lock().expect("index mutex poisoned");
        let server = &servers[*index];
        *index += 1;
        OllamaProvider::new(server.uri())
    })
    .await;
}

#[tokio::test]
async fn wire_malformed_response_body_errors_not_panics() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(200).set_body_string("not json at all"))
        .mount(&server)
        .await;

    let provider = OllamaProvider::new(server.uri());
    let result = provider.request(&[Message::user("hi")], &[]).await;

    assert!(
        result.is_err(),
        "Wire_MalformedResponseBody_ErrorsNotPanics: expected an error, got {result:?}"
    );
}

#[tokio::test]
async fn wire_http_error_status_errors_not_panics() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(500).set_body_string("internal error"))
        .mount(&server)
        .await;

    let provider = OllamaProvider::new(server.uri());
    let result = provider.request(&[Message::user("hi")], &[]).await;

    assert!(
        result.is_err(),
        "expected an error on a non-2xx response, got {result:?}"
    );
}

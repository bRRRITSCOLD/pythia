//! `pythia-provider-ollama` — the one concrete [`Provider`] implementation
//! for the vertical slice: `reqwest` + `tokio` against Ollama's
//! OpenAI-compatible `/v1/chat/completions` endpoint.
//!
//! Every Ollama-specific wire quirk is contained in the private [`wire`]
//! module (ADR-0005) — `OllamaProvider` itself only speaks
//! `pythia_provider`'s wire-agnostic types.
//!
//! # Streaming
//! `Provider::request` returns the complete, ordered `Vec<ResponseChunk>`
//! for a turn (see `pythia_provider`'s docs on why: no interactive UI to
//! stream tokens to in this headless engine). This implementation asks
//! Ollama for a single non-streamed JSON response (`stream: false`) rather
//! than collecting a chunked stream itself — the simplest thing that
//! satisfies the contract (KISS); a streaming variant can be added later
//! without changing the public interface if latency-to-first-token ever
//! matters for this engine.

mod wire;

use std::time::Duration;

use async_trait::async_trait;
use pythia_provider::{Message, Provider, ProviderError, ResponseChunk, ToolSchema};

/// The default Ollama model this slice targets.
pub const DEFAULT_MODEL: &str = "qwen3.5";

/// How long to wait for the initial TCP/TLS connection to the Ollama
/// server before giving up. Kept short — a connect that hasn't succeeded
/// in a few seconds is not going to succeed.
const DEFAULT_CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

/// The default overall request timeout (connect + send + receive full
/// response). Local inference on modest hardware can legitimately take
/// minutes for a long generation, so this is intentionally generous — a
/// short blanket timeout (e.g. 30s) would abort valid in-flight requests.
/// Without *some* bound, though, a wedged or black-holed server would hang
/// `Provider::request` forever and stall the autonomous kernel loop
/// indefinitely.
const DEFAULT_REQUEST_TIMEOUT: Duration = Duration::from_secs(10 * 60);

/// A [`Provider`] implementation backed by a local (or remote) Ollama
/// server's OpenAI-compatible `/v1/chat/completions` endpoint.
pub struct OllamaProvider {
    base_url: String,
    model: String,
    client: reqwest::Client,
}

impl OllamaProvider {
    /// Builds a provider targeting `base_url` (e.g. `http://localhost:11434`)
    /// using [`DEFAULT_MODEL`] and the default connect/request timeouts.
    pub fn new(base_url: impl Into<String>) -> Self {
        Self::with_model(base_url, DEFAULT_MODEL)
    }

    /// Builds a provider targeting `base_url` with an explicit `model`
    /// name, for callers that need a model other than [`DEFAULT_MODEL`].
    /// Uses the default connect/request timeouts.
    pub fn with_model(base_url: impl Into<String>, model: impl Into<String>) -> Self {
        Self::with_model_and_timeouts(
            base_url,
            model,
            DEFAULT_CONNECT_TIMEOUT,
            DEFAULT_REQUEST_TIMEOUT,
        )
    }

    /// Builds a provider with explicit connect and overall request
    /// timeouts, for callers that need to tune either bound (e.g. a slower
    /// remote deployment, or a smaller/faster model expected to respond
    /// quickly). A wedged or black-holed server can never hang
    /// `Provider::request` longer than `request_timeout`.
    pub fn with_model_and_timeouts(
        base_url: impl Into<String>,
        model: impl Into<String>,
        connect_timeout: Duration,
        request_timeout: Duration,
    ) -> Self {
        let client = reqwest::Client::builder()
            .connect_timeout(connect_timeout)
            .timeout(request_timeout)
            .build()
            .expect("reqwest client configuration (timeouts only) is always valid");

        Self {
            base_url: base_url.into(),
            model: model.into(),
            client,
        }
    }

    fn chat_completions_url(&self) -> String {
        format!(
            "{}/v1/chat/completions",
            self.base_url.trim_end_matches('/')
        )
    }
}

#[async_trait]
impl Provider for OllamaProvider {
    async fn request(
        &self,
        messages: &[Message],
        tools: &[ToolSchema],
    ) -> Result<Vec<ResponseChunk>, ProviderError> {
        if messages.is_empty() {
            return Err(ProviderError::EmptyMessages);
        }

        let body = wire::ChatRequest {
            model: self.model.clone(),
            messages: messages.iter().map(wire::WireMessage::from).collect(),
            tools: tools.iter().map(wire::WireTool::from).collect(),
            stream: false,
        };

        let response = self
            .client
            .post(self.chat_completions_url())
            .json(&body)
            .send()
            .await
            .map_err(|error| ProviderError::RequestFailed(error.to_string()))?;

        let status = response.status();
        let bytes = response
            .bytes()
            .await
            .map_err(|error| ProviderError::RequestFailed(error.to_string()))?;

        if !status.is_success() {
            const MAX_ERROR_BODY_LEN: usize = 512;
            let body_text = String::from_utf8_lossy(&bytes);
            let truncated = if body_text.len() > MAX_ERROR_BODY_LEN {
                // `body_text` is always valid UTF-8 (from `from_utf8_lossy`),
                // but slicing at a raw byte offset can land mid-codepoint;
                // walk back to the nearest char boundary first.
                let mut cut = MAX_ERROR_BODY_LEN;
                while cut > 0 && !body_text.is_char_boundary(cut) {
                    cut -= 1;
                }
                format!("{}... (truncated)", &body_text[..cut])
            } else {
                body_text.into_owned()
            };
            return Err(ProviderError::RequestFailed(format!(
                "ollama returned HTTP {status}: {truncated}"
            )));
        }

        let parsed: wire::ChatResponse = serde_json::from_slice(&bytes).map_err(|error| {
            ProviderError::RequestFailed(format!("malformed response body: {error}"))
        })?;

        parsed
            .into_response_chunks()
            .map_err(|error| ProviderError::RequestFailed(error.to_string()))
    }
}

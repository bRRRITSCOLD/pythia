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

use async_trait::async_trait;
use pythia_provider::{Message, Provider, ProviderError, ResponseChunk, ToolSchema};

/// The default Ollama model this slice targets.
pub const DEFAULT_MODEL: &str = "qwen3.5";

/// A [`Provider`] implementation backed by a local (or remote) Ollama
/// server's OpenAI-compatible `/v1/chat/completions` endpoint.
pub struct OllamaProvider {
    base_url: String,
    model: String,
    client: reqwest::Client,
}

impl OllamaProvider {
    /// Builds a provider targeting `base_url` (e.g. `http://localhost:11434`)
    /// using [`DEFAULT_MODEL`].
    pub fn new(base_url: impl Into<String>) -> Self {
        Self::with_model(base_url, DEFAULT_MODEL)
    }

    /// Builds a provider targeting `base_url` with an explicit `model`
    /// name, for callers that need a model other than [`DEFAULT_MODEL`].
    pub fn with_model(base_url: impl Into<String>, model: impl Into<String>) -> Self {
        Self {
            base_url: base_url.into(),
            model: model.into(),
            client: reqwest::Client::new(),
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
            return Err(ProviderError::RequestFailed(format!(
                "ollama returned HTTP {status}: {}",
                String::from_utf8_lossy(&bytes)
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

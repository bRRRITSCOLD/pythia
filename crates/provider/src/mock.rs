//! `MockProvider` — a scriptable [`Provider`] test double.
//!
//! Feature-gated behind `test-util` so it never ships in a production
//! build. Tasks 15/19/20 (kernel turn-loop tests, both demos) depend on
//! this to drive the kernel deterministically without a live model.

use std::sync::Mutex;

use async_trait::async_trait;

use crate::{Message, Provider, ProviderError, ResponseChunk, ToolSchema};

/// One scripted response: the ordered chunks `MockProvider` returns for the
/// next call to [`Provider::request`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ScriptedResponse {
    pub chunks: Vec<ResponseChunk>,
}

impl ScriptedResponse {
    pub fn text(text: impl Into<String>) -> Self {
        Self {
            chunks: vec![ResponseChunk::Text(text.into())],
        }
    }

    pub fn chunks(chunks: Vec<ResponseChunk>) -> Self {
        Self { chunks }
    }
}

/// A [`Provider`] test double that returns pre-scripted responses in order
/// and records every call it received (for assertions like "the provider
/// was called exactly once for events E1..E3, not twice").
pub struct MockProvider {
    scripted: Mutex<Vec<ScriptedResponse>>,
    calls: Mutex<Vec<Vec<Message>>>,
}

impl MockProvider {
    /// Builds a `MockProvider` that returns `scripted` responses in order,
    /// one per call to `request`.
    pub fn new(scripted: Vec<ScriptedResponse>) -> Self {
        Self {
            scripted: Mutex::new(scripted),
            calls: Mutex::new(Vec::new()),
        }
    }

    /// Number of times `request` has been called.
    pub fn call_count(&self) -> usize {
        self.calls
            .lock()
            .expect("mock provider call log poisoned")
            .len()
    }

    /// The `messages` slice passed to each call to `request`, in call order.
    pub fn calls(&self) -> Vec<Vec<Message>> {
        self.calls
            .lock()
            .expect("mock provider call log poisoned")
            .clone()
    }
}

#[async_trait]
impl Provider for MockProvider {
    async fn request(
        &self,
        messages: &[Message],
        _tools: &[ToolSchema],
    ) -> Result<Vec<ResponseChunk>, ProviderError> {
        if messages.is_empty() {
            return Err(ProviderError::EmptyMessages);
        }

        self.calls
            .lock()
            .expect("mock provider call log poisoned")
            .push(messages.to_vec());

        let mut scripted = self.scripted.lock().expect("mock provider script poisoned");
        if scripted.is_empty() {
            return Err(ProviderError::RequestFailed(
                "MockProvider: no scripted response left".to_string(),
            ));
        }
        Ok(scripted.remove(0).chunks)
    }
}

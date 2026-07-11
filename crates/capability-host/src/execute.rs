//! `ExecutionResult`: the redacted-by-construction result of a skill's `run` call (SR-5).
//!
//! `build_execution_result` is the *only* function anywhere in this crate that can produce a
//! public `ExecutionResult`. The raw bytes a skill's `run` export returns are a plain, private
//! local -- never themselves `pub`, never returned by any other function -- and every occurrence
//! of a value the host handed out via a `secret:*` capability during the same call (tracked as
//! `host_fns::secret::HandedOutSecret`) is replaced by an opaque, diagnosable marker before the
//! redacted bytes are wrapped in the public `ExecutionResult` type. There is no code path that
//! produces an `ExecutionResult` carrying a handed-out secret's plaintext: redaction isn't a
//! separate pass someone could skip, it's inside the one function capable of building the type at
//! all.
//!
//! `pythia-capability-host::execute()` (Task 9) is the intended caller once it lands: it invokes
//! a skill's `run` export, reads the raw result bytes back out of guest memory, drains the
//! `Instance`'s handed-out secrets recorded during that call, and passes both into
//! `build_execution_result` -- exactly the two inputs this function needs.

use crate::host_fns::secret::HandedOutSecret;

/// A skill call's result, guaranteed redacted of every secret value the host handed out while
/// producing it. The only way to obtain one is `build_execution_result`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExecutionResult {
    bytes: Vec<u8>,
}

impl ExecutionResult {
    /// The redacted result bytes. Still tag-prefixed per `pythia_manifest::host_fn`'s
    /// `RESULT_TAG_OK`/`RESULT_TAG_ERR` convention if that's what the raw bytes were -- this type
    /// doesn't interpret the tag, it only guarantees redaction of anything handed out as a
    /// secret.
    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes
    }
}

/// The single constructor for `ExecutionResult` (see module doc). `raw` is consumed here and
/// never returned unredacted by any path in this crate.
pub(crate) fn build_execution_result(
    raw: Vec<u8>,
    handed_out: &[HandedOutSecret],
) -> ExecutionResult {
    let mut bytes = raw;
    for secret in handed_out {
        // An empty secret value would match at every byte offset and corrupt unrelated content
        // rather than redact anything meaningful. A resolvable secret is never empty in
        // practice (`host_fns::secret::resolve_secret_value` only returns `Some` for an env var
        // that's actually set), but skipping keeps this function total either way.
        if secret.value.is_empty() {
            continue;
        }
        bytes = redact_all(&bytes, &secret.value, &marker_for(&secret.name));
    }
    ExecutionResult { bytes }
}

/// The opaque marker a handed-out secret value is replaced with -- diagnosable (names which
/// capability's value leaked into the raw output) without ever containing the plaintext itself.
fn marker_for(secret_name: &str) -> Vec<u8> {
    format!("<redacted:secret:{secret_name}>").into_bytes()
}

/// Replaces every non-overlapping occurrence of `needle` in `haystack` with `marker`.
fn redact_all(haystack: &[u8], needle: &[u8], marker: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(haystack.len());
    let mut i = 0;
    while i < haystack.len() {
        if haystack[i..].starts_with(needle) {
            out.extend_from_slice(marker);
            i += needle.len();
        } else {
            out.push(haystack[i]);
            i += 1;
        }
    }
    out
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    fn handed_out(name: &str, value: &str) -> HandedOutSecret {
        HandedOutSecret {
            name: name.to_string(),
            value: value.as_bytes().to_vec(),
        }
    }

    fn contains_subslice(haystack: &[u8], needle: &[u8]) -> bool {
        needle.is_empty()
            || haystack
                .windows(needle.len())
                .any(|window| window == needle)
    }

    #[test]
    fn ExecutionResult_ContainsHandedOutSecretValue_RedactedNotPresent() {
        let raw =
            b"auth header: Basic dXNlcg==s3cr3t-value... (plaintext embedded in skill output)"
                .to_vec();
        let handed = vec![handed_out("SMTP_PASSWORD", "s3cr3t-value")];

        let result = build_execution_result(raw, &handed);

        let bytes = result.as_bytes();
        assert!(
            !contains_subslice(bytes, b"s3cr3t-value"),
            "expected the plaintext secret value to be absent from the ExecutionResult, got {:?}",
            String::from_utf8_lossy(bytes)
        );
        assert!(
            contains_subslice(bytes, b"<redacted:secret:SMTP_PASSWORD>"),
            "expected a diagnosable redaction marker to be present, got {:?}",
            String::from_utf8_lossy(bytes)
        );
    }

    #[test]
    fn ExecutionResult_NoSecretCapabilityInvoked_UnaffectedByRedactionPass() {
        let raw = b"plain skill output, no secret capability invoked".to_vec();

        let result = build_execution_result(raw.clone(), &[]);

        assert_eq!(result.as_bytes(), raw.as_slice());
    }

    #[test]
    fn ExecutionResult_MultipleHandedOutSecrets_EachRedactedIndependently() {
        let raw = b"user=admin-user pass=hunter2-value both embedded".to_vec();
        let handed = vec![
            handed_out("ADMIN_USER", "admin-user"),
            handed_out("ADMIN_PASS", "hunter2-value"),
        ];

        let result = build_execution_result(raw, &handed);

        let bytes = result.as_bytes();
        assert!(!contains_subslice(bytes, b"admin-user"));
        assert!(!contains_subslice(bytes, b"hunter2-value"));
        assert!(contains_subslice(bytes, b"<redacted:secret:ADMIN_USER>"));
        assert!(contains_subslice(bytes, b"<redacted:secret:ADMIN_PASS>"));
    }

    #[test]
    fn ExecutionResult_HandedOutSecretNeverAppearsInRawOutput_BytesUnchangedExceptNoMatch() {
        // A skill can hold a secret grant without ever echoing the value back -- redaction must
        // be a no-op in that case, not an error or a corrupted result.
        let raw = b"skill acted on the secret but did not return it".to_vec();
        let handed = vec![handed_out("API_KEY", "never-appears-in-output")];

        let result = build_execution_result(raw.clone(), &handed);

        assert_eq!(result.as_bytes(), raw.as_slice());
    }
}

//! `execute()`: the crate's public boundary (Task 9). One function the kernel calls per tool
//! dispatch, assembling Tasks 5-8: resolve grants, build the WASI ctx + `Linker` (Task 5,
//! `wasi.rs`/`linker.rs`), configure fuel/memory/table limits (Task 7, `limits.rs`), instantiate,
//! stage `args` into the skill's own linear memory and call its `run` export, then build the
//! redacted-by-construction `ExecutionResult` (Task 8, this module). Every `HostError` variant
//! `CapabilityHost::instantiate`/`Instance::call_i32` can produce is mapped here to exactly one of
//! `ExecutionResult`'s three `status` values -- there is no fourth status and no path that leaves
//! a caller needing to interpret raw error text or a `Result` to know what happened.
//!
//! `ExecutionResult`: the redacted-by-construction result of a skill's `run` call (SR-5).
//!
//! `build_ok_result` is the *only* function anywhere in this crate that can produce a public
//! `ExecutionResult` with `status: Ok`. The raw bytes a skill's `run` export returns are a plain,
//! private local -- never themselves `pub`, never returned by any other function -- and every
//! occurrence of a value the host handed out via a `secret:*` capability during the same call
//! (tracked as `host_fns::secret::HandedOutSecret`) is replaced by an opaque, diagnosable marker
//! before the redacted bytes are wrapped in the public `ExecutionResult` type. There is no code
//! path that produces an `ExecutionResult` carrying a handed-out secret's plaintext: redaction
//! isn't a separate pass someone could skip, it's inside the one function capable of building an
//! `Ok`-status result at all. `Denied`/`ResourceLimitExceeded` results never touch a skill's raw
//! output (the skill's `run` export was never reached, or its result was never trusted), so they
//! carry only a diagnostic reason string -- nothing to redact.

use pythia_manifest::{PolicyFile, SkillManifest};

use crate::host_fns::secret::HandedOutSecret;
use crate::{CapabilityHost, HostError};

/// The outcome of a skill call, as the kernel needs to distinguish it: did it run to completion
/// (`Ok`), was it refused before any host function or the skill's own `run` export could execute
/// (`Denied` -- SR-2's mechanism, surfaced here), or did it get force-terminated for exceeding its
/// fuel/memory/table budget (`ResourceLimitExceeded` -- SR-6, kept distinct from `Denied` so the
/// kernel's event log can tell "refused" apart from "ran, then was killed").
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ExecutionStatus {
    Ok,
    Denied,
    ResourceLimitExceeded,
}

/// A skill call's result, guaranteed redacted of every secret value the host handed out while
/// producing it (when `status` is `Ok`; `Denied`/`ResourceLimitExceeded` results never contained
/// a skill's raw output to begin with). The only way to obtain one is `execute()`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExecutionResult {
    status: ExecutionStatus,
    bytes: Vec<u8>,
    tainted: bool,
}

impl ExecutionResult {
    pub fn status(&self) -> ExecutionStatus {
        self.status
    }

    /// The redacted result bytes. On `Ok`, still tag-prefixed per `pythia_manifest::host_fn`'s
    /// `RESULT_TAG_OK`/`RESULT_TAG_ERR` convention if that's what the skill's raw bytes were --
    /// this type doesn't interpret the tag, it only guarantees redaction of anything handed out
    /// as a secret. On `Denied`/`ResourceLimitExceeded`, a UTF-8 diagnostic reason string, not a
    /// skill-produced payload.
    pub fn as_bytes(&self) -> &[u8] {
        &self.bytes
    }

    /// Whether this result's content should be treated as tainted (came from, or was influenced
    /// by, untrusted input) by anything downstream that consumes it -- the event log, the
    /// provider context, CLI rendering. `execute()`'s caller sets this from the skill's own
    /// manifest-declared taint class (e.g. `read-file`'s output is unconditionally tainted per
    /// the spec's Unit 3 invariant); this crate has no opinion of its own on which skills produce
    /// tainted output and never infers it from the bytes themselves.
    pub fn is_tainted(&self) -> bool {
        self.tainted
    }
}

/// Assembles Tasks 5-8 into the one call the kernel makes per tool dispatch. Never panics and
/// never returns a `Result`: every failure mode this crate can produce -- an ungranted capability
/// (`HostError::CapabilityDenied`), a fuel/memory/table ceiling hit (`HostError::ResourceLimitExceeded`),
/// or any other sandbox failure (malformed wasm, a WASI context that failed to build, a missing
/// `run`/`pythia_alloc` export -- `HostError::Wasmtime`) -- is folded into one of
/// `ExecutionResult`'s three `status` values before this function returns, so the kernel's
/// dispatch step never needs to match on this crate's internal error type at all.
///
/// `tainted` is threaded straight through to the returned `ExecutionResult` regardless of
/// `status` -- see `ExecutionResult::is_tainted`'s doc for why this crate takes it as an input
/// rather than inferring it.
pub fn execute(
    module_bytes: &[u8],
    manifest: &SkillManifest,
    policy: &PolicyFile,
    args: &[u8],
    tainted: bool,
) -> ExecutionResult {
    let host = match CapabilityHost::new() {
        Ok(host) => host,
        Err(err) => return denied(format!("engine construction failed: {err}"), tainted),
    };

    let mut instance = match host.instantiate(module_bytes, manifest, policy) {
        Ok(instance) => instance,
        Err(err) => return result_for_host_error(err, tainted),
    };

    match instance.call_run(args) {
        Ok(raw) => instance.into_execution_result(raw, tainted),
        Err(err) => result_for_host_error(err, tainted),
    }
}

/// Maps every `HostError` variant to the matching `ExecutionResult::status`. `CapabilityDenied`
/// and `ResourceLimitExceeded` map to their own eponymous status, exactly as named -- the whole
/// point of keeping them distinct `HostError` variants (Tasks 5, 7) was so this mapping could be
/// this direct. `HostError::Wasmtime` (any other sandbox failure) is the fail-closed default: this
/// crate only has three statuses, `Ok` is never appropriate for a call that didn't produce a
/// trusted skill result, and treating an unrecognized failure as `ResourceLimitExceeded` would
/// misattribute it to a resource ceiling it may never have touched -- `Denied` is the honest
/// default for "did not complete, for a reason that isn't confirmed to be the resource ceiling."
fn result_for_host_error(err: HostError, tainted: bool) -> ExecutionResult {
    match err {
        HostError::CapabilityDenied(import) => denied(
            format!(
                "capability denied: import `{}` was not granted",
                sanitize_reason_fragment(&import)
            ),
            tainted,
        ),
        HostError::ResourceLimitExceeded(reason) => resource_limit_exceeded(reason, tainted),
        HostError::Wasmtime(err) => denied(
            format!(
                "execution failed: {}",
                sanitize_reason_fragment(&err.to_string())
            ),
            tainted,
        ),
    }
}

/// The maximum length, in bytes, of a guest-derived fragment folded into a `Denied` reason
/// string. Bounds the amount of guest-controlled text the kernel ends up persisting and
/// rendering per denial -- a guest can pick an arbitrarily long import name or trigger an error
/// whose `Display` output is unbounded, and without this cap that text flows straight into a
/// public `ExecutionResult` (see module doc: `CapabilityDenied`/`Wasmtime` are the only two
/// `HostError` variants that lift guest-controlled bytes into a reason string).
const MAX_REASON_FRAGMENT: usize = 256;

/// Marker appended when a guest-derived fragment is truncated for exceeding `MAX_REASON_FRAGMENT`.
const TRUNCATION_MARKER: &str = "…(truncated)";

/// Placeholder substituted for each control/non-printable byte a guest-derived fragment contains
/// (anything below `0x20`, `0x7f` DEL, and the ANSI CSI introducer `ESC [`) so a malicious import
/// name or error string can't inject terminal escape sequences or embedded newlines into a
/// rendered/persisted reason string.
const CONTROL_BYTE_PLACEHOLDER: &str = "\u{fffd}";

/// Sanitizes a guest-controlled fragment before it is folded into a `Denied` reason string: caps
/// its length (truncating with `TRUNCATION_MARKER`) and replaces control/non-printable bytes with
/// a visible placeholder so guest-chosen bytes can never inject raw control sequences (e.g. ANSI
/// CSI) or blow out the size of a persisted/rendered `ExecutionResult`. Fixed, host-authored
/// prefixes ("capability denied: import …", "execution failed: …") are never passed through this
/// function -- only the guest-derived fragment interpolated into them is.
fn sanitize_reason_fragment(fragment: &str) -> String {
    let truncated: String = fragment.chars().take(MAX_REASON_FRAGMENT).collect();
    let was_truncated = fragment.chars().count() > MAX_REASON_FRAGMENT;

    let mut sanitized = String::with_capacity(truncated.len());
    let mut chars = truncated.chars().peekable();
    while let Some(ch) = chars.next() {
        let is_control_or_del = (ch as u32) < 0x20 || ch == '\u{7f}';
        let is_csi_introducer = ch == '\u{1b}' && chars.peek() == Some(&'[');
        if is_control_or_del || is_csi_introducer {
            sanitized.push_str(CONTROL_BYTE_PLACEHOLDER);
            if is_csi_introducer {
                // Consume the `[` too so the CSI introducer collapses to one placeholder rather
                // than a placeholder followed by a literal `[`.
                chars.next();
            }
        } else {
            sanitized.push(ch);
        }
    }

    if was_truncated {
        sanitized.push_str(TRUNCATION_MARKER);
    }
    sanitized
}

fn denied(reason: String, tainted: bool) -> ExecutionResult {
    ExecutionResult {
        status: ExecutionStatus::Denied,
        bytes: reason.into_bytes(),
        tainted,
    }
}

fn resource_limit_exceeded(reason: String, tainted: bool) -> ExecutionResult {
    ExecutionResult {
        status: ExecutionStatus::ResourceLimitExceeded,
        bytes: reason.into_bytes(),
        tainted,
    }
}

/// The single constructor for an `Ok`-status `ExecutionResult` (see module doc). `raw` is
/// consumed here and never returned unredacted by any path in this crate.
pub(crate) fn build_ok_result(
    raw: Vec<u8>,
    handed_out: &[HandedOutSecret],
    tainted: bool,
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
    ExecutionResult {
        status: ExecutionStatus::Ok,
        bytes,
        tainted,
    }
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

        let result = build_ok_result(raw, &handed, false);

        assert_eq!(result.status(), ExecutionStatus::Ok);
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

        let result = build_ok_result(raw.clone(), &[], false);

        assert_eq!(result.status(), ExecutionStatus::Ok);
        assert_eq!(result.as_bytes(), raw.as_slice());
    }

    #[test]
    fn ExecutionResult_MultipleHandedOutSecrets_EachRedactedIndependently() {
        let raw = b"user=admin-user pass=hunter2-value both embedded".to_vec();
        let handed = vec![
            handed_out("ADMIN_USER", "admin-user"),
            handed_out("ADMIN_PASS", "hunter2-value"),
        ];

        let result = build_ok_result(raw, &handed, false);

        assert_eq!(result.status(), ExecutionStatus::Ok);
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

        let result = build_ok_result(raw.clone(), &handed, false);

        assert_eq!(result.status(), ExecutionStatus::Ok);
        assert_eq!(result.as_bytes(), raw.as_slice());
    }

    #[test]
    fn Denied_GuestImportNameWithControlBytes_SanitizedInReason() {
        let import = "evil\u{1b}[2Jmodule\n::\0name".to_string();

        let result = result_for_host_error(HostError::CapabilityDenied(import), false);

        assert_eq!(result.status(), ExecutionStatus::Denied);
        let bytes = result.as_bytes();
        assert!(
            !contains_subslice(bytes, b"\x1b[2J"),
            "expected raw ANSI CSI sequence to be absent, got {:?}",
            String::from_utf8_lossy(bytes)
        );
        assert!(
            !contains_subslice(bytes, b"\n"),
            "expected raw newline to be absent, got {:?}",
            String::from_utf8_lossy(bytes)
        );
        assert!(
            !contains_subslice(bytes, b"\0"),
            "expected raw NUL byte to be absent, got {:?}",
            String::from_utf8_lossy(bytes)
        );
        assert!(
            contains_subslice(bytes, CONTROL_BYTE_PLACEHOLDER.as_bytes()),
            "expected a visible placeholder for the stripped control bytes, got {:?}",
            String::from_utf8_lossy(bytes)
        );
    }

    #[test]
    fn Denied_OversizedGuestReason_LengthBounded() {
        let import = "a".repeat(MAX_REASON_FRAGMENT * 4);

        let result = result_for_host_error(HostError::CapabilityDenied(import), false);

        assert_eq!(result.status(), ExecutionStatus::Denied);
        let bytes = result.as_bytes();
        assert!(
            bytes.len() < MAX_REASON_FRAGMENT * 4,
            "expected the oversized guest fragment to be truncated, got length {}",
            bytes.len()
        );
        assert!(
            contains_subslice(bytes, TRUNCATION_MARKER.as_bytes()),
            "expected the truncation marker to be present, got {:?}",
            String::from_utf8_lossy(bytes)
        );
    }

    #[test]
    fn ExecutionResult_TaintedFlag_PassesThroughUnchangedRegardlessOfRedaction() {
        let raw = b"some output".to_vec();

        let untainted = build_ok_result(raw.clone(), &[], false);
        let tainted = build_ok_result(raw, &[], true);

        assert!(!untainted.is_tainted());
        assert!(tainted.is_tainted());
    }
}

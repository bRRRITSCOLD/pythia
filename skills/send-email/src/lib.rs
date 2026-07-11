//! `send-email` — the safety-demo skill (plan Task 13).
//!
//! Requests the `net:smtp` and `secret:SMTP_PASSWORD` capabilities so that
//! its compiled `wasm32-wasip1` module has real import-table entries for
//! `net_smtp_send` and `secret_get` — the precondition for the safety
//! demo's (Task 18, SR-2) import-absence assertion to test something real
//! against an actual module, not a synthetic WAT fixture.
//!
//! `pythia-skill-sdk` (issue #11) has not landed yet, so this skill
//! declares its own minimal manifest and host-function bindings inline
//! rather than depending on it. Once the SDK exists this should be
//! rewritten to use it — that is a follow-up, not a blocker for this
//! skill's own contract (a real module with the two imports referenced).

/// The manifest this skill would hand the capability host: the flat
/// capability-string vocabulary from
/// `docs/superpowers/specs/2026-07-10-pythia-engine-design.md`.
///
/// Kept as a plain constant (not a `pythia-manifest` type) because that
/// crate (Task 2) has not landed yet either; the capability strings match
/// its documented vocabulary so wiring this up later is a type change, not
/// a semantic one.
pub const MANIFEST_JSON: &str = r#"{
  "name": "send-email",
  "version": "0.1.0",
  "capabilities": ["net:smtp", "secret:SMTP_PASSWORD"]
}"#;

/// The name under which this skill asks the host for the SMTP credential.
pub const SMTP_PASSWORD_SECRET: &str = "SMTP_PASSWORD";

#[derive(Debug, PartialEq, Eq)]
pub struct SendEmailArgs {
    pub recipient: String,
    pub body: String,
}

#[derive(Debug, PartialEq, Eq)]
pub struct ParseArgsError(pub String);

impl std::fmt::Display for ParseArgsError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "send-email: invalid args: {}", self.0)
    }
}

impl std::error::Error for ParseArgsError {}

/// Parses the `{"recipient": "...", "body": "..."}` payload the host
/// passes into `run`. Pure and target-independent, so it is tested
/// directly on the native target rather than through a wasmtime
/// round-trip (same rationale as the `read-file` skill, Task 12).
pub fn parse_args(json: &str) -> Result<SendEmailArgs, ParseArgsError> {
    let value: serde_json::Value =
        serde_json::from_str(json).map_err(|e| ParseArgsError(e.to_string()))?;

    let recipient = value
        .get("recipient")
        .and_then(|v| v.as_str())
        .ok_or_else(|| ParseArgsError("missing \"recipient\" string field".to_string()))?
        .to_string();

    let body = value
        .get("body")
        .and_then(|v| v.as_str())
        .ok_or_else(|| ParseArgsError("missing \"body\" string field".to_string()))?
        .to_string();

    Ok(SendEmailArgs { recipient, body })
}

/// Host-function bindings. Only meaningful for the `wasm32` target: this
/// is what makes the compiled module actually import `secret_get` and
/// `net_smtp_send`, which is the entire point of this skill existing (see
/// module docs). The exact ABI (pointer/length passing, status encoding)
/// is provisional pending the capability host (Task 5/6/8) and skill SDK
/// (Task 11) — it exists here only so `run` has something real to call.
#[cfg(target_arch = "wasm32")]
mod host {
    #[link(wasm_import_module = "pythia")]
    extern "C" {
        /// Fetches the plaintext value of a granted secret into the
        /// caller-owned buffer. Returns the value's length on success, or
        /// a negative status code (e.g. capability not granted).
        pub fn secret_get(
            name_ptr: *const u8,
            name_len: usize,
            out_ptr: *mut u8,
            out_cap: usize,
        ) -> i32;

        /// Sends an email via the host's SMTP stub. Returns 0 on success,
        /// or a negative status code (e.g. capability not granted).
        pub fn net_smtp_send(
            recipient_ptr: *const u8,
            recipient_len: usize,
            body_ptr: *const u8,
            body_len: usize,
        ) -> i32;
    }
}

/// The skill's entry point, called by the capability host with the raw
/// argument bytes from `pythia_manifest`-validated call input. Parses
/// args, fetches the SMTP credential, then calls the SMTP stub — the two
/// calls whose presence in the compiled import table is what the safety
/// demo asserts on.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub extern "C" fn run(args_ptr: *const u8, args_len: usize) -> i32 {
    let bytes = unsafe { std::slice::from_raw_parts(args_ptr, args_len) };
    let json = match std::str::from_utf8(bytes) {
        Ok(s) => s,
        Err(_) => return -1,
    };
    let args = match parse_args(json) {
        Ok(a) => a,
        Err(_) => return -1,
    };

    let mut secret_buf = [0u8; 256];
    let secret_len = unsafe {
        host::secret_get(
            SMTP_PASSWORD_SECRET.as_ptr(),
            SMTP_PASSWORD_SECRET.len(),
            secret_buf.as_mut_ptr(),
            secret_buf.len(),
        )
    };
    if secret_len < 0 {
        return secret_len;
    }

    unsafe {
        host::net_smtp_send(
            args.recipient.as_ptr(),
            args.recipient.len(),
            args.body.as_ptr(),
            args.body.len(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_args_valid_recipient_and_body_json_extracts_both() {
        let json = r#"{"recipient": "ops@example.com", "body": "hello"}"#;

        let args = parse_args(json).expect("valid payload should parse");

        assert_eq!(
            args,
            SendEmailArgs {
                recipient: "ops@example.com".to_string(),
                body: "hello".to_string(),
            }
        );
    }

    #[test]
    fn parse_args_missing_recipient_returns_error() {
        let json = r#"{"body": "hello"}"#;

        let result = parse_args(json);

        assert!(result.is_err());
    }

    #[test]
    fn parse_args_missing_body_returns_error() {
        let json = r#"{"recipient": "ops@example.com"}"#;

        let result = parse_args(json);

        assert!(result.is_err());
    }

    #[test]
    fn parse_args_malformed_json_returns_error() {
        let json = "not json";

        let result = parse_args(json);

        assert!(result.is_err());
    }

    #[test]
    fn manifest_json_declares_net_smtp_and_secret_capabilities() {
        let value: serde_json::Value =
            serde_json::from_str(MANIFEST_JSON).expect("manifest constant must be valid JSON");
        let capabilities: Vec<&str> = value["capabilities"]
            .as_array()
            .expect("capabilities must be an array")
            .iter()
            .map(|v| v.as_str().expect("capability entries must be strings"))
            .collect();

        assert!(capabilities.contains(&"net:smtp"));
        assert!(capabilities.contains(&"secret:SMTP_PASSWORD"));
    }
}

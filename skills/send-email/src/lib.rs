//! `send-email` — the safety-demo skill (plan Task 13). Requests `net:smtp` and
//! `secret:SMTP_PASSWORD` so its compiled `wasm32-wasip1` module has real import-table entries
//! for `net_smtp_send` and `secret_get` — the precondition for the safety demo's (Task 18, SR-2)
//! import-absence assertion to test something real against an actual module, not a synthetic WAT
//! fixture.
//!
//! # `run` export ABI
//!
//! Same calling convention as the `read-file` skill (Task 12): the host obtains a guest-owned
//! buffer via this module's exported `pythia_alloc(len) -> *mut u8` (re-exported from
//! `pythia-skill-sdk`), writes the UTF-8 JSON argument bytes (`{"recipient": "...", "body":
//! "..."}`) into it, then calls `run(args_ptr, args_len, out_len_ptr) -> *mut u8`. The returned
//! pointer plus the `usize` written to `out_len_ptr` describe a buffer allocated by this module's
//! own `pythia_alloc`, so the caller can hand it back to `pythia_skill_sdk::result::decode_result`
//! after reading it out of linear memory.

use pythia_skill_sdk::{declare_manifest, err_result};
#[cfg(target_arch = "wasm32")]
use pythia_skill_sdk::{net_smtp_send, ok_result, secret_get};

declare_manifest! {
    name: "send-email",
    requested: ["net:smtp", "secret:SMTP_PASSWORD"],
}

/// The name under which this skill asks the host for the SMTP credential.
pub const SMTP_PASSWORD_SECRET: &str = "SMTP_PASSWORD";

#[derive(Debug, PartialEq, Eq)]
pub struct SendEmailArgs {
    pub recipient: String,
    pub body: String,
}

/// Parses the skill's JSON argument payload (`{"recipient": "...", "body": "..."}`). Pure and
/// target-independent, so it's unit-testable on the host target without a wasm runtime — the
/// wasm-specific glue (`secret_get`/`net_smtp_send`) is exercised through
/// `pythia-capability-host`'s own integration tests instead (same rationale as the `read-file`
/// skill, Task 12).
fn parse_args(json: &str) -> Result<SendEmailArgs, String> {
    #[derive(serde::Deserialize)]
    struct Args {
        recipient: String,
        body: String,
    }

    serde_json::from_str::<Args>(json)
        .map(|args| SendEmailArgs {
            recipient: args.recipient,
            body: args.body,
        })
        .map_err(|e| format!("send-email: invalid args: {e}"))
}

/// Parses `json`, fetches the granted `secret:SMTP_PASSWORD` credential, calls the granted
/// `net:smtp` capability with the recipient/body, and returns the tag-prefixed result bytes
/// (`ok_result`/`err_result`) `decode_result` expects. Argument-parse failures are reported as
/// `err_result`, never a panic — the argument payload is untrusted input, not a programmer
/// error.
fn run_impl(json: &str) -> Vec<u8> {
    match parse_args(json) {
        Ok(args) => {
            #[cfg(target_arch = "wasm32")]
            {
                let _password = secret_get(SMTP_PASSWORD_SECRET);
                let message = format!("To: {}\r\n\r\n{}", args.recipient, args.body);
                ok_result(&net_smtp_send(message.as_bytes()))
            }
            #[cfg(not(target_arch = "wasm32"))]
            {
                let _ = args;
                err_result(
                    "send-email: secret_get/net_smtp_send are only callable when compiled for wasm32",
                )
            }
        }
        Err(message) => err_result(&message),
    }
}

/// Host-callable export. See the module-level ABI doc for the calling convention.
///
/// # Safety
///
/// `args_ptr` must point to `args_len` valid, initialized bytes in this instance's linear memory
/// (UTF-8 JSON), and `out_len_ptr` must point to a valid, writable `usize` slot the caller
/// supplied for this call. Both are the caller's (the host's) responsibility to uphold, matching
/// the same contract `pythia-skill-sdk::imports` documents for the reverse direction. `args_ptr`
/// is null-checked before use: a null pointer (with any `args_len`) is treated as an empty
/// argument payload rather than dereferenced.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub unsafe extern "C" fn run(
    args_ptr: *const u8,
    args_len: usize,
    out_len_ptr: *mut usize,
) -> *mut u8 {
    let args = if args_ptr.is_null() {
        String::new()
    } else {
        std::str::from_utf8(std::slice::from_raw_parts(args_ptr, args_len))
            .map(|s| s.to_string())
            .unwrap_or_default()
    };

    let mut result = run_impl(&args);
    result.shrink_to_fit();
    let ptr = result.as_mut_ptr();
    *out_len_ptr = result.len();
    std::mem::forget(result);
    ptr
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;
    use pythia_skill_sdk::{decode_result, SkillResult};

    #[test]
    fn ParseArgs_ValidRecipientAndBodyJson_ExtractsBoth() {
        let args = parse_args(r#"{"recipient": "ops@example.com", "body": "hello"}"#)
            .expect("valid payload should parse");

        assert_eq!(
            args,
            SendEmailArgs {
                recipient: "ops@example.com".to_string(),
                body: "hello".to_string(),
            }
        );
    }

    #[test]
    fn ParseArgs_MissingRecipientField_ErrorsNotPanics() {
        let result = parse_args(r#"{"body": "hello"}"#);

        assert!(result.is_err());
    }

    #[test]
    fn ParseArgs_MissingBodyField_ErrorsNotPanics() {
        let result = parse_args(r#"{"recipient": "ops@example.com"}"#);

        assert!(result.is_err());
    }

    #[test]
    fn ParseArgs_MalformedJson_ErrorsNotPanics() {
        let result = parse_args("not json");

        assert!(result.is_err());
    }

    #[test]
    fn RunImpl_MalformedArgs_ReturnsErrTaggedResult() {
        let encoded = run_impl("not json");

        match decode_result(&encoded) {
            Some(SkillResult::Err(_)) => {}
            other => panic!("expected an Err-tagged result, got {other:?}"),
        }
    }

    #[test]
    fn SkillManifestToml_DeclaresNetSmtpAndSecretCapabilities() {
        let toml_src = skill_manifest_toml();
        let manifest: pythia_manifest::SkillManifest =
            toml::from_str(&toml_src).expect("round-trips through pythia-manifest's own parser");

        assert_eq!(manifest.name, "send-email");
        assert!(manifest
            .requested
            .contains(&pythia_manifest::Capability::Net("smtp".to_string())));
        assert!(manifest
            .requested
            .contains(&pythia_manifest::Capability::Secret(
                "SMTP_PASSWORD".to_string()
            )));
    }
}

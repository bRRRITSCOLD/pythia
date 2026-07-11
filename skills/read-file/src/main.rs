//! `read-file` skill: the durability demo's skill (Task 17). Requests `fs:read:/notes` only —
//! no `net`, no `secret` — the minimal skill needed to prove replay correctness: one recorded
//! effect, nothing more.
//!
//! # `run` export ABI
//!
//! `pythia-capability-host::execute()` (Task 9) calls this module the same way this module's own
//! `pythia-skill-sdk::imports` calls the host: the caller obtains a guest-owned buffer via this
//! module's exported `pythia_alloc(len) -> *mut u8` (re-exported from `pythia-skill-sdk`), writes
//! the UTF-8 JSON argument bytes (`{"path": "..."}`) into it, then calls
//! `run(args_ptr, args_len, out_len_ptr) -> *mut u8`. The returned pointer plus the `usize`
//! written to `out_len_ptr` describe a buffer allocated by this module's own `pythia_alloc`
//! (never a scratch region the host owns), so the caller can hand it back to
//! `pythia_skill_sdk::result::decode_result` after reading it out of linear memory — the payload
//! is tag-prefixed via `ok_result`/`err_result`, exactly like a host import's result shape, just
//! with the roles reversed (this module is the allocator and the tag-writer here, not the host).

#[cfg(target_arch = "wasm32")]
use pythia_skill_sdk::ok_result;
use pythia_skill_sdk::{declare_manifest, err_result};

declare_manifest! {
    name: "read-file",
    requested: ["fs:read:/notes"],
}

/// Parses the skill's JSON argument payload (`{"path": "..."}`), extracting the requested path.
/// Pure and target-independent, so it's unit-testable on the host target without a wasm
/// runtime — the wasm-specific glue (`fs_read` itself) is exercised through
/// `pythia-capability-host`'s own integration tests instead (see the plan's Task 12 test list).
fn parse_args(json: &str) -> Result<String, String> {
    #[derive(serde::Deserialize)]
    struct Args {
        path: String,
    }

    serde_json::from_str::<Args>(json)
        .map(|args| args.path)
        .map_err(|e| format!("read-file: invalid args: {e}"))
}

/// Parses `json`, calls the granted `fs:read:/notes` capability for the requested path, and
/// returns the tag-prefixed result bytes (`ok_result`/`err_result`) `decode_result` expects.
/// Argument-parse failures and a missing/denied capability (an absent `fs_read` import makes the
/// call itself a link-time error the host handles before this function ever runs, per
/// `pythia-capability-host`'s import-absence semantics) are both reported as `err_result`, never
/// a panic — an adversarial or malformed argument payload is untrusted input, not a programmer
/// error.
fn run_impl(json: &str) -> Vec<u8> {
    match parse_args(json) {
        Ok(path) => {
            #[cfg(target_arch = "wasm32")]
            {
                ok_result(&pythia_skill_sdk::fs_read(&path))
            }
            #[cfg(not(target_arch = "wasm32"))]
            {
                let _ = path;
                err_result("read-file: fs_read is only callable when compiled for wasm32")
            }
        }
        Err(message) => err_result(&message),
    }
}

/// WASI reactor entry point required by the `wasm32-wasip1` command-module convention; unused —
/// the host calls the `run` export below directly rather than invoking `_start`.
fn main() {}

/// Host-callable export. See the module-level ABI doc for the calling convention.
///
/// # Safety
///
/// `args_ptr` must point to `args_len` valid, initialized bytes in this instance's linear memory
/// (UTF-8 JSON), and `out_len_ptr` must point to a valid, writable `usize` slot the caller
/// supplied for this call. Both are the caller's (the host's) responsibility to uphold, matching
/// the same contract `pythia-skill-sdk::imports` documents for the reverse direction.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub unsafe extern "C" fn run(
    args_ptr: *const u8,
    args_len: usize,
    out_len_ptr: *mut usize,
) -> *mut u8 {
    let args = std::str::from_utf8(std::slice::from_raw_parts(args_ptr, args_len))
        .map(|s| s.to_string())
        .unwrap_or_default();

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
    fn ParseArgs_ValidPathJson_ExtractsPath() {
        let path = parse_args(r#"{"path": "/notes"}"#).expect("valid JSON parses");

        assert_eq!(path, "/notes");
    }

    #[test]
    fn ParseArgs_MissingPathField_ErrorsNotPanics() {
        let result = parse_args(r#"{"not_path": "/notes"}"#);

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
}

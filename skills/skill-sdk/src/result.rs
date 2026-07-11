//! Byte-shape helpers for a skill's return value.
//!
//! A skill's `run` export hands its result back to the host as raw bytes; the capability
//! host's `execute()` (Task 9) is the reader on the other end. The shape is a one-byte tag
//! followed by the payload: `pythia_manifest::host_fn::RESULT_TAG_OK` + arbitrary bytes for
//! success, `RESULT_TAG_ERR` + a UTF-8 message for failure. Kept intentionally minimal — no
//! length prefix needed, since the host already knows the total byte count from the wasm call's
//! return. The tag bytes themselves live in `pythia-manifest::host_fn` (not here, and not
//! re-derived by the host's decoder) — the same shared-constants pattern this crate's `imports`
//! module uses for host function names, so the two workspaces can't drift apart silently.

use pythia_manifest::host_fn::{RESULT_TAG_ERR as TAG_ERR, RESULT_TAG_OK as TAG_OK};

/// A decoded skill result, the inverse of `ok_result`/`err_result`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SkillResult {
    Ok(Vec<u8>),
    Err(String),
}

/// Encodes a successful result: the tag byte followed by `payload` unchanged.
pub fn ok_result(payload: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(1 + payload.len());
    out.push(TAG_OK);
    out.extend_from_slice(payload);
    out
}

/// Encodes a failed result: the tag byte followed by `message`'s UTF-8 bytes.
pub fn err_result(message: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity(1 + message.len());
    out.push(TAG_ERR);
    out.extend_from_slice(message.as_bytes());
    out
}

/// Decodes bytes produced by `ok_result`/`err_result`. Returns `None` for an empty slice or an
/// unrecognized tag byte — malformed input is a data error here, not a panic.
pub fn decode_result(bytes: &[u8]) -> Option<SkillResult> {
    let (&tag, payload) = bytes.split_first()?;
    match tag {
        TAG_OK => Some(SkillResult::Ok(payload.to_vec())),
        TAG_ERR => std::str::from_utf8(payload)
            .ok()
            .map(|s| SkillResult::Err(s.to_string())),
        _ => None,
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    #[test]
    fn ResultEncode_OkPayload_DecodesBackToOriginalBytes() {
        let payload = b"hello wasm".to_vec();

        let encoded = ok_result(&payload);
        let decoded = decode_result(&encoded).expect("valid encoding decodes");

        assert_eq!(decoded, SkillResult::Ok(payload));
    }

    #[test]
    fn ResultEncode_ErrMessage_DecodesBackToOriginalMessage() {
        let message = "granted scope mismatch";

        let encoded = err_result(message);
        let decoded = decode_result(&encoded).expect("valid encoding decodes");

        assert_eq!(decoded, SkillResult::Err(message.to_string()));
    }

    #[test]
    fn DecodeResult_EmptyBytes_ReturnsNone() {
        assert_eq!(decode_result(&[]), None);
    }

    #[test]
    fn DecodeResult_UnknownTag_ReturnsNoneNotPanic() {
        assert_eq!(decode_result(&[0xFF, 1, 2, 3]), None);
    }

    #[test]
    fn DecodeResult_ErrTagInvalidUtf8_ReturnsNone() {
        let invalid_utf8 = [TAG_ERR, 0xFF, 0xFE];

        assert_eq!(decode_result(&invalid_utf8), None);
    }
}

//! `pythia-skill-sdk`: skill-side bindings — declare a manifest, call granted host imports
//! ergonomically, and encode a skill's return value — without hand-rolling `extern "C"` blocks
//! per skill.

pub mod imports;
pub mod manifest;
pub mod result;

#[cfg(target_arch = "wasm32")]
pub use imports::{fs_read, net_smtp_send, secret_get};
pub use result::{decode_result, err_result, ok_result, SkillResult};

//! `pythia-manifest`: capability identifier vocabulary, skill manifest schema (request),
//! policy schema (authority), and the pure fail-closed resolution function that turns the two
//! into what actually gets linked into a skill's sandbox. Zero wasmtime dependency — fully
//! unit-testable without a sandbox.

mod capability;
mod manifest;
mod policy;
mod resolve;

pub use capability::{Capability, CapabilityParseError};
pub use manifest::SkillManifest;
pub use policy::{Decision, PolicyFile};
pub use resolve::{resolve, ResolvedGrants};

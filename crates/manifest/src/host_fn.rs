//! Shared host-function import names.
//!
//! `pythia-capability-host` (Tasks 5-8) registers these names on its wasmtime `Linker`; the
//! skills workspace's `pythia-skill-sdk` (Task 11) declares `extern "C"` imports with matching
//! names. The two workspaces don't share a build dependency (the skills workspace targets
//! `wasm32-wasip1` and never links against `wasmtime`), so this module is the single source of
//! truth both sides reference by convention to keep the names from drifting apart silently.

/// The wasm import module namespace every host function is registered under.
pub const WASM_IMPORT_MODULE: &str = "pythia";

/// Reads a granted file path. Backs `fs:read:<path>` capabilities.
pub const FS_READ: &str = "fs_read";

/// Sends an email via SMTP. Backs `net:smtp` capabilities.
pub const NET_SMTP_SEND: &str = "net_smtp_send";

/// Resolves a granted secret by name. Backs `secret:<name>` capabilities.
pub const SECRET_GET: &str = "secret_get";

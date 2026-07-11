//! Builds a `Linker<HostState>` whose *only* imports are (a) the standard WASI preview1 surface
//! (always present — every wasm32-wasip1 module needs it to be a valid module, its authority is
//! what `wasi.rs` scopes down to zero-by-default) and (b) one placeholder slot per capability
//! actually present in `grants.granted`.
//!
//! No host function has a real body yet — that lands per-capability in Tasks 6 (`fs_read`), 7
//! (limits, not an import), 8 (`secret_get`). What this task proves is the *shape*: a capability
//! that isn't granted has no import slot at all, so a module that references it fails
//! instantiation with wasmtime's own "unknown import" error — not a runtime permission check
//! inside a host function that could be forgotten or bypassed.

use std::collections::HashSet;

use anyhow::Result;
use pythia_manifest::{Capability, ResolvedGrants};
use wasmtime::{Caller, Engine, Linker};

use crate::HostState;

/// Import module namespace for Pythia's own host functions, distinct from
/// `wasi_snapshot_preview1`.
pub(crate) const HOST_MODULE: &str = "pythia_host";

pub(crate) fn build_linker(engine: &Engine, grants: &ResolvedGrants) -> Result<Linker<HostState>> {
    let mut linker = Linker::new(engine);

    wasmtime_wasi::preview1::add_to_linker_sync(&mut linker, |state: &mut HostState| {
        &mut state.wasi
    })?;

    let mut registered = HashSet::new();
    for capability in &grants.granted {
        let Some(import_name) = import_name_for(capability) else {
            continue;
        };
        if registered.insert(import_name.clone()) {
            linker.func_wrap(HOST_MODULE, import_name.as_str(), placeholder)?;
        }
    }

    Ok(linker)
}

/// Maps a granted capability to the import name a skill would use to call it. Distinct
/// capabilities of the same kind (e.g. two `fs:read` grants for different paths) share one
/// import slot — the per-call scope re-check (Task 6) is what actually distinguishes them, not
/// the linker.
///
/// Wildcards never appear in `grants.granted` (`resolve()` always routes them through `prompt`
/// instead — see `pythia_manifest::resolve`), so they have no import name here.
fn import_name_for(capability: &Capability) -> Option<String> {
    match capability {
        Capability::FsRead(_) => Some("fs_read".to_string()),
        Capability::Net(service) => Some(format!("net_{service}_send")),
        Capability::Secret(_) => Some("secret_get".to_string()),
        Capability::FsReadWildcard | Capability::NetWildcard => None,
    }
}

/// Placeholder host function body. Presence of the import slot — not what it does when called —
/// is the load-bearing behavior this task proves; Tasks 6/7/8 replace this per capability.
fn placeholder(_caller: Caller<'_, HostState>) {}

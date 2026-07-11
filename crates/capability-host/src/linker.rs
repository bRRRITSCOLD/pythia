//! Builds a `Linker<HostState>` whose *only* imports are (a) the standard WASI preview1 surface
//! (always present — every wasm32-wasip1 module needs it to be a valid module, its authority is
//! what `wasi.rs` scopes down to zero-by-default) and (b) one placeholder slot per capability
//! actually present in `grants.granted`.
//!
//! `fs_read` (Task 6, `crate::host_fns::fs::fs_read`) and `secret_get` (Task 8,
//! `crate::host_fns::secret::secret_get`) have real bodies; `net_*_send` remains a placeholder
//! import slot until Task 7. What Task 5 proved and this still relies on: a capability that isn't
//! granted has no import slot at all, so a module that references it fails instantiation with
//! wasmtime's own "unknown import" error — not a runtime permission check inside a host function
//! that could be forgotten or bypassed.
//!
//! One WASI preview1 import is deliberately *not* left at its `wasmtime-wasi` default:
//! `poll_oneoff` is overridden immediately after `add_to_linker_sync` to always deny (see
//! `WASI_ERRNO_NOTSUP` below) because its default implementation blocks the host OS thread for a
//! guest-controlled duration -- a fuel-blind hang no capability grant gates.

use std::collections::HashSet;

use anyhow::Result;
use pythia_manifest::{host_fn, Capability, ResolvedGrants};
use wasmtime::{Caller, Engine, Linker};

use crate::host_fns;
use crate::HostState;

/// WASI preview1's own import module namespace -- the fixed name `wasmtime_wasi::preview1`
/// registers every WASI function under (per the `wasi_snapshot_preview1` witx module), and the
/// same name a `poll_oneoff` override below must target to land in the same linker slot.
const WASI_PREVIEW1_MODULE: &str = "wasi_snapshot_preview1";

/// `errno::NOTSUP` (58) from `wasi_snapshot_preview1.witx`'s `$errno` enum -- returned by the
/// `poll_oneoff` stub below so a denied call surfaces as an ordinary WASI error the guest's libc
/// can translate, not a trap.
const WASI_ERRNO_NOTSUP: i32 = 58;

pub(crate) fn build_linker(engine: &Engine, grants: &ResolvedGrants) -> Result<Linker<HostState>> {
    let mut linker = Linker::new(engine);

    wasmtime_wasi::preview1::add_to_linker_sync(&mut linker, |state: &mut HostState| {
        &mut state.wasi
    })?;

    // Fuel-blind hang vector: `poll_oneoff` is WASI preview1's blocking wait -- a guest can
    // subscribe to a purely relative monotonic-clock timer and the host-side implementation
    // parks the calling (single-threaded kernel) OS thread for that guest-controlled duration.
    // Fuel only decrements on executed wasm instructions, so a parked host thread is invisible to
    // it: this is a hang no fuel budget bounds. Real async waiting is a capability-gated design
    // this crate doesn't have yet, so for now the entire call is denied at the linker rather than
    // left wired to wasmtime-wasi's blocking implementation. `allow_shadowing` is needed because
    // `add_to_linker_sync` above already defined this exact (module, name) slot; this call
    // replaces that definition rather than adding a second one.
    linker.allow_shadowing(true);
    linker.func_wrap(
        WASI_PREVIEW1_MODULE,
        "poll_oneoff",
        |_caller: Caller<'_, HostState>,
         _in_ptr: i32,
         _out_ptr: i32,
         _nsubscriptions: i32,
         _nevents_out_ptr: i32|
         -> i32 { WASI_ERRNO_NOTSUP },
    )?;
    linker.allow_shadowing(false);

    let mut registered = HashSet::new();
    for capability in &grants.granted {
        let Some(import_name) = import_name_for(capability) else {
            continue;
        };
        if registered.insert(import_name.clone()) {
            register_import(&mut linker, &import_name)?;
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

/// Registers the real host function body for capabilities that have one (`fs_read`, Task 6;
/// `secret_get`, Task 8), or the placeholder for the rest (`net_*_send` — Task 7). Every branch
/// still registers *an* import slot for a granted capability; which body runs is the only thing
/// that changes per task.
///
/// Registered under `host_fn::WASM_IMPORT_MODULE` (`"pythia"`) -- the same module name
/// `pythia_manifest::host_fn` and the skill SDK's `#[link(wasm_import_module = "pythia")]`
/// (`skills/skill-sdk/src/imports.rs`) both use. A real skill's compiled wasm import table
/// therefore names entries `pythia.fs_read`, `pythia.secret_get`, `pythia.net_smtp_send`; this
/// linker must register under the identical module name or instantiation fails on "unknown
/// import" for every real skill (issue #32).
fn register_import(linker: &mut Linker<HostState>, import_name: &str) -> Result<()> {
    if import_name == host_fn::FS_READ {
        linker.func_wrap(host_fn::WASM_IMPORT_MODULE, import_name, host_fns::fs::fs_read)?;
    } else if import_name == host_fn::SECRET_GET {
        linker.func_wrap(
            host_fn::WASM_IMPORT_MODULE,
            import_name,
            host_fns::secret::secret_get,
        )?;
    } else {
        linker.func_wrap(host_fn::WASM_IMPORT_MODULE, import_name, placeholder)?;
    }
    Ok(())
}

/// Placeholder host function body for capabilities without a real implementation yet. Presence
/// of the import slot — not what it does when called — is the load-bearing behavior Task 5
/// proves; Task 7 replaces this for `net_*_send`.
fn placeholder(_caller: Caller<'_, HostState>) {}

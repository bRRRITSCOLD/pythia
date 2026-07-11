//! Builds the WASI (preview1) context for a skill instantiation from its resolved grants.
//!
//! The builder starts from `WasiCtxBuilder::new()`, which is already the most restrictive
//! possible context — no `inherit_*` call is ever made. Every addition below is driven by an
//! entry in `grants.granted`; nothing is added "for convenience" (no cwd/home preopen, no env
//! passthrough). This is SR-4: zero ambient authority by default.
//!
//! Clock and random number generation are *not* gated behind a capability — `WasiCtxBuilder`
//! wires in host clocks/RNG unconditionally because WASI itself requires a working
//! `clock_time_get`/`random_get` to function at all (there's no host-identifying information
//! leaked by "what time is it" or "give me random bytes" the way there is by "read this file"
//! or "reach this env var"). That's the "beyond WASI minimum" line SR-4 draws.

use anyhow::Result;
use pythia_manifest::{Capability, ResolvedGrants};
use wasmtime_wasi::preview1::WasiP1Ctx;
use wasmtime_wasi::{DirPerms, FilePerms, WasiCtxBuilder};

/// Builds a `WasiP1Ctx` with exactly the ambient authority `grants` calls for — one preopen per
/// `fs:read` grant, scoped to precisely the granted path, and nothing else.
pub(crate) fn build_wasi_ctx(grants: &ResolvedGrants) -> Result<WasiP1Ctx> {
    let mut builder = WasiCtxBuilder::new();

    for capability in &grants.granted {
        if let Capability::FsRead(path) = capability {
            let guest_path = path.to_string_lossy().into_owned();
            builder.preopened_dir(path, guest_path, DirPerms::READ, FilePerms::READ)?;
        }
    }

    Ok(builder.build_p1())
}

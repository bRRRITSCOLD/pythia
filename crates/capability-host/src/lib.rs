//! `pythia-capability-host`: wasmtime embedder, `Linker` construction from
//! `pythia_manifest::ResolvedGrants`, per-call skill instantiation.
//!
//! This crate owns SR-4 (zero WASI ambient authority): the `WasiCtx` built for every
//! instantiation starts from the most restrictive configuration wasmtime-wasi offers and gains
//! authority only where a capability was actually granted. It also carries the mechanism behind
//! SR-2's first two assertions: a capability that isn't granted has no import slot in the
//! `Linker`, so a skill module that references it fails instantiation outright — denial is a
//! structural absence, not a runtime check that could be skipped.
//!
//! No host function has a real body in this task — `fs_read`/`net_*_send`/`secret_get` are
//! placeholder import slots here; their bodies land in Tasks 6, 7, and 8.

mod linker;
mod wasi;

use std::fmt;

use pythia_manifest::{resolve, PolicyFile, SkillManifest};
use wasmtime::{Engine, Module, Store, Val};
use wasmtime_wasi::preview1::WasiP1Ctx;

/// Per-`Store` state: currently just the WASI preview1 context. Host functions land here as
/// fields in Tasks 6/7/8 (e.g. secret store, fuel/memory accounting).
struct HostState {
    wasi: WasiP1Ctx,
}

/// Everything that can go wrong standing up a skill sandbox.
#[derive(Debug)]
pub enum HostError {
    /// A skill's wasm module imports a capability that wasn't in `grants.granted` — there was no
    /// matching import slot in the `Linker` at all. This is SR-2's core mechanism: absent grant
    /// -> absent import -> instantiation fails, before any skill-specific host function runs.
    CapabilityDenied(String),
    /// Any other wasmtime-level failure: malformed wasm bytes, a WASI context that failed to
    /// build (e.g. a granted `fs:read` path that doesn't exist on the host), or an instantiation
    /// failure not caused by a missing capability import.
    Wasmtime(anyhow::Error),
}

impl fmt::Display for HostError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            HostError::CapabilityDenied(import) => {
                write!(f, "capability denied: import `{import}` was not granted")
            }
            HostError::Wasmtime(err) => write!(f, "capability host error: {err}"),
        }
    }
}

impl std::error::Error for HostError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            HostError::CapabilityDenied(_) => None,
            HostError::Wasmtime(err) => Some(err.as_ref()),
        }
    }
}

/// A running (instantiated, not-yet-called) skill: its `Store` and the `wasmtime::Instance`
/// created inside it. Exported functions are reached through the narrow `call_i32` helper below
/// — the full calling convention lands with the skill SDK (Task 11) once host functions have
/// real bodies (Tasks 6/7/8).
pub struct Instance {
    store: Store<HostState>,
    inner: wasmtime::Instance,
}

impl Instance {
    /// Calls an exported function taking zero or more `i32` params and returning exactly one
    /// `i32`. Enough surface for this task's mechanism tests (WASI ambient-authority probes,
    /// capability presence checks) without committing to a full calling convention early.
    pub fn call_i32(&mut self, func_name: &str, args: &[i32]) -> Result<i32, HostError> {
        let func = self
            .inner
            .get_func(&mut self.store, func_name)
            .ok_or_else(|| {
                HostError::Wasmtime(anyhow::anyhow!("no exported function named `{func_name}`"))
            })?;

        let call_args: Vec<Val> = args.iter().map(|&v| Val::I32(v)).collect();
        let mut results = [Val::I32(0)];
        func.call(&mut self.store, &call_args, &mut results)
            .map_err(HostError::Wasmtime)?;

        match results.first() {
            Some(Val::I32(value)) => Ok(*value),
            _ => Err(HostError::Wasmtime(anyhow::anyhow!(
                "`{func_name}` did not return a single i32"
            ))),
        }
    }
}

/// Owns the wasmtime `Engine` (expensive to create, cheap to share) and instantiates skills into
/// a fresh `Store`/`Linker` per call.
pub struct CapabilityHost {
    engine: Engine,
}

impl CapabilityHost {
    pub fn new() -> Result<Self, HostError> {
        Ok(CapabilityHost {
            engine: Engine::default(),
        })
    }

    /// Resolves `manifest.requested` against `policy` (fail-closed — see
    /// `pythia_manifest::resolve`), builds a `WasiCtx` and `Linker` carrying exactly that
    /// authority, and instantiates `module_bytes` inside a fresh `Store`.
    ///
    /// Returns `HostError::CapabilityDenied` if the module imports anything outside what was
    /// granted — checked explicitly against the `Linker`'s contents before instantiation is
    /// attempted, so the failure mode is deterministic and doesn't depend on wasmtime's error
    /// message text.
    pub fn instantiate(
        &self,
        module_bytes: &[u8],
        manifest: &SkillManifest,
        policy: &PolicyFile,
    ) -> Result<Instance, HostError> {
        let grants = resolve(&manifest.requested, policy, &manifest.name);

        let module = Module::new(&self.engine, module_bytes).map_err(HostError::Wasmtime)?;
        let linker = linker::build_linker(&self.engine, &grants).map_err(HostError::Wasmtime)?;
        let wasi_ctx = wasi::build_wasi_ctx(&grants).map_err(HostError::Wasmtime)?;

        let mut store = Store::new(&self.engine, HostState { wasi: wasi_ctx });

        for import in module.imports() {
            if linker
                .get(&mut store, import.module(), import.name())
                .is_none()
            {
                return Err(HostError::CapabilityDenied(format!(
                    "{}::{}",
                    import.module(),
                    import.name()
                )));
            }
        }

        let inner = linker
            .instantiate(&mut store, &module)
            .map_err(HostError::Wasmtime)?;

        Ok(Instance { store, inner })
    }
}

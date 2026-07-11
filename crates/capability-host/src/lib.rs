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
//! `fs_read` has a real body as of Task 6 (`host_fns::fs`, SR-3's per-call scope re-check);
//! `net_*_send`/`secret_get` remain placeholder import slots until Task 8.
//!
//! It also owns SR-6 (fuel + memory limits, `limits.rs`): every `Store` carries an explicit fuel
//! budget and a linear-memory ceiling, and exceeding either force-terminates the instance as a
//! distinct `HostError::ResourceLimitExceeded` rather than hanging the kernel's single-threaded
//! loop.

mod host_fns;
mod limits;
mod linker;
mod wasi;

use std::fmt;

use pythia_manifest::{resolve, PolicyFile, ResolvedGrants, SkillManifest};
use wasmtime::{Config, Engine, Module, Store, Trap, Val};
use wasmtime_wasi::preview1::WasiP1Ctx;

/// Negative sentinels shared across the wasm ABI boundary and this crate's own test suite (a
/// separate crate, so it can only see `pub` items) -- see `host_fns::fs` for the values and how
/// each is produced.
pub use host_fns::fs::{BUFFER_TOO_SMALL, DENIED, IO_ERROR};

/// Per-`Store` state: the WASI preview1 context, the resolved grants so host functions
/// (e.g. `fs_read`) can re-check scope against the exact grant on every call rather than trust a
/// decision cached at link time (SR-3), and the SR-6 memory-limit accounting.
struct HostState {
    wasi: WasiP1Ctx,
    grants: ResolvedGrants,
    limits: limits::MemoryLimiter,
}

/// Everything that can go wrong standing up a skill sandbox.
#[derive(Debug)]
pub enum HostError {
    /// A skill's wasm module imports a capability that wasn't in `grants.granted` — there was no
    /// matching import slot in the `Linker` at all. This is SR-2's core mechanism: absent grant
    /// -> absent import -> instantiation fails, before any skill-specific host function runs.
    CapabilityDenied(String),
    /// A skill instantiation exceeded its fuel budget or linear-memory ceiling (SR-6) and was
    /// force-terminated. Kept distinct from `Wasmtime` so callers (Task 9/15) can map it to
    /// `effect_result.status = "resource_limit_exceeded"`, never conflated with `"denied"`.
    ResourceLimitExceeded(String),
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
            HostError::ResourceLimitExceeded(reason) => {
                write!(f, "resource limit exceeded: {reason}")
            }
            HostError::Wasmtime(err) => write!(f, "capability host error: {err}"),
        }
    }
}

impl std::error::Error for HostError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            HostError::CapabilityDenied(_) => None,
            HostError::ResourceLimitExceeded(_) => None,
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
            .map_err(|err| self.classify_call_error(err))?;

        match results.first() {
            Some(Val::I32(value)) => Ok(*value),
            _ => Err(HostError::Wasmtime(anyhow::anyhow!(
                "`{func_name}` did not return a single i32"
            ))),
        }
    }

    /// Reads `len` bytes out of the instance's exported `memory` at `offset`. Used to retrieve
    /// what a host function (e.g. `fs_read`) wrote back into guest linear memory.
    ///
    /// Rejects negative `offset`/`len` with an error rather than silently clamping to `0`: a
    /// clamp would turn a caller bug (or a negative byte count returned by a host function) into
    /// a read from an unintended offset instead of a visible failure.
    pub fn read_memory(&mut self, offset: i32, len: i32) -> Result<Vec<u8>, HostError> {
        if offset < 0 || len < 0 {
            return Err(HostError::Wasmtime(anyhow::anyhow!(
                "read_memory: offset and len must be non-negative (offset={offset}, len={len})"
            )));
        }
        let memory = self.memory()?;
        let mut buf = vec![0u8; len as usize];
        memory
            .read(&mut self.store, offset as usize, &mut buf)
            .map_err(|err| HostError::Wasmtime(anyhow::Error::from(err)))?;
        Ok(buf)
    }

    /// Writes `bytes` into the instance's exported `memory` at `offset`. Used by callers/tests to
    /// stage arguments (e.g. a path string) before invoking an exported function.
    ///
    /// Rejects a negative `offset` with an error rather than silently clamping to `0` (see
    /// `read_memory`).
    pub fn write_memory(&mut self, offset: i32, bytes: &[u8]) -> Result<(), HostError> {
        if offset < 0 {
            return Err(HostError::Wasmtime(anyhow::anyhow!(
                "write_memory: offset must be non-negative (offset={offset})"
            )));
        }
        let memory = self.memory()?;
        memory
            .write(&mut self.store, offset as usize, bytes)
            .map_err(|err| HostError::Wasmtime(anyhow::Error::from(err)))
    }

    fn memory(&mut self) -> Result<wasmtime::Memory, HostError> {
        self.inner
            .get_memory(&mut self.store, "memory")
            .ok_or_else(|| {
                HostError::Wasmtime(anyhow::anyhow!("instance has no exported `memory`"))
            })
    }

    /// SR-6: turns a failed call into `HostError::ResourceLimitExceeded` when it was caused by
    /// fuel exhaustion or the store's memory ceiling, rather than surfacing it as an opaque
    /// `HostError::Wasmtime`. Fuel exhaustion is a well-known wasmtime trap code
    /// (`Trap::OutOfFuel`); the memory ceiling is this store's own `MemoryLimiter`, so its
    /// `exceeded()` flag is checked directly rather than pattern-matching trap text.
    fn classify_call_error(&self, err: anyhow::Error) -> HostError {
        if matches!(err.downcast_ref::<Trap>(), Some(Trap::OutOfFuel)) {
            return HostError::ResourceLimitExceeded(
                "fuel budget exhausted before the call completed".to_string(),
            );
        }
        if self.store.data().limits.exceeded() {
            return HostError::ResourceLimitExceeded(
                "linear memory ceiling exceeded before the call completed".to_string(),
            );
        }
        HostError::Wasmtime(err)
    }
}

/// Owns the wasmtime `Engine` (expensive to create, cheap to share) and instantiates skills into
/// a fresh `Store`/`Linker` per call.
pub struct CapabilityHost {
    engine: Engine,
}

impl CapabilityHost {
    pub fn new() -> Result<Self, HostError> {
        // SR-6: fuel consumption must be enabled at the `Engine` (not `Store`) level for
        // `Store::set_fuel` to have any effect -- see `limits::configure_limits`.
        let mut config = Config::new();
        config.consume_fuel(true);
        let engine = Engine::new(&config).map_err(HostError::Wasmtime)?;

        Ok(CapabilityHost { engine })
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

        let mut store = Store::new(
            &self.engine,
            HostState {
                wasi: wasi_ctx,
                grants,
                limits: limits::MemoryLimiter::new(limits::MEMORY_LIMIT_BYTES),
            },
        );
        limits::configure_limits(&mut store).map_err(HostError::Wasmtime)?;

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

//! SR-6: every `Store` created for a skill instantiation carries an explicit fuel budget and a
//! linear-memory ceiling. Exceeding either force-terminates the instance -- a wasmtime trap that
//! unwinds back to the caller inside `Instance::call_i32` -- rather than hanging the kernel's
//! single-threaded loop on a skill that loops forever or grows memory without bound.
//!
//! Fuel is the concrete mechanism chosen here (over epoch-interruption-plus-ticker): this is a
//! single-threaded embedder, and `Store::set_fuel` gives an exact, deterministic budget with no
//! background thread required. The memory ceiling is enforced via a custom `ResourceLimiter`
//! rather than wasmtime's built-in `StoreLimits` so termination is a trap (not a silent
//! `memory.grow` failure the skill could loop past) and so the crate can tell "this store hit its
//! own ceiling" apart from any other trap without parsing error text.

use anyhow::{bail, Result};
use wasmtime::{ResourceLimiter, Store};

use crate::HostState;

/// Fuel granted to every skill instantiation. Wasmtime charges roughly one unit per executed
/// instruction/block -- generous enough for real skill work, small enough that an infinite loop
/// terminates well within a test's timeout.
pub(crate) const FUEL_BUDGET: u64 = 5_000_000;

/// Linear-memory ceiling per `Store`, in bytes (16 MiB -- 256 wasm pages of 64 KiB each).
pub(crate) const MEMORY_LIMIT_BYTES: usize = 16 * 1024 * 1024;

/// A `ResourceLimiter` scoped to one `Store`, enforcing `MEMORY_LIMIT_BYTES` and remembering
/// whether *this* store was the one that got force-terminated for it -- `Instance::call_i32`
/// reads `exceeded()` after a failed call to distinguish a memory-ceiling trap from any other
/// wasmtime error, independent of the trap's own representation.
pub(crate) struct MemoryLimiter {
    max_bytes: usize,
    exceeded: bool,
}

impl MemoryLimiter {
    pub(crate) fn new(max_bytes: usize) -> Self {
        MemoryLimiter {
            max_bytes,
            exceeded: false,
        }
    }

    pub(crate) fn exceeded(&self) -> bool {
        self.exceeded
    }
}

impl ResourceLimiter for MemoryLimiter {
    fn memory_growing(
        &mut self,
        _current: usize,
        desired: usize,
        _maximum: Option<usize>,
    ) -> Result<bool> {
        if desired > self.max_bytes {
            self.exceeded = true;
            bail!(
                "linear memory ceiling of {} bytes exceeded (requested {desired} bytes)",
                self.max_bytes
            );
        }
        Ok(true)
    }

    fn table_growing(
        &mut self,
        _current: usize,
        _desired: usize,
        _maximum: Option<usize>,
    ) -> Result<bool> {
        // This task owns memory + fuel only (SR-6); table growth is unbounded here by design --
        // no capability grants a skill the ability to reference host-managed tables at all.
        Ok(true)
    }
}

/// Applies the fuel budget and memory ceiling to `store`. Must run after `HostState` (and its
/// `MemoryLimiter`) is constructed but before the module is instantiated, since a skill's start
/// section or data-segment initialization can itself grow memory or burn fuel.
pub(crate) fn configure_limits(store: &mut Store<HostState>) -> Result<()> {
    store.set_fuel(FUEL_BUDGET)?;
    store.limiter(|state| &mut state.limits as &mut dyn ResourceLimiter);
    Ok(())
}

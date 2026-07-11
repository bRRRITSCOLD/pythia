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

/// Table-element ceiling per `Store` (SR-6). A module needs no capability grant to declare its
/// *own* internal `(table N funcref)` -- unlike linear memory there is no host-managed resource
/// being referenced, so the capability system has nothing to gate here. Left unbounded, a single
/// `table.grow` instruction (one fuel unit) can request billions of elements; wasmtime reserves a
/// pointer's worth of space per element (8 bytes on a 64-bit host), so an unbounded grant lets one
/// instruction commit tens of gigabytes and OOM-kill the embedding process before fuel or the
/// memory ceiling ever come into play. 10,000 elements (wasmtime's own `DEFAULT_TABLE_LIMIT`
/// count, ~80 KiB at 8 bytes/element) is far more than any real skill's function-pointer table
/// needs and small enough that even filling it is inexpensive.
pub(crate) const TABLE_ELEMENT_LIMIT: usize = 10_000;

/// A `ResourceLimiter` scoped to one `Store`, enforcing `MEMORY_LIMIT_BYTES` and
/// `TABLE_ELEMENT_LIMIT` and remembering which ceiling (if either) got *this* store
/// force-terminated -- `Instance::classify_call_error` consumes these flags after a failed call to
/// attribute the trap precisely, independent of the trap's own representation.
pub(crate) struct MemoryLimiter {
    max_bytes: usize,
    max_table_elements: usize,
    memory_exceeded: bool,
    table_exceeded: bool,
}

impl MemoryLimiter {
    pub(crate) fn new(max_bytes: usize, max_table_elements: usize) -> Self {
        MemoryLimiter {
            max_bytes,
            max_table_elements,
            memory_exceeded: false,
            table_exceeded: false,
        }
    }

    /// Returns whether the memory ceiling caused the most recent trap, resetting the flag to
    /// `false`. Take-and-reset (rather than a plain read) so a later, unrelated call on the same
    /// `Instance` can't be misclassified as a repeat of a memory-ceiling kill that already
    /// happened and was already reported.
    pub(crate) fn take_memory_exceeded(&mut self) -> bool {
        std::mem::take(&mut self.memory_exceeded)
    }

    /// Returns whether the table-element ceiling caused the most recent trap, resetting the flag
    /// to `false`. Same take-and-reset rationale as `take_memory_exceeded`.
    pub(crate) fn take_table_exceeded(&mut self) -> bool {
        std::mem::take(&mut self.table_exceeded)
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
            self.memory_exceeded = true;
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
        desired: usize,
        _maximum: Option<usize>,
    ) -> Result<bool> {
        // Fires for both the initial table allocation at instantiate time and every runtime
        // `table.grow`, exactly like `memory_growing` -- so this bounds a skill's declared initial
        // table size as well as any growth it performs later. `bail!` (not `Ok(false)`) so the
        // instance traps immediately rather than a skill looping on a failed grow.
        if desired > self.max_table_elements {
            self.table_exceeded = true;
            bail!(
                "table element ceiling of {} elements exceeded (requested {desired} elements)",
                self.max_table_elements
            );
        }
        Ok(true)
    }
}

/// Applies the fuel budget and memory/table ceilings to `store`. Must run after `HostState` (and
/// its `MemoryLimiter`) is constructed but before the module is instantiated, since a skill's
/// start section or data-segment initialization can itself grow memory, grow a table, or burn
/// fuel.
pub(crate) fn configure_limits(store: &mut Store<HostState>) -> Result<()> {
    store.set_fuel(FUEL_BUDGET)?;
    store.limiter(|state| &mut state.limits as &mut dyn ResourceLimiter);
    Ok(())
}

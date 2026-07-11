//! Real host function bodies, one module per capability kind. `linker.rs` wires each into its
//! placeholder import slot; each module is `pub(crate)` (the security boundary stays narrow —
//! see the crate's own doc comment). The one exception is the negative-sentinel constants
//! (`fs::DENIED` etc.), which `lib.rs` re-exports at the crate root `pub` so the integration
//! test suite can share them instead of re-declaring the literals.

pub(crate) mod fs;
pub(crate) mod secret;

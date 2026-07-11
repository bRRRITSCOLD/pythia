//! Real host function bodies, one module per capability kind. `linker.rs` wires each into its
//! placeholder import slot; nothing here is `pub` outside the crate -- the security boundary
//! stays narrow (see the crate's own doc comment).

pub(crate) mod fs;

//! `fs_read` host function body (SR-3): canonicalizes the wasm-supplied path (resolving `..`
//! and symlinks) and checks it against the *exact* granted `fs:read` scope on every call -- the
//! grant is never trusted past a one-time check at link time, because there isn't one; the
//! `Linker` only proves the import slot exists (Task 5), this is what decides each call.

use std::path::PathBuf;

use pythia_manifest::Capability;
use wasmtime::{Caller, Extern};

use crate::HostState;

/// Negative sentinels returned across the wasm ABI in place of a `Result` (host functions speak
/// only in the numeric types wasm understands). A non-negative return is a byte count.
pub(crate) const DENIED: i32 = -1;
pub(crate) const BUFFER_TOO_SMALL: i32 = -2;
pub(crate) const IO_ERROR: i32 = -3;

/// `fs_read(path_ptr, path_len, buf_ptr, buf_cap) -> i32`
///
/// Reads `path_len` bytes at `path_ptr` out of the caller's linear memory as a path, canonicalizes
/// it, and requires the canonical path to fall inside the canonical form of at least one granted
/// `fs:read` scope -- recomputed from `caller.data().grants` on this call, not cached from
/// instantiation. On a match, reads the file and writes up to `buf_cap` bytes at `buf_ptr`,
/// returning the byte count written; returns a negative sentinel on denial, an oversized read, or
/// any I/O failure (including "the path doesn't exist", which canonicalization requires).
pub(crate) fn fs_read(
    mut caller: Caller<'_, HostState>,
    path_ptr: i32,
    path_len: i32,
    buf_ptr: i32,
    buf_cap: i32,
) -> i32 {
    let memory = match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => memory,
        _ => return IO_ERROR,
    };

    let requested_path = match read_guest_path(&mut caller, path_ptr, path_len) {
        Some(path) => path,
        None => return IO_ERROR,
    };

    let canonical_requested = match std::fs::canonicalize(&requested_path) {
        Ok(path) => path,
        Err(_) => return IO_ERROR,
    };

    let granted_scopes: Vec<PathBuf> = caller
        .data()
        .grants
        .granted
        .iter()
        .filter_map(|capability| match capability {
            Capability::FsRead(path) => Some(path.clone()),
            _ => None,
        })
        .collect();

    let within_scope = granted_scopes.iter().any(|scope| {
        std::fs::canonicalize(scope)
            .map(|canonical_scope| canonical_requested.starts_with(&canonical_scope))
            .unwrap_or(false)
    });

    if !within_scope {
        return DENIED;
    }

    let contents = match std::fs::read(&canonical_requested) {
        Ok(bytes) => bytes,
        Err(_) => return IO_ERROR,
    };

    if contents.len() as i32 > buf_cap {
        return BUFFER_TOO_SMALL;
    }

    match memory.write(&mut caller, buf_ptr as usize, &contents) {
        Ok(()) => contents.len() as i32,
        Err(_) => IO_ERROR,
    }
}

fn read_guest_path(caller: &mut Caller<'_, HostState>, ptr: i32, len: i32) -> Option<PathBuf> {
    if ptr < 0 || len < 0 {
        return None;
    }
    let memory = match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => memory,
        _ => return None,
    };
    let mut buf = vec![0u8; len as usize];
    memory.read(&mut *caller, ptr as usize, &mut buf).ok()?;
    String::from_utf8(buf).ok().map(PathBuf::from)
}

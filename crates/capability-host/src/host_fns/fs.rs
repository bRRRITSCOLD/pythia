//! `fs_read` host function body (SR-3, SR-13): decides scope containment *lexically* against the
//! granted `fs:read` scopes before the wasm-supplied path ever touches the filesystem, then
//! canonicalizes (resolving `..` and symlinks) to close the symlink-escape case, and only then
//! reads -- the grant is never trusted past a one-time check at link time, because there isn't
//! one; the `Linker` only proves the import slot exists (Task 5), this is what decides each call.
//!
//! Ordering here is deliberate and security-load-bearing (see the staff review that gated this
//! file, and threat-model §3 item 5 / SR-13): if the wasm-supplied path were canonicalized before
//! the scope decision, an existing-but-out-of-scope file and a nonexistent file would return
//! different sentinels (`DENIED` vs `IO_ERROR`), letting a guest binary-search the host's
//! filesystem for the existence of arbitrary paths it was never granted. Deciding containment
//! lexically first — and only canonicalizing/reading paths that already passed — closes that
//! oracle.

use std::io::Read;
use std::path::{Component, Path, PathBuf};

use pythia_manifest::Capability;
use wasmtime::{Caller, Extern, Memory};

use crate::HostState;

/// Negative sentinels returned across the wasm ABI in place of a `Result` (host functions speak
/// only in the numeric types wasm understands). A non-negative return is a byte count. `pub`
/// (not `pub(crate)`): the integration test suite under `tests/` links against this crate's
/// public API only, and needs the exact sentinel values rather than re-declaring them as
/// independent literals that could silently drift from these.
pub const DENIED: i32 = -1;
pub const BUFFER_TOO_SMALL: i32 = -2;
pub const IO_ERROR: i32 = -3;

/// PATH_MAX-style ceiling on the guest-supplied path length, enforced *before* any host
/// allocation. Without this (and the linear-memory bounds check in `read_guest_path`), a guest
/// could supply `path_len` up to ~`i32::MAX` and force a multi-gigabyte host-side `Vec`
/// allocation before any scope decision ran.
const MAX_PATH_LEN: usize = 4096;

/// `fs_read(path_ptr, path_len, buf_ptr, buf_cap) -> i32`
///
/// Reads `path_len` bytes at `path_ptr` out of the caller's linear memory as a path, lexically
/// normalizes it and checks containment against the canonical form of the granted `fs:read`
/// scopes (recomputed from `caller.data().grants` on this call, not cached from instantiation)
/// *before* touching the filesystem for the requested path at all. Only a path that already
/// passed that lexical check is canonicalized (resolving symlinks) and re-checked, then read: up
/// to `buf_cap` bytes are written at `buf_ptr`, and the byte count is returned. Returns a
/// negative sentinel on denial, an oversized read, or any I/O failure.
pub(crate) fn fs_read(
    mut caller: Caller<'_, HostState>,
    path_ptr: i32,
    path_len: i32,
    buf_ptr: i32,
    buf_cap: i32,
) -> i32 {
    if buf_ptr < 0 || buf_cap < 0 {
        return IO_ERROR;
    }

    let memory = match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => memory,
        _ => return IO_ERROR,
    };

    let requested_path = match read_guest_path(&mut caller, &memory, path_ptr, path_len) {
        Some(path) => path,
        None => return IO_ERROR,
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

    // Canonicalizing the granted scopes is safe: these paths come from host policy, never from
    // the guest, so there is no existence oracle in resolving them.
    let canonical_scopes: Vec<PathBuf> = granted_scopes
        .iter()
        .filter_map(|scope| std::fs::canonicalize(scope).ok())
        .collect();

    let lexical_requested = match lexical_absolute(&requested_path) {
        Some(path) => path,
        None => return DENIED,
    };

    if !canonical_scopes
        .iter()
        .any(|scope| lexical_requested.starts_with(scope))
    {
        // Denied without ever calling `canonicalize`/`stat` on the requested path: a
        // nonexistent out-of-scope path and an existing out-of-scope path are indistinguishable
        // to the guest, both DENIED here.
        return DENIED;
    }

    // Only paths that survived lexical containment reach the filesystem. A nonexistent path
    // *inside* a granted scope surfacing as IO_ERROR here is not an oracle: the guest already
    // has read authority over that scope, so "this path in my own scope doesn't exist" reveals
    // nothing it wasn't already entitled to know.
    let canonical_requested = match std::fs::canonicalize(&requested_path) {
        Ok(path) => path,
        Err(_) => return IO_ERROR,
    };

    let within_scope = canonical_scopes
        .iter()
        .any(|scope| canonical_requested.starts_with(scope));
    if !within_scope {
        // Symlink inside a granted scope that resolves outside it.
        return DENIED;
    }

    // TOCTOU (threat-model §3 item 5, SR-13): `canonicalize` above and `File::open` below are
    // not atomic -- a concurrent symlink swap at `canonical_requested` in between could redirect
    // the open outside the granted scope. Accepted residual risk for this pass: skills have no
    // `fs:write`/create capability yet, so a guest cannot stage the swap itself; only a
    // cooperating host-side process could race this window. Using a single `File` handle for
    // both the metadata check and the read below (rather than re-resolving the path a third
    // time) closes the *second* TOCTOU window between the size check and the read. Follow-up,
    // once `fs:write` lands: reopen through a rooted/cap-std-style directory handle, or verify
    // the opened fd's canonical path still matches `canonical_requested` post-open.
    let file = match std::fs::File::open(&canonical_requested) {
        Ok(file) => file,
        Err(_) => return IO_ERROR,
    };

    let metadata = match file.metadata() {
        Ok(metadata) => metadata,
        Err(_) => return IO_ERROR,
    };

    let buf_cap_u64 = buf_cap as u64;
    if metadata.len() > buf_cap_u64 {
        // Reject via the metadata length *before* reading: a whole-file `std::fs::read` followed
        // by `contents.len() as i32` would silently wrap negative for files >= 2^31 bytes,
        // bypassing this check after already paying for an unbounded host-side read.
        return BUFFER_TOO_SMALL;
    }

    // Bound the read itself to `buf_cap` (+1 to detect the file having grown since the metadata
    // check above) instead of `std::fs::read`'s whole-file slurp.
    let mut contents = Vec::new();
    if file
        .take(buf_cap_u64.saturating_add(1))
        .read_to_end(&mut contents)
        .is_err()
    {
        return IO_ERROR;
    }

    let written_len = match i32::try_from(contents.len()) {
        Ok(len) if len <= buf_cap => len,
        _ => return BUFFER_TOO_SMALL,
    };

    match memory.write(&mut caller, buf_ptr as usize, &contents) {
        Ok(()) => written_len,
        Err(_) => IO_ERROR,
    }
}

/// Reads `len` bytes of guest-controlled path bytes out of linear memory at `ptr`, rejecting
/// (without allocating) anything that exceeds `MAX_PATH_LEN` or the instance's actual memory
/// size. The previous version allocated `vec![0u8; len]` for whatever `len` the guest supplied —
/// up to ~2GiB — before any bounds check ran.
fn read_guest_path(
    caller: &mut Caller<'_, HostState>,
    memory: &Memory,
    ptr: i32,
    len: i32,
) -> Option<PathBuf> {
    if ptr < 0 || len < 0 {
        return None;
    }
    let ptr = ptr as usize;
    let len = len as usize;
    if len > MAX_PATH_LEN {
        return None;
    }
    let memory_size = memory.data_size(&mut *caller);
    if len > memory_size {
        return None;
    }
    let end = ptr.checked_add(len)?;
    if end > memory_size {
        return None;
    }
    let mut buf = vec![0u8; len];
    memory.read(&mut *caller, ptr, &mut buf).ok()?;
    String::from_utf8(buf).ok().map(PathBuf::from)
}

/// Lexically normalizes `path` (resolving `.`/`..` components in-memory, no filesystem access)
/// after making it absolute against the process's current directory if it wasn't already. Used
/// to decide scope containment *before* the requested path is allowed to touch the filesystem
/// (see the module doc comment on the existence-oracle this closes).
fn lexical_absolute(path: &Path) -> Option<PathBuf> {
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        std::env::current_dir().ok()?.join(path)
    };
    Some(normalize_lexically(&absolute))
}

/// `..`/`.` component resolution using only the path's own components — never touches the
/// filesystem, so it cannot be used as (or influenced by) a symlink/existence oracle.
fn normalize_lexically(path: &Path) -> PathBuf {
    let mut result = PathBuf::new();
    for component in path.components() {
        match component {
            Component::ParentDir => {
                match result.components().next_back() {
                    Some(Component::Normal(_)) => {
                        result.pop();
                    }
                    Some(Component::RootDir) | None => {
                        // Can't go above the root or above an already-empty relative base;
                        // drop the `..` rather than letting it escape the base lexically.
                    }
                    _ => {
                        result.push(component);
                    }
                }
            }
            Component::CurDir => {}
            other => result.push(other),
        }
    }
    result
}

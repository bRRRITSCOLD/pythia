//! `extern "C"` host imports and their safe Rust wrappers.
//!
//! The import names below (`fs_read`, `net_smtp_send`, `secret_get`) must match
//! `pythia_manifest::host_fn`'s constants and the Linker registration names
//! `pythia-capability-host` builds from granted capabilities (Tasks 5-8). The two workspaces
//! don't share a build dependency — the skills workspace targets `wasm32-wasip1` and never
//! links `wasmtime` — so this is kept in sync by convention, not enforced by the compiler (see
//! the `imports_match_host_fn_constants` test below, which at least catches a rename here).
//!
//! Only meaningful under `wasm32`: there is no host on the other end of these imports when
//! this crate is compiled for the native test target, so the `extern "C"` block and its
//! wrappers are cfg-gated out there (this module's ptr/len marshaling isn't part of the crate's
//! host-target test suite — see `pythia-capability-host`'s own tests for exercising `fs_read`
//! end to end).
//!
//! # Return-buffer allocation ABI
//!
//! Each import writes its result into linear memory and returns `(ptr, len)` (`len` via the
//! `out_len` out-param). For `take_host_bytes` to reclaim that buffer as a `Vec<u8>` soundly,
//! the memory at `ptr` must have come from *this guest's* global allocator, with capacity
//! exactly `len` — `Vec::from_raw_parts`'s safety contract. That means the host cannot write
//! into a scratch region or a `memory.grow`-extended range of its own choosing; it must first
//! ask the guest to allocate. This module exports `pythia_alloc` for that purpose:
//!
//! 1. Guest calls a host import (e.g. `fs_read`).
//! 2. Host computes the result, then calls the guest's exported `pythia_alloc(len) -> *mut u8`
//!    to obtain a guest-owned buffer of the right size.
//! 3. Host writes `len` bytes into the guest's linear memory starting at that pointer.
//! 4. Host returns the pointer (and `len`) to the guest as the import's result.
//! 5. Guest calls `take_host_bytes(ptr, len)`, which is sound because the buffer was allocated
//!    by step 2's `pythia_alloc` call with the same `len`.
//!
//! `pythia-capability-host` (Task 9) must call the instance's exported `pythia_alloc` rather
//! than writing to an arbitrary address for this contract to hold.

#[cfg(target_arch = "wasm32")]
mod ffi {
    #[link(wasm_import_module = "pythia")]
    extern "C" {
        pub fn fs_read(path_ptr: *const u8, path_len: usize, out_len: *mut usize) -> *mut u8;
        pub fn net_smtp_send(msg_ptr: *const u8, msg_len: usize, out_len: *mut usize) -> *mut u8;
        pub fn secret_get(name_ptr: *const u8, name_len: usize, out_len: *mut usize) -> *mut u8;
    }
}

/// Exported so the host can obtain a guest-owned buffer of exactly `len` bytes before writing a
/// host import's result into it (see the module-level "Return-buffer allocation ABI" doc). The
/// returned pointer is allocated via this crate's global allocator with capacity `len` and is
/// leaked (via `mem::forget`) until `take_host_bytes` reclaims it with the same `len` — the
/// caller (the host) is responsible for writing into it and returning it to the guest, at which
/// point ownership passes back to the guest.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub extern "C" fn pythia_alloc(len: usize) -> *mut u8 {
    let mut buf = Vec::<u8>::with_capacity(len);
    let ptr = buf.as_mut_ptr();
    std::mem::forget(buf);
    ptr
}

/// Reads bytes back out of a `(ptr, len)` pair the host wrote into this instance's linear
/// memory, taking ownership so they're freed like any other `Vec<u8>`.
///
/// # Safety
///
/// `ptr` must be either null (meaning "no buffer", handled below) or a pointer previously
/// returned by this module's `pythia_alloc(len)` — allocated with capacity exactly `len` and
/// not yet freed or reused. Passing a pointer from any other source, or a `len` that doesn't
/// match the original `pythia_alloc` call's argument, is undefined behavior: it violates
/// `Vec::from_raw_parts`'s contract that `(ptr, len, capacity)` describe a single prior
/// allocation from this crate's global allocator. Note that a real host allocation for `len ==
/// 0` still goes through `Vec::from_raw_parts(ptr, 0, 0)` here rather than being special-cased
/// away, so it is not leaked.
#[cfg(target_arch = "wasm32")]
unsafe fn take_host_bytes(ptr: *mut u8, len: usize) -> Vec<u8> {
    if ptr.is_null() {
        return Vec::new();
    }
    Vec::from_raw_parts(ptr, len, len)
}

/// Calls the host's `fs_read` import for a granted `fs:read:<path>` capability.
#[cfg(target_arch = "wasm32")]
pub fn fs_read(path: &str) -> Vec<u8> {
    unsafe {
        let mut out_len: usize = 0;
        let ptr = ffi::fs_read(path.as_ptr(), path.len(), &mut out_len);
        take_host_bytes(ptr, out_len)
    }
}

/// Calls the host's `net_smtp_send` import for a granted `net:smtp` capability.
#[cfg(target_arch = "wasm32")]
pub fn net_smtp_send(message: &[u8]) -> Vec<u8> {
    unsafe {
        let mut out_len: usize = 0;
        let ptr = ffi::net_smtp_send(message.as_ptr(), message.len(), &mut out_len);
        take_host_bytes(ptr, out_len)
    }
}

/// Calls the host's `secret_get` import for a granted `secret:<name>` capability.
#[cfg(target_arch = "wasm32")]
pub fn secret_get(name: &str) -> Vec<u8> {
    unsafe {
        let mut out_len: usize = 0;
        let ptr = ffi::secret_get(name.as_ptr(), name.len(), &mut out_len);
        take_host_bytes(ptr, out_len)
    }
}

// `#[link(...)] extern "C"` blocks require string literals, not `const` references, so the
// import names above can't reference `pythia_manifest::host_fn` directly and are duplicated
// literals kept in sync by convention (see the module doc). This test at least makes a rename
// of one side without the other fail CI here, on the host target, rather than only surfacing as
// an instantiation-time link error in `pythia-capability-host`'s own workspace.
#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    #[test]
    fn ImportNames_MatchPythiaManifestHostFnConstants() {
        assert_eq!(pythia_manifest::host_fn::FS_READ, "fs_read");
        assert_eq!(pythia_manifest::host_fn::NET_SMTP_SEND, "net_smtp_send");
        assert_eq!(pythia_manifest::host_fn::SECRET_GET, "secret_get");
    }
}

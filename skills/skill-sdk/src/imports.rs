//! `extern "C"` host imports and their safe Rust wrappers.
//!
//! The import names below (`fs_read`, `net_smtp_send`, `secret_get`) must match
//! `pythia_manifest::host_fn`'s constants and the Linker registration names
//! `pythia-capability-host` builds from granted capabilities (Tasks 5-8). The two workspaces
//! don't share a build dependency — the skills workspace targets `wasm32-wasip1` and never
//! links `wasmtime` — so this is kept in sync by convention, not enforced by the compiler.
//!
//! Only meaningful under `wasm32`: there is no host on the other end of these imports when
//! this crate is compiled for the native test target, so the `extern "C"` block and its
//! wrappers are cfg-gated out there (this module's ptr/len marshaling isn't part of the crate's
//! host-target test suite — see `pythia-capability-host`'s own tests for exercising `fs_read`
//! end to end).

#[cfg(target_arch = "wasm32")]
mod ffi {
    #[link(wasm_import_module = "pythia")]
    extern "C" {
        pub fn fs_read(path_ptr: *const u8, path_len: usize, out_len: *mut usize) -> *mut u8;
        pub fn net_smtp_send(msg_ptr: *const u8, msg_len: usize, out_len: *mut usize) -> *mut u8;
        pub fn secret_get(name_ptr: *const u8, name_len: usize, out_len: *mut usize) -> *mut u8;
    }
}

/// Reads bytes back out of a `(ptr, len)` pair the host wrote into this instance's linear
/// memory, taking ownership so they're freed like any other `Vec<u8>`.
#[cfg(target_arch = "wasm32")]
unsafe fn take_host_bytes(ptr: *mut u8, len: usize) -> Vec<u8> {
    if ptr.is_null() || len == 0 {
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

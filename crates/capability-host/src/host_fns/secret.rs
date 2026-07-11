//! `secret_get` host function body (SR-5): resolves a granted `secret:<name>` capability to its
//! plaintext value and copies it into the calling skill's own linear memory. The plaintext is
//! visible to the skill *inside the sandbox* -- that is the point, a skill legitimately holding a
//! `secret:*` grant needs the value to act on it (e.g. build an SMTP auth header). What SR-5
//! actually requires is what happens to the value *after* this function returns it: this module
//! records every `(name, plaintext)` pair it hands out on `HostState::handed_out_secrets`, and
//! `crate::execute::build_execution_result` -- the one function anywhere in this crate that can
//! construct a public `ExecutionResult` -- unconditionally redacts every one of them before an
//! `ExecutionResult` can exist. See that module's doc comment for the rest of the mechanism.
//!
//! # Wire ABI
//!
//! `secret_get(name_ptr, name_len, out_len_ptr) -> ptr`, matching
//! `pythia-skill-sdk::imports::ffi::secret_get`'s guest-side declaration exactly. Unlike
//! `fs_read` (Task 6), which writes into a buffer the *caller* pre-allocated at a fixed offset,
//! this ABI uses the "guest allocates" contract `pythia-skill-sdk::imports` documents: the host
//! asks the guest's own exported `pythia_alloc(len) -> *mut u8` for a guest-owned buffer sized
//! exactly `len`, writes the plaintext into it, writes `len` to `out_len_ptr`, and returns the
//! buffer pointer -- never a pointer into memory the host chose on its own.
//!
//! # Secret source (not security-relevant; see Task 8's file-level approach)
//!
//! For the vertical slice, a secret named `NAME` resolves to the environment variable
//! `PYTHIA_SECRET_NAME`, read fresh on every call. The prefix exists only to avoid an accidental
//! collision with an unrelated ambient env var (e.g. `PATH`) under the same short name -- it is
//! not a security boundary; SR-5 is about what happens to the value once resolved, not how it
//! was resolved.

use wasmtime::{Caller, Extern, Val};

use pythia_manifest::Capability;

use crate::HostState;

/// The environment-variable prefix a secret's name is resolved under. See the module doc's
/// "Secret source" section.
const ENV_PREFIX: &str = "PYTHIA_SECRET_";

/// One `(capability name, plaintext bytes)` pair the host handed out to a skill during a call.
/// `crate::execute::build_execution_result` redacts every one of these, unconditionally, before
/// an `ExecutionResult` can exist -- see that module's doc comment.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct HandedOutSecret {
    pub(crate) name: String,
    pub(crate) value: Vec<u8>,
}

/// `secret_get(name_ptr, name_len, out_len_ptr) -> ptr`. See the module doc for the full ABI and
/// secret-source contract.
///
/// Re-checks (SR-3-style, on *every* call, never cached from link/import time) that a
/// `secret:<name>` capability matching *exactly* this requested name is present in
/// `caller.data().grants.granted` -- a skill importing `secret_get` at all only proves *some*
/// `secret:*` capability was granted (distinct secret grants share one import slot; see
/// `linker::import_name_for`), not that this specific name was.
///
/// On denial, a missing/unresolvable secret, or any marshaling failure, writes `0` to
/// `out_len_ptr` and returns a null (`0`) pointer. This is indistinguishable, at this minimal
/// pointer/length ABI, from "the secret's value happens to be empty" -- an accepted limitation of
/// the contract `pythia-skill-sdk::imports` defines (no separate error channel); a skill that
/// needs to tell the two apart should treat an empty/zero-length result as a failure.
pub(crate) fn secret_get(
    mut caller: Caller<'_, HostState>,
    name_ptr: i32,
    name_len: i32,
    out_len_ptr: i32,
) -> i32 {
    let name = match read_guest_string(&mut caller, name_ptr, name_len) {
        Some(name) => name,
        None => return deny(&mut caller, out_len_ptr),
    };

    let granted = caller.data().grants.granted.iter().any(|capability| {
        matches!(capability, Capability::Secret(granted_name) if granted_name == &name)
    });
    if !granted {
        return deny(&mut caller, out_len_ptr);
    }

    let value = match resolve_secret_value(&name) {
        Some(value) => value,
        None => return deny(&mut caller, out_len_ptr),
    };

    caller.data_mut().handed_out_secrets.push(HandedOutSecret {
        name: name.clone(),
        value: value.clone(),
    });

    match write_to_guest_buffer(&mut caller, &value, out_len_ptr) {
        Some(ptr) => ptr,
        None => deny(&mut caller, out_len_ptr),
    }
}

/// Writes `0` to `out_len_ptr` (best-effort -- a failure here means the guest already can't be
/// trusted to read it back sensibly) and returns the null pointer sentinel.
fn deny(caller: &mut Caller<'_, HostState>, out_len_ptr: i32) -> i32 {
    let _ = write_out_len(caller, out_len_ptr, 0);
    0
}

/// See the module doc's "Secret source" section.
fn resolve_secret_value(name: &str) -> Option<Vec<u8>> {
    std::env::var(format!("{ENV_PREFIX}{name}"))
        .ok()
        .map(String::into_bytes)
}

fn read_guest_string(caller: &mut Caller<'_, HostState>, ptr: i32, len: i32) -> Option<String> {
    if ptr < 0 || len < 0 {
        return None;
    }
    let memory = match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => memory,
        _ => return None,
    };
    let mut buf = vec![0u8; len as usize];
    memory.read(&mut *caller, ptr as usize, &mut buf).ok()?;
    String::from_utf8(buf).ok()
}

fn write_out_len(caller: &mut Caller<'_, HostState>, out_len_ptr: i32, len: u32) -> Option<()> {
    if out_len_ptr < 0 {
        return None;
    }
    let memory = match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => memory,
        _ => return None,
    };
    memory
        .write(&mut *caller, out_len_ptr as usize, &len.to_le_bytes())
        .ok()
}

/// Asks the guest's exported `pythia_alloc(len)` for a guest-owned buffer, writes `value` into
/// it, and writes `value.len()` to `out_len_ptr` -- the buffer-ownership contract
/// `pythia-skill-sdk::imports` requires so the guest's `take_host_bytes` can reclaim the memory
/// soundly (`Vec::from_raw_parts`'s safety contract: the buffer must have come from the guest's
/// own allocator).
fn write_to_guest_buffer(
    caller: &mut Caller<'_, HostState>,
    value: &[u8],
    out_len_ptr: i32,
) -> Option<i32> {
    let alloc = match caller.get_export("pythia_alloc") {
        Some(Extern::Func(func)) => func,
        _ => return None,
    };

    let mut results = [Val::I32(0)];
    alloc
        .call(&mut *caller, &[Val::I32(value.len() as i32)], &mut results)
        .ok()?;
    let ptr = match results.first() {
        Some(Val::I32(ptr)) => *ptr,
        _ => return None,
    };

    let memory = match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => memory,
        _ => return None,
    };
    memory.write(&mut *caller, ptr as usize, value).ok()?;
    write_out_len(caller, out_len_ptr, value.len() as u32)?;
    Some(ptr)
}

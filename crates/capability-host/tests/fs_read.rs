//! SR-3: the `fs_read` host function re-checks the exact granted scope on *every* call, after
//! canonicalizing the wasm-supplied path (resolving `..` and symlinks) -- never decided once at
//! link time and trusted for the life of the instance.
//!
//! Every probe module exports linear memory, bakes the wasm-supplied path into a data segment
//! at a fixed offset, and calls the `pythia_host::fs_read(path_ptr, path_len, buf_ptr, buf_cap)
//! -> i32` import: a non-negative return is the number of bytes written into `buf_ptr`; negative
//! sentinels signal denial or an I/O failure.

#![allow(non_snake_case)]

use std::collections::HashMap;
use std::fs;
use std::os::unix::fs::symlink;

use pythia_capability_host::{CapabilityHost, BUFFER_TOO_SMALL, DENIED, IO_ERROR};
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};

const PATH_OFFSET: i32 = 0;
const BUF_OFFSET: i32 = 4096;
const BUF_CAP: i32 = 4096;
/// One wasm page (64 KiB) -- the size of the `(memory (export "memory") 1)` declared by every
/// probe module below.
const MEMORY_SIZE: i32 = 65536;
/// Matches `host_fns::fs::MAX_PATH_LEN` (not exported; re-declared here since the two crates are
/// separately compiled and this is a small, deliberately-chosen literal, not a sentinel value
/// finding 5 is about sharing).
const MAX_PATH_LEN: i32 = 4096;

/// Builds a probe module whose `read(path_len)` export calls `fs_read` with the path baked at
/// `PATH_OFFSET` (length supplied by the caller, so the same module works for paths of differing
/// length) and a fixed output buffer/capacity, plus a `read_at(path_ptr, path_len)` export that
/// gives tests direct control over the path pointer as well (used to probe memory-bounds
/// handling near the end of the guest's linear memory).
fn read_probe_wat() -> String {
    format!(
        r#"
        (module
            (import "pythia_host" "fs_read"
                (func $fs_read (param i32 i32 i32 i32) (result i32)))
            (memory (export "memory") 1)
            (func (export "read") (param $path_len i32) (result i32)
                i32.const {path_offset}
                local.get $path_len
                i32.const {buf_offset}
                i32.const {buf_cap}
                call $fs_read)
            (func (export "read_at") (param $path_ptr i32) (param $path_len i32) (result i32)
                local.get $path_ptr
                local.get $path_len
                i32.const {buf_offset}
                i32.const {buf_cap}
                call $fs_read))
    "#,
        path_offset = PATH_OFFSET,
        buf_offset = BUF_OFFSET,
        buf_cap = BUF_CAP,
    )
}

fn manifest_and_policy(
    skill_name: &str,
    granted_dir: &std::path::Path,
) -> (SkillManifest, PolicyFile) {
    let capability = Capability::FsRead(granted_dir.to_path_buf());
    let manifest = SkillManifest {
        name: skill_name.to_string(),
        requested: vec![capability.clone()],
    };
    let mut skills = HashMap::new();
    skills.insert(
        skill_name.to_string(),
        HashMap::from([(capability, Decision::Grant)]),
    );
    (manifest, PolicyFile { skills })
}

#[test]
fn FsRead_PathWithinGrantedScope_ReturnsContent() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("notes.txt");
    fs::write(&file_path, b"hello from notes").expect("fixture file writes");

    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    instance
        .write_memory(PATH_OFFSET, file_path.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");

    let path_len = file_path.to_string_lossy().len() as i32;
    let result = instance
        .call_i32("read", &[path_len])
        .expect("read call succeeds");

    assert!(result >= 0, "expected a byte count, got sentinel {result}");
    let content = instance
        .read_memory(BUF_OFFSET, result)
        .expect("output buffer reads");
    assert_eq!(content, b"hello from notes");
}

#[test]
fn FsRead_DotDotTraversalOutsideGrantedScope_Denied() {
    let root = tempfile::tempdir().expect("tempdir creates");
    let granted_dir = root.path().join("notes");
    fs::create_dir(&granted_dir).expect("granted dir creates");
    let secret_path = root.path().join("secrets/id_rsa");
    fs::create_dir_all(secret_path.parent().unwrap()).expect("secrets dir creates");
    fs::write(&secret_path, b"top secret").expect("secret file writes");

    let (manifest, policy) = manifest_and_policy("fs-read-skill", &granted_dir);
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let traversal_path = granted_dir.join("../secrets/id_rsa");
    instance
        .write_memory(PATH_OFFSET, traversal_path.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");

    let path_len = traversal_path.to_string_lossy().len() as i32;
    let result = instance
        .call_i32("read", &[path_len])
        .expect("read call succeeds");

    assert_eq!(result, DENIED, "expected a `..` escape to be denied");
}

#[test]
fn FsRead_SymlinkInsideScopeResolvingOutside_Denied() {
    let root = tempfile::tempdir().expect("tempdir creates");
    let granted_dir = root.path().join("notes");
    fs::create_dir(&granted_dir).expect("granted dir creates");
    let outside_secret = root.path().join("id_rsa");
    fs::write(&outside_secret, b"top secret").expect("secret file writes");
    let link_path = granted_dir.join("link-to-secret");
    symlink(&outside_secret, &link_path).expect("symlink creates");

    let (manifest, policy) = manifest_and_policy("fs-read-skill", &granted_dir);
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    instance
        .write_memory(PATH_OFFSET, link_path.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");

    let path_len = link_path.to_string_lossy().len() as i32;
    let result = instance
        .call_i32("read", &[path_len])
        .expect("read call succeeds");

    assert_eq!(
        result, DENIED,
        "expected a symlink resolving outside the granted scope to be denied"
    );
}

#[test]
fn FsRead_ExactGrantedPath_AllowedEveryCallNotJustFirst() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("notes.txt");
    fs::write(&file_path, b"stable content").expect("fixture file writes");

    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    instance
        .write_memory(PATH_OFFSET, file_path.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");
    let path_len = file_path.to_string_lossy().len() as i32;

    let first = instance
        .call_i32("read", &[path_len])
        .expect("first read call succeeds");
    assert!(first >= 0, "expected first call to succeed, got {first}");

    let second = instance
        .call_i32("read", &[path_len])
        .expect("second read call succeeds");
    assert!(
        second >= 0,
        "expected second call to re-check and succeed identically, got {second}"
    );
    assert_eq!(
        first, second,
        "both calls should read the same content length"
    );
}

#[test]
fn FsRead_PathOutsideGrantedScope_DeniedRecordedAsToolResultDenial() {
    let granted_dir = tempfile::tempdir().expect("tempdir creates");
    let outside_dir = tempfile::tempdir().expect("tempdir creates");
    let outside_file = outside_dir.path().join("other.txt");
    fs::write(&outside_file, b"not yours").expect("fixture file writes");

    let (manifest, policy) = manifest_and_policy("fs-read-skill", granted_dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    instance
        .write_memory(PATH_OFFSET, outside_file.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");
    let path_len = outside_file.to_string_lossy().len() as i32;

    let result = instance
        .call_i32("read", &[path_len])
        .expect("read call succeeds");

    assert_eq!(
        result, DENIED,
        "a path entirely outside the granted scope must resolve to the same denial sentinel a \
         wrapping ToolResult will translate into a denial (Task 9)"
    );
}

/// Staff-review finding 1: canonicalizing the guest-supplied path *before* deciding scope turned
/// `fs_read` into an existence oracle for arbitrary host paths -- an existing-but-out-of-scope
/// file canonicalizes successfully and is denied (`DENIED`), while a nonexistent path fails
/// canonicalization first (`IO_ERROR`), letting a guest distinguish "exists but not mine" from
/// "doesn't exist" for any path on the host. Both must now produce the identical sentinel.
#[test]
fn FsRead_ExistingOutOfScopePathAndNonexistentOutOfScopePath_SameSentinel() {
    let granted_dir = tempfile::tempdir().expect("tempdir creates");
    let outside_dir = tempfile::tempdir().expect("tempdir creates");

    let existing_outside_file = outside_dir.path().join("exists.txt");
    fs::write(&existing_outside_file, b"i am here").expect("fixture file writes");
    let nonexistent_outside_file = outside_dir.path().join("does-not-exist.txt");
    assert!(
        !nonexistent_outside_file.exists(),
        "fixture precondition: this path must not exist"
    );

    let (manifest, policy) = manifest_and_policy("fs-read-skill", granted_dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");
    let host = CapabilityHost::new().expect("engine constructs");

    let mut existing_instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");
    existing_instance
        .write_memory(
            PATH_OFFSET,
            existing_outside_file.to_string_lossy().as_bytes(),
        )
        .expect("path writes into guest memory");
    let existing_len = existing_outside_file.to_string_lossy().len() as i32;
    let existing_result = existing_instance
        .call_i32("read", &[existing_len])
        .expect("read call succeeds");

    let mut nonexistent_instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");
    nonexistent_instance
        .write_memory(
            PATH_OFFSET,
            nonexistent_outside_file.to_string_lossy().as_bytes(),
        )
        .expect("path writes into guest memory");
    let nonexistent_len = nonexistent_outside_file.to_string_lossy().len() as i32;
    let nonexistent_result = nonexistent_instance
        .call_i32("read", &[nonexistent_len])
        .expect("read call succeeds");

    assert_eq!(
        existing_result, DENIED,
        "an existing out-of-scope path must be DENIED"
    );
    assert_eq!(
        nonexistent_result, DENIED,
        "a nonexistent out-of-scope path must also be DENIED"
    );
    assert_eq!(
        existing_result, nonexistent_result,
        "existing and nonexistent out-of-scope paths must be indistinguishable to the guest \
         (same sentinel) -- otherwise fs_read is an existence oracle for arbitrary host paths"
    );
}

/// Staff-review finding 2: `path_len` is guest-controlled and previously drove an unbounded host
/// allocation (`vec![0u8; len]`) before any bounds check ran. `len` one byte past the instance's
/// actual linear memory size must be rejected without attempting that allocation/read.
#[test]
fn FsRead_PathLenExceedsMemorySize_DeniedWithIoError() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let result = instance
        .call_i32("read_at", &[PATH_OFFSET, MEMORY_SIZE + 1])
        .expect("read call succeeds");

    assert_eq!(
        result, IO_ERROR,
        "path_len exceeding the instance's actual memory size must be rejected, not allocated"
    );
}

/// Staff-review finding 2: a `path_ptr` near the end of linear memory combined with a (small,
/// under-cap) `path_len` that runs past the end of memory must be rejected, not read
/// out-of-bounds or clamped.
#[test]
fn FsRead_PathPtrNearEndOfMemoryWithLenPastBounds_DeniedWithIoError() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let near_end_ptr = MEMORY_SIZE - 50;
    let len_past_end = 100; // well under MAX_PATH_LEN, but ptr + len overruns memory
    let result = instance
        .call_i32("read_at", &[near_end_ptr, len_past_end])
        .expect("read call succeeds");

    assert_eq!(
        result, IO_ERROR,
        "ptr + len exceeding the instance's memory size must be rejected"
    );
}

/// Staff-review finding 2: `path_len` above the PATH_MAX-style ceiling must be rejected even
/// when it comfortably fits inside the instance's actual memory.
#[test]
fn FsRead_PathLenExceedsMaxPathLen_DeniedWithIoError() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let result = instance
        .call_i32("read_at", &[PATH_OFFSET, MAX_PATH_LEN + 1])
        .expect("read call succeeds");

    assert_eq!(
        result, IO_ERROR,
        "path_len above the PATH_MAX-style ceiling must be rejected even though it fits in memory"
    );
}

/// Staff-review finding 3: a file larger than `buf_cap` must be rejected via `BUFFER_TOO_SMALL`
/// using the pre-read metadata length check, not read in full first.
#[test]
fn FsRead_FileLargerThanBufCap_ReturnsBufferTooSmall() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("big.bin");
    let contents = vec![b'x'; (BUF_CAP as usize) + 1];
    fs::write(&file_path, &contents).expect("fixture file writes");

    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    instance
        .write_memory(PATH_OFFSET, file_path.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");
    let path_len = file_path.to_string_lossy().len() as i32;

    let result = instance
        .call_i32("read", &[path_len])
        .expect("read call succeeds");

    assert_eq!(result, BUFFER_TOO_SMALL);
}

/// Staff-review finding 3: the previous implementation cast `contents.len() as i32` after reading
/// the whole file, which silently wraps negative for files >= 2^31 bytes and bypasses the
/// `BUFFER_TOO_SMALL` check entirely. A sparse file (created via `set_len`, so this test doesn't
/// actually write gigabytes to disk) that reports a length past `i32::MAX` must still be rejected
/// via the metadata check, fast and without the truncating cast ever running.
#[test]
fn FsRead_FileLargerThanI32Max_ReturnsBufferTooSmallWithoutTruncationWrap() {
    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("huge.bin");
    let file = fs::File::create(&file_path).expect("file creates");
    let past_i32_max = u64::from(u32::MAX) + 4096;
    file.set_len(past_i32_max).expect("sparse set_len succeeds");
    drop(file);

    let (manifest, policy) = manifest_and_policy("fs-read-skill", dir.path());
    let module_bytes = wat::parse_str(read_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    instance
        .write_memory(PATH_OFFSET, file_path.to_string_lossy().as_bytes())
        .expect("path writes into guest memory");
    let path_len = file_path.to_string_lossy().len() as i32;

    let result = instance
        .call_i32("read", &[path_len])
        .expect("read call succeeds");

    assert_eq!(
        result, BUFFER_TOO_SMALL,
        "a file whose length overflows i32 must be rejected via the metadata check, not truncated"
    );
}

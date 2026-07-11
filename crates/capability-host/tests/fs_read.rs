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

use pythia_capability_host::CapabilityHost;
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};

const PATH_OFFSET: i32 = 0;
const BUF_OFFSET: i32 = 4096;
const BUF_CAP: i32 = 4096;
const DENIED: i32 = -1;

/// Builds a probe module whose `read(path_len)` export calls `fs_read` with the path baked at
/// `PATH_OFFSET` (length supplied by the caller, so the same module works for paths of differing
/// length) and a fixed output buffer/capacity.
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

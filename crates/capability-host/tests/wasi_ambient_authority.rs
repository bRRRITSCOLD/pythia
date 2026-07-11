//! SR-4: a zero-capability manifest gets a WASI context with no preopens, no env passthrough,
//! and no ambient authority beyond what WASI structurally requires to function at all (clock,
//! random). A granted `fs:read` capability preopens *exactly* that scope -- never a convenience
//! preopen of cwd or home.
//!
//! These probe wasm modules call `wasi_snapshot_preview1` functions directly and return the
//! errno, so the assertions exercise the real mechanism (the `Linker` + `WasiCtx` built by
//! `CapabilityHost::instantiate`) rather than any crate-internal state.

#![allow(non_snake_case)]

use std::collections::HashMap;

use pythia_capability_host::CapabilityHost;
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};

const WASI_ERRNO_SUCCESS: i32 = 0;

fn zero_capability_manifest() -> (SkillManifest, PolicyFile) {
    (
        SkillManifest {
            name: "probe-skill".to_string(),
            requested: vec![],
        },
        PolicyFile::default(),
    )
}

/// `probe_fd(fd) -> errno` from `fd_prestat_get(fd, buf=8)`. WASI programs enumerate their
/// preopens by walking fd 3, 4, 5, ... until this returns `errno::badf` -- a nonzero result here
/// means "no preopen at this fd".
const PROBE_FD_WAT: &str = r#"
    (module
        (import "wasi_snapshot_preview1" "fd_prestat_get"
            (func $fd_prestat_get (param i32 i32) (result i32)))
        (memory (export "memory") 1)
        (func (export "probe_fd") (param $fd i32) (result i32)
            local.get $fd
            i32.const 8
            call $fd_prestat_get))
"#;

/// `probe_environ_count() -> i32` -- the env var count WASI reports via `environ_sizes_get`.
const PROBE_ENVIRON_COUNT_WAT: &str = r#"
    (module
        (import "wasi_snapshot_preview1" "environ_sizes_get"
            (func $environ_sizes_get (param i32 i32) (result i32)))
        (memory (export "memory") 1)
        (func (export "probe_environ_count") (result i32)
            i32.const 0
            i32.const 4
            call $environ_sizes_get
            drop
            i32.const 0
            i32.load))
"#;

/// `probe_clock() -> errno` from `clock_time_get(realtime, precision=1, buf=0)`.
const PROBE_CLOCK_WAT: &str = r#"
    (module
        (import "wasi_snapshot_preview1" "clock_time_get"
            (func $clock_time_get (param i32 i64 i32) (result i32)))
        (memory (export "memory") 1)
        (func (export "probe_clock") (result i32)
            i32.const 0
            i64.const 1
            i32.const 0
            call $clock_time_get))
"#;

/// `probe_random() -> errno` from `random_get(buf=0, len=8)`.
const PROBE_RANDOM_WAT: &str = r#"
    (module
        (import "wasi_snapshot_preview1" "random_get"
            (func $random_get (param i32 i32) (result i32)))
        (memory (export "memory") 1)
        (func (export "probe_random") (result i32)
            i32.const 0
            i32.const 8
            call $random_get))
"#;

#[test]
fn Wasi_ZeroCapabilityManifest_NoPreopensConfigured() {
    let module_bytes = wat::parse_str(PROBE_FD_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest();

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds (no capability imports referenced)");

    let errno = instance
        .call_i32("probe_fd", &[3])
        .expect("probe_fd call succeeds");

    assert_ne!(
        errno, WASI_ERRNO_SUCCESS,
        "expected no preopen at fd 3 for a zero-capability manifest"
    );
}

#[test]
fn Wasi_ZeroCapabilityManifest_NoEnvPassthrough() {
    let module_bytes = wat::parse_str(PROBE_ENVIRON_COUNT_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest();

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let env_count = instance
        .call_i32("probe_environ_count", &[])
        .expect("probe_environ_count call succeeds");

    assert_eq!(env_count, 0, "expected zero env vars passed through");
}

#[test]
fn Wasi_ZeroCapabilityManifest_NoAmbientClockOrRandomPassthroughBeyondWasiMinimum() {
    let (manifest, policy) = zero_capability_manifest();
    let host = CapabilityHost::new().expect("engine constructs");

    let clock_module = wat::parse_str(PROBE_CLOCK_WAT).expect("wat parses");
    let mut clock_instance = host
        .instantiate(&clock_module, &manifest, &policy)
        .expect("instantiation succeeds");
    let clock_errno = clock_instance
        .call_i32("probe_clock", &[])
        .expect("probe_clock call succeeds");
    assert_eq!(
        clock_errno, WASI_ERRNO_SUCCESS,
        "clock_time_get is WASI's structural minimum -- it must keep working with zero grants"
    );

    let random_module = wat::parse_str(PROBE_RANDOM_WAT).expect("wat parses");
    let mut random_instance = host
        .instantiate(&random_module, &manifest, &policy)
        .expect("instantiation succeeds");
    let random_errno = random_instance
        .call_i32("probe_random", &[])
        .expect("probe_random call succeeds");
    assert_eq!(
        random_errno, WASI_ERRNO_SUCCESS,
        "random_get is WASI's structural minimum -- it must keep working with zero grants"
    );
}

#[test]
fn Wasi_FsReadGrant_PreopensOnlyTheGrantedScopeNotCwdOrHome() {
    let granted_dir = tempfile::tempdir().expect("tempdir creates");
    let module_bytes = wat::parse_str(PROBE_FD_WAT).expect("wat parses");

    let capability = Capability::FsRead(granted_dir.path().to_path_buf());
    let manifest = SkillManifest {
        name: "fs-read-skill".to_string(),
        requested: vec![capability.clone()],
    };
    let mut skills = HashMap::new();
    skills.insert(
        "fs-read-skill".to_string(),
        HashMap::from([(capability, Decision::Grant)]),
    );
    let policy = PolicyFile { skills };

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let first_preopen_errno = instance
        .call_i32("probe_fd", &[3])
        .expect("probe_fd call succeeds");
    assert_eq!(
        first_preopen_errno, WASI_ERRNO_SUCCESS,
        "expected the granted fs:read scope preopened at fd 3"
    );

    let second_preopen_errno = instance
        .call_i32("probe_fd", &[4])
        .expect("probe_fd call succeeds");
    assert_ne!(
        second_preopen_errno, WASI_ERRNO_SUCCESS,
        "expected exactly one preopen -- no convenience preopen of cwd or home alongside it"
    );
}

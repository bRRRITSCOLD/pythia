//! `execute()`: the crate's public boundary (Task 9) -- assembles Tasks 5-8 into the one call the
//! kernel makes per tool dispatch. Every `HostError` variant `CapabilityHost::instantiate`/
//! `Instance::call_i32` can produce funnels into one of `ExecutionResult`'s three `status` values
//! (`Ok`, `Denied`, `ResourceLimitExceeded`) -- never a fourth, and never left for the caller to
//! interpret raw error text.
//!
//! `Execute_GrantedFsRead_ReturnsOkResultWithContent` compiles and runs the real `read-file` skill
//! (`skills/read-file`, built via `pythia-skill-sdk`) rather than a synthetic WAT fixture: this is
//! the test that exercises issue #32's fix end to end -- a real skill's compiled wasm imports
//! `pythia.fs_read`, and only links successfully if the host registers under the same `"pythia"`
//! module name the skill SDK's `#[link(wasm_import_module = "pythia")]` uses.

#![allow(non_snake_case)]

use std::collections::HashMap;
use std::path::PathBuf;
use std::process::Command;
use std::sync::Mutex;

use pythia_capability_host::{execute, ExecutionStatus};
use pythia_manifest::host_fn::{RESULT_TAG_ERR, RESULT_TAG_OK};
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};

/// `std::env::set_var`/`remove_var` mutate process-global state; serialize the one test in this
/// file that touches it (same rationale as `tests/secret_get.rs`'s `ENV_LOCK`).
static ENV_LOCK: Mutex<()> = Mutex::new(());

fn manifest_and_policy(
    skill_name: &str,
    entries: Vec<(Capability, Decision)>,
) -> (SkillManifest, PolicyFile) {
    let requested: Vec<Capability> = entries.iter().map(|(cap, _)| cap.clone()).collect();
    let manifest = SkillManifest {
        name: skill_name.to_string(),
        requested,
    };
    let mut skills = HashMap::new();
    skills.insert(skill_name.to_string(), entries.into_iter().collect());
    (manifest, PolicyFile { skills })
}

/// Builds the real `read-file` skill for `wasm32-wasip1` (same approach as
/// `skills/send-email/tests/build_wasm.rs`) and returns the compiled module bytes. Building a real
/// skill here -- not a synthetic WAT fixture -- is what makes this test exercise issue #32's fix:
/// `read-file` imports `pythia.fs_read` via `pythia-skill-sdk`'s `#[link(wasm_import_module =
/// "pythia")]`, so it only instantiates if the host's linker registers under that exact name.
fn build_read_file_wasm() -> Vec<u8> {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let repo_root = manifest_dir
        .parent()
        .expect("crates/capability-host must have a parent (crates/)")
        .parent()
        .expect("crates/ must have a parent (the repo root)");
    let skills_workspace_dir = repo_root.join("skills");

    let status = Command::new(env!("CARGO"))
        .arg("build")
        .arg("--package")
        .arg("read-file")
        .arg("--target")
        .arg("wasm32-wasip1")
        .current_dir(&skills_workspace_dir)
        .status()
        .expect("failed to invoke cargo build for wasm32-wasip1 target");
    assert!(
        status.success(),
        "cargo build --package read-file --target wasm32-wasip1 must succeed"
    );

    let wasm_path = skills_workspace_dir
        .join("target")
        .join("wasm32-wasip1")
        .join("debug")
        .join("read-file.wasm");
    std::fs::read(&wasm_path).unwrap_or_else(|e| panic!("reading {wasm_path:?}: {e}"))
}

#[test]
fn Execute_GrantedFsRead_ReturnsOkResultWithContent() {
    let module_bytes = build_read_file_wasm();

    let dir = tempfile::tempdir().expect("tempdir creates");
    let file_path = dir.path().join("notes.txt");
    std::fs::write(&file_path, b"hello from execute()").expect("fixture file writes");

    let (manifest, policy) = manifest_and_policy(
        "read-file",
        vec![(
            Capability::FsRead(dir.path().to_path_buf()),
            Decision::Grant,
        )],
    );

    let args = format!(r#"{{"path": "{}"}}"#, file_path.display()).into_bytes();

    let result = execute(&module_bytes, &manifest, &policy, &args, true);

    assert_eq!(
        result.status(),
        ExecutionStatus::Ok,
        "expected Ok, got a result with bytes {:?}",
        String::from_utf8_lossy(result.as_bytes())
    );
    assert!(
        result.is_tainted(),
        "expected the caller-supplied tainted=true flag to survive into the result"
    );

    let bytes = result.as_bytes();
    let (&tag, payload) = bytes.split_first().expect("result has at least a tag byte");
    assert_eq!(
        tag,
        RESULT_TAG_OK,
        "expected the skill's ok_result tag, got err tag with payload {:?}",
        String::from_utf8_lossy(payload)
    );
    assert_eq!(payload, b"hello from execute()");
}

#[test]
fn Execute_DeniedNetCapability_ReturnsDeniedResult_NoHostFunctionCalled() {
    // SR-2 assertions 1-3 at this crate's own public boundary: a module importing a capability
    // that was never granted has no import slot in the Linker at all (assertion 1), so
    // instantiation fails before any host function -- including this placeholder `net_smtp_send`
    // -- ever executes (assertion 2), and therefore no socket syscall (or any other host-function
    // side effect) can occur during the attempted call (assertion 3): there is no code path
    // between "denied" and "a host function ran."
    let wat = r#"
        (module
            (import "pythia" "net_smtp_send" (func $net_smtp_send))
            (memory (export "memory") 1)
            (func (export "pythia_alloc") (param $len i32) (result i32) i32.const 0)
            (func (export "run")
                (param $args_ptr i32) (param $args_len i32) (param $out_len_ptr i32) (result i32)
                unreachable))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");

    let (manifest, policy) = manifest_and_policy(
        "send-email-skill",
        vec![], // no policy entry at all for net:smtp -- fail-closed, denied.
    );
    let manifest = SkillManifest {
        requested: vec![Capability::Net("smtp".to_string())],
        ..manifest
    };

    let result = execute(&module_bytes, &manifest, &policy, b"", false);

    assert_eq!(
        result.status(),
        ExecutionStatus::Denied,
        "expected Denied, got a result with bytes {:?}",
        String::from_utf8_lossy(result.as_bytes())
    );
}

#[test]
fn Execute_ResourceLimitExceeded_ReturnsResourceLimitExceededResult_NotDeniedNotOk() {
    // Zero capabilities requested/granted -- the fuel ceiling must still terminate this instance
    // during its `run` call, distinctly from `Denied` (SR-6, distinct from SR-2's mechanism).
    let wat = r#"
        (module
            (memory (export "memory") 1)
            (func (export "pythia_alloc") (param $len i32) (result i32) i32.const 4096)
            (func (export "run")
                (param $args_ptr i32) (param $args_len i32) (param $out_len_ptr i32) (result i32)
                (loop $l br $l)
                i32.const 0))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");

    let manifest = SkillManifest {
        name: "infinite-loop-skill".to_string(),
        requested: vec![],
    };
    let policy = PolicyFile::default();

    let result = execute(&module_bytes, &manifest, &policy, b"", false);

    match result.status() {
        ExecutionStatus::ResourceLimitExceeded => {}
        ExecutionStatus::Denied => panic!(
            "expected ResourceLimitExceeded, got Denied with bytes {:?}",
            String::from_utf8_lossy(result.as_bytes())
        ),
        ExecutionStatus::Ok => panic!(
            "expected ResourceLimitExceeded, got Ok with bytes {:?}",
            String::from_utf8_lossy(result.as_bytes())
        ),
    }
}

#[test]
fn Execute_SecretCapabilityInvoked_ResultNeverContainsPlaintext() {
    let _guard = ENV_LOCK.lock().unwrap();
    let secret_name = "EXECUTE_TESTSECRET";
    let secret_value = "s3cr3t-leaked-if-not-redacted";
    std::env::set_var(format!("PYTHIA_SECRET_{secret_name}"), secret_value);

    // Imports `secret_get`, fetches `secret_name`'s plaintext, and echoes it back verbatim
    // (tag-prefixed like a real skill's `ok_result`) -- exactly the shape that would leak the
    // plaintext into `ExecutionResult` if `execute()`'s redaction path were ever skipped.
    let wat = format!(
        r#"
        (module
            (import "pythia" "secret_get"
                (func $secret_get (param i32 i32 i32) (result i32)))
            (memory (export "memory") 1)
            (global $next_free (mut i32) (i32.const 8192))
            (data (i32.const 0) "{secret_name}")
            (func $alloc (export "pythia_alloc") (param $len i32) (result i32)
                (local $ptr i32)
                (local.set $ptr (global.get $next_free))
                (global.set $next_free (i32.add (global.get $next_free) (local.get $len)))
                (local.get $ptr))
            (func (export "run")
                (param $args_ptr i32) (param $args_len i32) (param $out_len_ptr i32) (result i32)
                (local $secret_out_len_ptr i32)
                (local $secret_ptr i32)
                (local $secret_len i32)
                (local $result_ptr i32)
                (local $i i32)
                (local.set $secret_out_len_ptr (call $alloc (i32.const 4)))
                (local.set $secret_ptr
                    (call $secret_get
                        (i32.const 0)
                        (i32.const {secret_name_len})
                        (local.get $secret_out_len_ptr)))
                (local.set $secret_len (i32.load (local.get $secret_out_len_ptr)))
                (local.set $result_ptr
                    (call $alloc (i32.add (i32.const 1) (local.get $secret_len))))
                (i32.store8 (local.get $result_ptr) (i32.const 0))
                (local.set $i (i32.const 0))
                (block $done
                    (loop $copy
                        (br_if $done (i32.ge_u (local.get $i) (local.get $secret_len)))
                        (i32.store8
                            (i32.add (i32.add (local.get $result_ptr) (i32.const 1)) (local.get $i))
                            (i32.load8_u (i32.add (local.get $secret_ptr) (local.get $i))))
                        (local.set $i (i32.add (local.get $i) (i32.const 1)))
                        (br $copy)))
                (i32.store (local.get $out_len_ptr) (i32.add (i32.const 1) (local.get $secret_len)))
                (local.get $result_ptr)))
    "#,
        secret_name = secret_name,
        secret_name_len = secret_name.len(),
    );
    let module_bytes = wat::parse_str(&wat).expect("wat parses");

    let (manifest, policy) = manifest_and_policy(
        "secret-echo-skill",
        vec![(Capability::Secret(secret_name.to_string()), Decision::Grant)],
    );

    let result = execute(&module_bytes, &manifest, &policy, b"", false);

    std::env::remove_var(format!("PYTHIA_SECRET_{secret_name}"));

    assert_eq!(
        result.status(),
        ExecutionStatus::Ok,
        "expected Ok (the skill's own call succeeded), got a result with bytes {:?}",
        String::from_utf8_lossy(result.as_bytes())
    );

    let bytes = result.as_bytes();
    assert!(
        !bytes
            .windows(secret_value.len())
            .any(|window| window == secret_value.as_bytes()),
        "expected the plaintext secret value to be absent from the ExecutionResult, got {:?}",
        String::from_utf8_lossy(bytes)
    );
    let marker = format!("<redacted:secret:{secret_name}>");
    assert!(
        bytes
            .windows(marker.len())
            .any(|window| window == marker.as_bytes()),
        "expected a diagnosable redaction marker, got {:?}",
        String::from_utf8_lossy(bytes)
    );

    // Assert `RESULT_TAG_ERR` never appears misleadingly where `RESULT_TAG_OK` was expected --
    // guards the test fixture itself, not the crate under test.
    assert_ne!(bytes.first(), Some(&RESULT_TAG_ERR));
}

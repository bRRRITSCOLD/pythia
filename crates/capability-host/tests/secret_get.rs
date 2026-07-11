//! SR-5's mechanism half: a skill with a granted `secret:*` capability receives the plaintext
//! value *inside the sandbox* (it needs the value to act on it), while a skill that never had
//! the capability granted has no `secret_get` import at all -- the same import-absence mechanism
//! SR-2 proves for `net`. The redaction half (the value never survives into a public
//! `ExecutionResult`) is covered by `execute.rs`'s own `ExecutionResult_*` unit tests.
//!
//! Every probe module exports linear memory, bakes the requested secret's name into a data
//! segment, exports a trivial `pythia_alloc` (a fixed-offset bump allocator -- good enough for a
//! single call per test), and calls the `pythia::secret_get(name_ptr, name_len,
//! out_len_ptr) -> ptr` import, matching `pythia-skill-sdk::imports`'s guest-side declaration.

#![allow(non_snake_case)]

use std::collections::HashMap;
use std::sync::Mutex;

use pythia_capability_host::{CapabilityHost, HostError};
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};

/// `std::env::set_var`/`remove_var` mutate process-global state; serialize the tests in this
/// file that touch it so they can't race each other under cargo's default parallel test runner.
static ENV_LOCK: Mutex<()> = Mutex::new(());

const NAME_OFFSET: i32 = 0;
const OUT_LEN_OFFSET: i32 = 64;
const ALLOC_OFFSET: i32 = 4096;

/// Builds a probe module whose `get_secret` export calls `secret_get` with the name baked at
/// `NAME_OFFSET` (length supplied by the caller) and returns the pointer `secret_get` returned
/// (0 on denial/failure, per the ABI's null-pointer sentinel).
fn get_secret_probe_wat() -> String {
    format!(
        r#"
        (module
            (import "pythia" "secret_get"
                (func $secret_get (param i32 i32 i32) (result i32)))
            (memory (export "memory") 1)
            (func (export "pythia_alloc") (param $len i32) (result i32)
                i32.const {alloc_offset})
            (func (export "get_secret") (param $name_len i32) (result i32)
                i32.const {name_offset}
                local.get $name_len
                i32.const {out_len_offset}
                call $secret_get))
    "#,
        alloc_offset = ALLOC_OFFSET,
        name_offset = NAME_OFFSET,
        out_len_offset = OUT_LEN_OFFSET,
    )
}

fn manifest_and_policy(skill_name: &str, secret_name: &str) -> (SkillManifest, PolicyFile) {
    let capability = Capability::Secret(secret_name.to_string());
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

fn write_name(instance: &mut pythia_capability_host::Instance, name: &str) {
    instance
        .write_memory(NAME_OFFSET, name.as_bytes())
        .expect("name writes into guest memory");
}

#[test]
fn SecretGet_GrantedCapability_SkillReceivesPlaintextWithinSandbox() {
    let _guard = ENV_LOCK.lock().unwrap();
    let secret_name = "SECRETGET_GRANTED_API_KEY";
    let secret_value = "s3cr3t-value-42";
    std::env::set_var(format!("PYTHIA_SECRET_{secret_name}"), secret_value);

    let (manifest, policy) = manifest_and_policy("secret-skill", secret_name);
    let module_bytes = wat::parse_str(get_secret_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    write_name(&mut instance, secret_name);
    let ptr = instance
        .call_i32("get_secret", &[secret_name.len() as i32])
        .expect("get_secret call succeeds");

    assert_ne!(
        ptr, 0,
        "expected a non-null buffer pointer for a granted secret"
    );

    let out_len_bytes = instance
        .read_memory(OUT_LEN_OFFSET, 4)
        .expect("out_len reads");
    let out_len = u32::from_le_bytes(out_len_bytes.try_into().unwrap()) as i32;
    assert_eq!(out_len, secret_value.len() as i32);

    let received = instance
        .read_memory(ptr, out_len)
        .expect("plaintext buffer reads");
    assert_eq!(
        received,
        secret_value.as_bytes(),
        "expected the skill's own linear memory to contain the plaintext secret value"
    );

    std::env::remove_var(format!("PYTHIA_SECRET_{secret_name}"));
}

#[test]
fn SecretGet_WrongNameNotGranted_DeniedEvenWithSecretImportPresent() {
    let _guard = ENV_LOCK.lock().unwrap();
    let granted_name = "SECRETGET_SCOPE_GRANTED";
    let requested_name = "SECRETGET_SCOPE_NOT_GRANTED";
    std::env::set_var(
        format!("PYTHIA_SECRET_{requested_name}"),
        "should-never-be-returned",
    );

    let (manifest, policy) = manifest_and_policy("secret-skill", granted_name);
    let module_bytes = wat::parse_str(get_secret_probe_wat()).expect("wat parses");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds -- the skill's import slot exists because *some* secret was granted");

    write_name(&mut instance, requested_name);
    let ptr = instance
        .call_i32("get_secret", &[requested_name.len() as i32])
        .expect("get_secret call succeeds");

    assert_eq!(
        ptr, 0,
        "expected denial (null pointer) for a name not in the granted scope, even though the \
         secret_get import slot exists for a different granted secret name"
    );

    std::env::remove_var(format!("PYTHIA_SECRET_{requested_name}"));
}

#[test]
fn SecretGet_NotGranted_ImportAbsent() {
    let wat = r#"
        (module
            (import "pythia" "secret_get"
                (func $secret_get (param i32 i32 i32) (result i32)))
            (func (export "noop") (result i32) i32.const 0))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");

    let manifest = SkillManifest {
        name: "secret-skill".to_string(),
        requested: vec![Capability::Secret("API_KEY".to_string())],
    };
    // No policy entry at all for this skill -- fail-closed, so `secret:API_KEY` resolves to
    // `denied`, and the linker never gets a `secret_get` import slot at all.
    let policy = PolicyFile::default();

    let host = CapabilityHost::new().expect("engine constructs");
    let result = host.instantiate(&module_bytes, &manifest, &policy);

    match result {
        Err(HostError::CapabilityDenied(import)) => {
            assert_eq!(import, "pythia::secret_get");
        }
        Ok(_) => panic!("expected HostError::CapabilityDenied, got Ok"),
        Err(other) => panic!("expected HostError::CapabilityDenied, got {other}"),
    }
}

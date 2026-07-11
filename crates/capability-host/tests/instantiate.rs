//! SR-2's core mechanism (assertions 1+2): a capability that isn't granted has no import slot
//! in the `Linker`, so a module referencing it fails instantiation outright.

#![allow(non_snake_case)]

use std::collections::HashMap;

use pythia_capability_host::{CapabilityHost, HostError};
use pythia_manifest::{Capability, Decision, PolicyFile, SkillManifest};

fn policy_with(skill_name: &str, entries: Vec<(Capability, Decision)>) -> PolicyFile {
    let mut skills = HashMap::new();
    skills.insert(skill_name.to_string(), entries.into_iter().collect());
    PolicyFile { skills }
}

#[test]
fn Instantiate_ZeroCapabilityManifest_NoImportsLinked() {
    let wat = r#"
        (module
            (func (export "noop") (result i32) i32.const 0))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");

    let manifest = SkillManifest {
        name: "zero-cap-skill".to_string(),
        requested: vec![],
    };
    let policy = PolicyFile::default();

    let host = CapabilityHost::new().expect("engine constructs");
    let result = host.instantiate(&module_bytes, &manifest, &policy);

    assert!(
        result.is_ok(),
        "expected instantiation to succeed, got {:?}",
        result.err()
    );
}

#[test]
fn Instantiate_GrantedCapability_MatchingImportLinked() {
    // `secret_get`'s real body (Task 8) has type `(i32, i32, i32) -> i32` (name_ptr, name_len,
    // out_len_ptr -> ptr) -- the import declaration below must match it exactly or wasmtime's
    // own instantiation-time type check fails the module, independent of capability grants.
    let wat = r#"
        (module
            (import "pythia_host" "secret_get"
                (func $secret_get (param i32 i32 i32) (result i32)))
            (func (export "noop") (result i32) i32.const 0))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");

    let manifest = SkillManifest {
        name: "secret-skill".to_string(),
        requested: vec![Capability::Secret("api_key".to_string())],
    };
    let policy = policy_with(
        "secret-skill",
        vec![(Capability::Secret("api_key".to_string()), Decision::Grant)],
    );

    let host = CapabilityHost::new().expect("engine constructs");
    let result = host.instantiate(&module_bytes, &manifest, &policy);

    assert!(
        result.is_ok(),
        "expected instantiation to succeed, got {:?}",
        result.err()
    );
}

#[test]
fn Instantiate_RequestedCapabilityNotGranted_ImportAbsent_InstantiationFails() {
    let wat = r#"
        (module
            (import "pythia_host" "net_smtp_send" (func $net_smtp_send))
            (func (export "noop") (result i32) i32.const 0))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");

    let manifest = SkillManifest {
        name: "send-email-skill".to_string(),
        requested: vec![Capability::Net("smtp".to_string())],
    };
    // No policy entry at all for this skill -- fail-closed, so `net:smtp` resolves to `denied`,
    // and the linker never gets a `net_smtp_send` import slot.
    let policy = PolicyFile::default();

    let host = CapabilityHost::new().expect("engine constructs");
    let result = host.instantiate(&module_bytes, &manifest, &policy);

    match result {
        Err(HostError::CapabilityDenied(import)) => {
            assert_eq!(import, "pythia_host::net_smtp_send");
        }
        Ok(_) => panic!("expected HostError::CapabilityDenied, got Ok"),
        Err(other) => panic!("expected HostError::CapabilityDenied, got {other}"),
    }
}

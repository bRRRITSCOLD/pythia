//! Integration test: `Build_Wasm32Wasip1Target_ProducesValidModuleWithNetAndSecretImportsReferenced`
//!
//! Compiles this crate for `wasm32-wasip1` (ADR-0006) and inspects the
//! resulting module's import section. This is the acceptance test for
//! Task 13's actual purpose: the safety demo's import-absence assertion
//! (Task 18, SR-2) is only meaningful if `send-email.wasm` really imports
//! `net_smtp_send` and `secret_get` — this test is what proves that, on a
//! real compiled artifact rather than a synthetic WAT fixture.

use std::path::PathBuf;
use std::process::Command;

#[test]
fn build_wasm32_wasip1_target_produces_valid_module_with_net_and_secret_imports_referenced() {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    // This crate is a member of the `skills/` workspace, so cargo places
    // build output under the workspace root's `target/`, not this crate's
    // own directory.
    let workspace_dir = manifest_dir
        .parent()
        .expect("send-email crate must have a parent (the skills/ workspace root)")
        .to_path_buf();

    let status = Command::new(env!("CARGO"))
        .arg("build")
        .arg("--package")
        .arg("send-email")
        .arg("--target")
        .arg("wasm32-wasip1")
        .arg("--lib")
        .current_dir(&workspace_dir)
        .status()
        .expect("failed to invoke cargo build for wasm32-wasip1 target");
    assert!(
        status.success(),
        "cargo build --target wasm32-wasip1 must succeed"
    );

    let wasm_path = workspace_dir
        .join("target")
        .join("wasm32-wasip1")
        .join("debug")
        .join("send_email.wasm");
    let wasm_bytes =
        std::fs::read(&wasm_path).unwrap_or_else(|e| panic!("reading {wasm_path:?}: {e}"));

    // A valid module: wasmparser's Validator will reject anything malformed.
    wasmparser::Validator::new()
        .validate_all(&wasm_bytes)
        .expect("compiled module must be a valid wasm binary");

    let imported_function_names: Vec<String> = wasmparser::Parser::new(0)
        .parse_all(&wasm_bytes)
        .filter_map(|payload| payload.ok())
        .filter_map(|payload| match payload {
            wasmparser::Payload::ImportSection(reader) => Some(reader),
            _ => None,
        })
        .flat_map(|reader| reader.into_iter().filter_map(|import| import.ok()))
        .filter(|import| matches!(import.ty, wasmparser::TypeRef::Func(_)))
        .map(|import| import.name.to_string())
        .collect();

    assert!(
        imported_function_names.contains(&"secret_get".to_string()),
        "expected \"secret_get\" among imported functions, got: {imported_function_names:?}"
    );
    assert!(
        imported_function_names.contains(&"net_smtp_send".to_string()),
        "expected \"net_smtp_send\" among imported functions, got: {imported_function_names:?}"
    );
}

//! Task 1 acceptance tests: the two Cargo workspaces exist, compile, and are wired to the
//! right targets. These shell out to `cargo build` against the real workspace roots rather
//! than asserting on file structure, so they fail for the right reason (a broken build) and
//! stay true as crates gain real logic in later tasks.

use std::path::PathBuf;
use std::process::Command;

/// Repo root, derived from this crate's manifest dir (`crates/cli`) rather than the process's
/// current directory, so the test is stable regardless of where `cargo test` is invoked from.
fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent() // crates/
        .and_then(|p| p.parent()) // repo root
        .expect("crates/cli is two levels below the repo root")
        .to_path_buf()
}

// Test names match the plan's `Subject_Scenario_Expectation` identifiers verbatim
// (docs/superpowers/plans/pythia-vertical-slice.md, Task 1) for direct traceability.
#[allow(non_snake_case)]
#[test]
fn Build_RootWorkspace_CompilesCleanAllCrates() {
    let status = Command::new(env!("CARGO"))
        .args(["build", "--workspace"])
        .current_dir(repo_root())
        .status()
        .expect("failed to spawn `cargo build --workspace`");

    assert!(
        status.success(),
        "root workspace (`crates/*`) failed to build clean"
    );
}

#[allow(non_snake_case)]
#[test]
fn Build_SkillsWorkspace_CompilesToWasm32Wasip1() {
    let status = Command::new(env!("CARGO"))
        .args(["build", "--target", "wasm32-wasip1"])
        .current_dir(repo_root().join("skills"))
        .status()
        .expect("failed to spawn `cargo build --target wasm32-wasip1` in skills/");

    assert!(
        status.success(),
        "skills workspace failed to build clean for wasm32-wasip1 \
         (is the target installed? `rustup target add wasm32-wasip1`)"
    );
}

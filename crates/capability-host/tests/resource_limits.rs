//! SR-6: every skill instantiation carries an explicit fuel budget, linear-memory ceiling, and
//! table-element ceiling. Exceeding any of the three force-terminates the instance -- surfaced as
//! a distinct `HostError::ResourceLimitExceeded`, never conflated with `HostError::CapabilityDenied`
//! or a generic `HostError::Wasmtime` -- and control returns to the caller instead of hanging the
//! (single-threaded) kernel loop.
//!
//! The table ceiling exists because a skill needs no capability grant to declare its own internal
//! `(table N funcref)`: unlike linear memory there is no host-managed resource being referenced, so
//! import-absence (SR-2's mechanism) has nothing to gate. Left unbounded, a capability-free skill
//! could commit tens of gigabytes with a single `table.grow` instruction (one fuel unit),
//! OOM-killing the host process before fuel or the memory ceiling ever engaged.

#![allow(non_snake_case)]

use std::sync::mpsc;
use std::time::Duration;

use pythia_capability_host::{CapabilityHost, HostError};
use pythia_manifest::{PolicyFile, SkillManifest};

/// The test's own patience for "did control come back at all" -- generous relative to how fast
/// the fuel/memory mechanisms should actually terminate the instance, but far short of forever,
/// so a regression that reintroduces a real hang fails the test suite instead of wedging it.
const HANG_GUARD_TIMEOUT: Duration = Duration::from_secs(10);

fn zero_capability_manifest(name: &str) -> (SkillManifest, PolicyFile) {
    (
        SkillManifest {
            name: name.to_string(),
            requested: vec![],
        },
        PolicyFile::default(),
    )
}

/// An unconditional infinite loop -- never grows memory, so this exercises the fuel mechanism in
/// isolation from the memory ceiling.
const INFINITE_LOOP_WAT: &str = r#"
    (module
        (func (export "loop_forever") (result i32)
            (loop $l
                br $l)
            i32.const 0))
"#;

/// Grows linear memory by one page (64 KiB) every iteration of an unconditional loop, without
/// bound -- exercises the memory ceiling well before fuel could plausibly run out (256 iterations
/// to cross a 16 MiB ceiling).
const UNBOUNDED_GROW_WAT: &str = r#"
    (module
        (memory (export "memory") 1)
        (func (export "grow_forever") (result i32)
            (loop $l
                (drop (memory.grow (i32.const 1)))
                br $l)
            i32.const 0))
"#;

/// Declares its *own* internal funcref table (no import, no capability grant possible or needed)
/// and grows it, in a single instruction, by 20,000 elements -- past `TABLE_ELEMENT_LIMIT`
/// (10,000) but trivially small in fuel terms (one `table.grow`). This is exactly the SR-6 host-OOM
/// shape the table ceiling closes: with an unbounded `table_growing`, a request like this (or a
/// multi-billion-element one) would previously have been granted outright.
const TABLE_GROW_OVER_CAP_WAT: &str = r#"
    (module
        (table (export "table") 1 funcref)
        (func (export "grow_over_cap") (result i32)
            (table.grow (ref.null func) (i32.const 20000))))
"#;

/// Grows the same kind of self-declared table, but by an amount well inside `TABLE_ELEMENT_LIMIT`
/// -- exercises that legitimate, bounded table growth is unaffected by the new ceiling.
const TABLE_GROW_WITHIN_CAP_WAT: &str = r#"
    (module
        (table (export "table") 1 funcref)
        (func (export "grow_within_cap") (result i32)
            (table.grow (ref.null func) (i32.const 100))))
"#;

#[test]
fn Fuel_InfiniteLoopSkill_ForceTerminatedWithinBudget() {
    let module_bytes = wat::parse_str(INFINITE_LOOP_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest("infinite-loop-skill");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds (loop only runs once called)");

    let result = instance.call_i32("loop_forever", &[]);

    match result {
        Err(HostError::ResourceLimitExceeded(_)) => {}
        Ok(value) => panic!("expected ResourceLimitExceeded, got Ok({value})"),
        Err(other) => panic!("expected ResourceLimitExceeded, got {other}"),
    }
}

#[test]
fn Memory_UnboundedGrowSkill_ForceTerminatedAtCeiling() {
    let module_bytes = wat::parse_str(UNBOUNDED_GROW_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest("unbounded-grow-skill");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds (growth only happens once called)");

    let result = instance.call_i32("grow_forever", &[]);

    match result {
        Err(HostError::ResourceLimitExceeded(_)) => {}
        Ok(value) => panic!("expected ResourceLimitExceeded, got Ok({value})"),
        Err(other) => panic!("expected ResourceLimitExceeded, got {other}"),
    }
}

#[test]
fn ResourceLimitExceeded_KernelLoopProceeds_DoesNotHang() {
    // Runs the fuel-exhaustion call on a background thread and waits on a bounded channel recv
    // rather than calling it inline: if the fuel mechanism regressed back to a real hang, this
    // test fails loudly at `HANG_GUARD_TIMEOUT` instead of wedging the whole test binary forever.
    let (tx, rx) = mpsc::channel();

    std::thread::spawn(move || {
        let module_bytes = wat::parse_str(INFINITE_LOOP_WAT).expect("wat parses");
        let (manifest, policy) = zero_capability_manifest("infinite-loop-skill-hang-guard");

        let host = CapabilityHost::new().expect("engine constructs");
        let mut instance = host
            .instantiate(&module_bytes, &manifest, &policy)
            .expect("instantiation succeeds");

        let result = instance.call_i32("loop_forever", &[]);
        // The send can only fail if the receiver already timed out and dropped -- fine either
        // way, the assertion below is what matters.
        let _ = tx.send(result.is_err());
    });

    match rx.recv_timeout(HANG_GUARD_TIMEOUT) {
        Ok(call_errored) => assert!(
            call_errored,
            "expected the fuel-exhausted call to return an error, not Ok"
        ),
        Err(_) => panic!(
            "kernel loop hung: the fuel-limited call did not return control within {HANG_GUARD_TIMEOUT:?}"
        ),
    }
}

#[test]
fn ResourceLimitExceeded_SurfacedAsDistinctHostErrorVariant() {
    let module_bytes = wat::parse_str(INFINITE_LOOP_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest("infinite-loop-skill-variant-check");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let result = instance.call_i32("loop_forever", &[]);

    // Not just "is an Err" -- must be the specific variant, distinguishable from
    // `CapabilityDenied` and from an opaque `Wasmtime` catch-all, so Task 9/15 can map it to
    // `effect_result.status = "resource_limit_exceeded"` distinctly from `"denied"`.
    match result {
        Err(HostError::ResourceLimitExceeded(reason)) => {
            assert!(
                !reason.is_empty(),
                "expected a non-empty reason describing which limit was exceeded"
            );
        }
        Err(HostError::CapabilityDenied(import)) => {
            panic!("expected ResourceLimitExceeded, got CapabilityDenied({import})")
        }
        Err(HostError::Wasmtime(err)) => {
            panic!("expected ResourceLimitExceeded, got opaque Wasmtime({err})")
        }
        Ok(value) => panic!("expected ResourceLimitExceeded, got Ok({value})"),
    }
}

#[test]
fn Table_SelfDeclaredTableGrownOverCap_ForceTerminatedAsResourceLimitExceeded() {
    // A skill requesting zero capabilities can still declare its own table -- no import slot, no
    // grant needed -- so this reuses `zero_capability_manifest` deliberately, to demonstrate the
    // ceiling holds even against a skill the capability system has nothing to deny.
    let module_bytes = wat::parse_str(TABLE_GROW_OVER_CAP_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest("table-over-cap-skill");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds (the module's own table starts at 1 element)");

    let result = instance.call_i32("grow_over_cap", &[]);

    match result {
        Err(HostError::ResourceLimitExceeded(reason)) => {
            assert!(
                reason.to_lowercase().contains("table"),
                "expected the reason to mention the table ceiling, got: {reason}"
            );
        }
        Ok(value) => panic!("expected ResourceLimitExceeded, got Ok({value})"),
        Err(other) => panic!("expected ResourceLimitExceeded, got {other}"),
    }
}

#[test]
fn Table_SelfDeclaredTableGrownWithinCap_Succeeds() {
    let module_bytes = wat::parse_str(TABLE_GROW_WITHIN_CAP_WAT).expect("wat parses");
    let (manifest, policy) = zero_capability_manifest("table-within-cap-skill");

    let host = CapabilityHost::new().expect("engine constructs");
    let mut instance = host
        .instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds");

    let result = instance.call_i32("grow_within_cap", &[]);

    match result {
        Ok(previous_size) => assert!(
            previous_size >= 0,
            "table.grow should report the table's prior size on success, got {previous_size}"
        ),
        Err(other) => panic!("expected growth within the cap to succeed, got {other}"),
    }
}

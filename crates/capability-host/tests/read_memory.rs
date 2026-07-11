//! `Instance::read_memory` must reject an out-of-bounds `len` *before* allocating a host-side
//! buffer for it, mirroring `host_fns::fs::read_guest_path`'s ceiling check (#36 item 1): a
//! guest-controlled `len` up to `i32::MAX` must never reach the `vec![0u8; len]` allocation.

#![allow(non_snake_case)]

use pythia_capability_host::CapabilityHost;
use pythia_manifest::{PolicyFile, SkillManifest};

/// One wasm page (64 KiB) -- the size of the `(memory (export "memory") 1)` declared below.
const MEMORY_SIZE: i32 = 65536;

fn instantiate_with_memory() -> pythia_capability_host::Instance {
    let wat = r#"
        (module
            (memory (export "memory") 1)
            (func (export "noop") (result i32) i32.const 0))
    "#;
    let module_bytes = wat::parse_str(wat).expect("wat parses");
    let manifest = SkillManifest {
        name: "read-memory-skill".to_string(),
        requested: vec![],
    };
    let policy = PolicyFile::default();

    let host = CapabilityHost::new().expect("engine constructs");
    host.instantiate(&module_bytes, &manifest, &policy)
        .expect("instantiation succeeds")
}

#[test]
fn ReadMemory_LenExceedingMemorySize_RejectedBeforeAllocating() {
    let mut instance = instantiate_with_memory();

    // Far larger than the one-page (64 KiB) linear memory the probe module declares, but nowhere
    // near large enough to itself exhaust host memory if it were (wrongly) allocated first --
    // the point is that this must be rejected by the bounds check, not merely survive because
    // the allocation happened to succeed.
    let oversized_len = MEMORY_SIZE * 4;

    let result = instance.read_memory(0, oversized_len);

    assert!(
        result.is_err(),
        "expected a len far exceeding memory_size to be rejected, got {:?}",
        result.ok()
    );
}

#[test]
fn ReadMemory_OffsetPlusLenOverflows_Rejected() {
    let mut instance = instantiate_with_memory();

    // offset + len overflows i32's positive range even though each individual value on its own
    // could pass a naive `len > memory_size` check.
    let result = instance.read_memory(i32::MAX - 1, i32::MAX - 1);

    assert!(
        result.is_err(),
        "expected offset+len overflow to be rejected, got {:?}",
        result.ok()
    );
}

#[test]
fn ReadMemory_ValidOffsetAndLen_RoundTripsThroughMemory() {
    let mut instance = instantiate_with_memory();
    let payload = b"hello capability host";

    instance
        .write_memory(0, payload)
        .expect("write within bounds succeeds");
    let read_back = instance
        .read_memory(0, payload.len() as i32)
        .expect("read within bounds succeeds");

    assert_eq!(read_back, payload);
}

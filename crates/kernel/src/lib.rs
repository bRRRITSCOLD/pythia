//! `pythia-kernel`: turn-loop orchestration, typed event vocabulary, replay-on-resume,
//! context-window compaction.
//!
//! This task (Task 14) lands only the typed event vocabulary and its pure translation to/from
//! `pythia_eventlog::EventRow` — no I/O, no turn-loop state machine yet (that's Task 15).

mod event;

pub use event::{KernelEvent, ToolCall, TranslateError};

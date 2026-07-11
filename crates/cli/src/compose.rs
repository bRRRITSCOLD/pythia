//! Composition root: the one place in the workspace that knows every concrete type
//! (`OllamaProvider`, the SQLite-backed `EventLog`, manifest/policy files) and wires them into a
//! `Kernel` (architecture doc §2). Everything here is config-driven — base URL, model, DB path,
//! and policy path are all read from the environment (BYO-endpoint, never hardcoded), matching
//! the BYO-key constraint ADR-0005 locks for every provider implementation.
//!
//! This slice registers no skills yet (`skills: HashMap::new()`) — `pythia-cli`'s dependency
//! graph for this task (plan Task 16: blocked by 3/9/10/15, *not* 12/13) carries no compiled
//! skill module to wire in. A skill-registration path is exactly what Tasks 17/18's demo tests
//! add on top of this composition root when they need `read_file` dispatchable end to end; this
//! module deliberately doesn't pre-guess that shape (YAGNI).

use std::collections::HashMap;
use std::env;
use std::fs;
use std::path::PathBuf;

use pythia_eventlog::{EventLog, EventLogError};
use pythia_kernel::{Kernel, SkillConfig};
use pythia_manifest::PolicyFile;
use pythia_provider_ollama::OllamaProvider;

/// Environment variable the SQLite event-log path is read from.
pub const ENV_DB_PATH: &str = "PYTHIA_DB_PATH";
/// Environment variable the Ollama server's base URL is read from.
pub const ENV_OLLAMA_BASE_URL: &str = "PYTHIA_OLLAMA_BASE_URL";
/// Environment variable the Ollama model name is read from.
pub const ENV_OLLAMA_MODEL: &str = "PYTHIA_OLLAMA_MODEL";
/// Environment variable an optional policy TOML file's path is read from. Absent means "no
/// grants" (`PolicyFile::default()`), which is the fail-closed default the capability model
/// requires anyway (architecture doc §5) — an unset policy path is not a startup error.
pub const ENV_POLICY_PATH: &str = "PYTHIA_POLICY_PATH";

const DEFAULT_DB_PATH: &str = "pythia.db";
const DEFAULT_OLLAMA_BASE_URL: &str = "http://localhost:11434";

/// Resolved configuration for one `run()` invocation. Reading this out of `Config::from_env()`
/// separately from acting on it keeps the env-var lookup itself trivially testable and keeps
/// `build_kernel` a pure function of its input rather than a hidden global-state reader.
#[derive(Debug, Clone)]
pub struct Config {
    pub db_path: PathBuf,
    pub ollama_base_url: String,
    pub ollama_model: Option<String>,
    pub policy_path: Option<PathBuf>,
}

impl Config {
    /// Reads configuration from the environment, falling back to locally-sane defaults (a
    /// relative `pythia.db` file, `http://localhost:11434`) for anything unset. Never a
    /// hardcoded remote endpoint or credential — the BYO-endpoint constraint holds even for the
    /// defaults, since `localhost` is not a hosted, subscription-authenticated service.
    ///
    /// Thin wrapper over the pure `from_vars` — all fallback logic lives there so it can be
    /// exercised with a stub lookup instead of mutating real process-global env state.
    pub fn from_env() -> Self {
        Self::from_vars(|key| env::var(key).ok())
    }

    /// Pure lookup-driven config resolution: `lookup` maps an `ENV_*` key to its raw string value
    /// (or `None` if unset), and this applies the same defaulting logic `from_env` used to apply
    /// directly against `std::env::var`. Keeping this free of any real env access makes the
    /// fallback path deterministically and parallel-safe testable (no process-global state).
    pub fn from_vars(lookup: impl Fn(&str) -> Option<String>) -> Self {
        Self {
            db_path: lookup(ENV_DB_PATH)
                .map(PathBuf::from)
                .unwrap_or_else(|| PathBuf::from(DEFAULT_DB_PATH)),
            ollama_base_url: lookup(ENV_OLLAMA_BASE_URL)
                .unwrap_or_else(|| DEFAULT_OLLAMA_BASE_URL.to_string()),
            ollama_model: lookup(ENV_OLLAMA_MODEL),
            policy_path: lookup(ENV_POLICY_PATH).map(PathBuf::from),
        }
    }
}

/// Failures composing the real `Kernel<OllamaProvider>`. Distinct from `KernelError` (which
/// covers failures *running* a turn) — these are all startup-time wiring failures.
#[derive(Debug, thiserror::Error)]
pub enum ComposeError {
    #[error("failed to open event log at {path}: {source}")]
    EventLog {
        path: PathBuf,
        #[source]
        source: EventLogError,
    },
    #[error("failed to read policy file at {path}: {source}")]
    PolicyRead {
        path: PathBuf,
        #[source]
        source: std::io::Error,
    },
    #[error("failed to parse policy file at {path}: {source}")]
    PolicyParse {
        path: PathBuf,
        #[source]
        source: toml::de::Error,
    },
}

/// Wires `config` into a real `Kernel<OllamaProvider>` — the one function that actually
/// constructs every concrete type this crate is the composition root for. Building the
/// `OllamaProvider` performs no network I/O (it only configures a `reqwest::Client`), so this
/// function is safe to call without a live Ollama server; the network dependency is confined to
/// `Kernel::run_turn`/`resume` later making an actual `Provider::request` call.
pub fn build_kernel(config: &Config) -> Result<Kernel<OllamaProvider>, ComposeError> {
    let eventlog = EventLog::open(&config.db_path).map_err(|source| ComposeError::EventLog {
        path: config.db_path.clone(),
        source,
    })?;

    let provider = match &config.ollama_model {
        Some(model) => OllamaProvider::with_model(config.ollama_base_url.clone(), model.clone()),
        None => OllamaProvider::new(config.ollama_base_url.clone()),
    };

    let policy = load_policy(config.policy_path.as_deref())?;

    let skills: HashMap<String, SkillConfig> = HashMap::new();

    Ok(Kernel::new(eventlog, provider, policy, skills))
}

fn load_policy(policy_path: Option<&std::path::Path>) -> Result<PolicyFile, ComposeError> {
    let Some(path) = policy_path else {
        return Ok(PolicyFile::default());
    };

    let raw = fs::read_to_string(path).map_err(|source| ComposeError::PolicyRead {
        path: path.to_path_buf(),
        source,
    })?;
    toml::from_str(&raw).map_err(|source| ComposeError::PolicyParse {
        path: path.to_path_buf(),
        source,
    })
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    #[test]
    fn FromVars_AllUnset_FallsBackToDefaults() {
        let config = Config::from_vars(|_| None);

        assert_eq!(config.db_path, PathBuf::from("pythia.db"));
        assert_eq!(config.ollama_base_url, "http://localhost:11434");
        assert_eq!(config.ollama_model, None);
        assert_eq!(config.policy_path, None);
    }

    #[test]
    fn FromVars_AllSet_ReadsEachVar() {
        let config = Config::from_vars(|key| match key {
            ENV_DB_PATH => Some("custom.db".to_string()),
            ENV_OLLAMA_BASE_URL => Some("http://example.internal:9999".to_string()),
            ENV_OLLAMA_MODEL => Some("llama3".to_string()),
            ENV_POLICY_PATH => Some("policy.toml".to_string()),
            _ => None,
        });

        assert_eq!(config.db_path, PathBuf::from("custom.db"));
        assert_eq!(config.ollama_base_url, "http://example.internal:9999");
        assert_eq!(config.ollama_model, Some("llama3".to_string()));
        assert_eq!(config.policy_path, Some(PathBuf::from("policy.toml")));
    }

    #[test]
    fn FromVars_PolicyPathSet_ParsedAsPathBuf() {
        let config = Config::from_vars(|key| {
            (key == ENV_POLICY_PATH).then(|| "/etc/pythia/policy.toml".to_string())
        });

        assert_eq!(
            config.policy_path,
            Some(PathBuf::from("/etc/pythia/policy.toml"))
        );
    }

    #[test]
    fn LoadPolicy_NoPathGiven_ReturnsDefaultEmptyPolicy() {
        let policy = load_policy(None).expect("no policy path must not error");

        assert_eq!(policy, PolicyFile::default());
    }

    #[test]
    fn LoadPolicy_ValidTomlFile_Parses() {
        let dir = tempfile::tempdir().expect("tempdir creates");
        let path = dir.path().join("policy.toml");
        std::fs::write(&path, "[skills.read_file]\n\"fs:read:/tmp\" = \"grant\"\n")
            .expect("fixture file writes");

        let policy = load_policy(Some(&path)).expect("valid policy file must parse");

        assert!(!policy.skills.is_empty());
    }

    #[test]
    fn LoadPolicy_MissingFile_ErrorsNotPanic() {
        let result = load_policy(Some(std::path::Path::new(
            "/nonexistent/pythia-policy.toml",
        )));

        assert!(matches!(result, Err(ComposeError::PolicyRead { .. })));
    }

    #[test]
    fn BuildKernel_ValidConfig_Succeeds() {
        let dir = tempfile::tempdir().expect("tempdir creates");
        let config = Config {
            db_path: dir.path().join("test.db"),
            ollama_base_url: DEFAULT_OLLAMA_BASE_URL.to_string(),
            ollama_model: None,
            policy_path: None,
        };

        // Building the kernel opens the SQLite file and configures (but does not call) the
        // HTTP client — no live Ollama server required for this assertion.
        let result = build_kernel(&config);

        assert!(result.is_ok());
    }
}

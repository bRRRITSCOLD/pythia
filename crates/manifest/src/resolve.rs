//! The pure resolution function: "what's requested" + "what's authorized" -> "what gets
//! linked". Fail-closed by construction — every branch that isn't an explicit `Grant` ends up
//! denied or, for wildcards, forced through `Prompt`. No wasmtime dependency; this is plain
//! data in, plain data out, fully unit-testable without a sandbox.

use crate::capability::Capability;
use crate::policy::{Decision, PolicyFile};

/// The outcome of resolving one skill's requested capabilities against a policy. Every
/// capability listed here is drawn only from `requested` — resolution narrows, it never widens
/// past what was asked for, no matter what the policy separately authorizes.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ResolvedGrants {
    /// Capabilities to actually link into the sandbox.
    pub granted: Vec<Capability>,
    /// Capabilities that require an interactive prompt before they can be granted — includes
    /// every wildcard request, regardless of policy.
    pub prompt: Vec<Capability>,
    /// Capabilities that are not granted: explicit `Deny`, or no policy entry at all
    /// (fail-closed — unlisted is denied, not silently ignored).
    pub denied: Vec<Capability>,
}

impl ResolvedGrants {
    pub fn is_granted(&self, capability: &Capability) -> bool {
        self.granted.contains(capability)
    }
}

/// Resolve `requested` capabilities for `skill_name` against `policy`.
///
/// - A wildcard request (`fs:read:*`, `net:*`) never resolves directly to `granted`, even if
///   the policy has a wildcard `Grant` entry for it — it always routes to `prompt` (SR-1's
///   wildcard clause).
/// - A concrete request with an explicit `Decision::Grant` resolves to `granted`.
/// - A concrete request with an explicit `Decision::Deny`, or with no policy entry at all,
///   resolves to `denied` — fail-closed: absence of authorization is treated the same as
///   explicit refusal.
/// - Capabilities the policy grants but the skill never requested are never considered; only
///   `requested` capabilities can appear in the result.
pub fn resolve(requested: &[Capability], policy: &PolicyFile, skill_name: &str) -> ResolvedGrants {
    let mut result = ResolvedGrants::default();

    for capability in requested {
        if capability.is_wildcard() {
            result.prompt.push(capability.clone());
            continue;
        }

        match policy.decision(skill_name, capability) {
            Some(Decision::Grant) => result.granted.push(capability.clone()),
            Some(Decision::Prompt) => result.prompt.push(capability.clone()),
            Some(Decision::Deny) | None => result.denied.push(capability.clone()),
        }
    }

    result
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::path::PathBuf;

    fn policy_with(skill_name: &str, entries: Vec<(Capability, Decision)>) -> PolicyFile {
        let mut skills = HashMap::new();
        skills.insert(skill_name.to_string(), entries.into_iter().collect());
        PolicyFile { skills }
    }

    #[test]
    fn Resolve_UnlistedCapability_DeniedNotGranted() {
        let requested = vec![Capability::Net("smtp".to_string())];
        let policy = PolicyFile::default(); // no entry at all for this skill/capability

        let result = resolve(&requested, &policy, "read-file");

        assert!(result.denied.contains(&Capability::Net("smtp".to_string())));
        assert!(result.granted.is_empty());
        assert!(result.prompt.is_empty());
    }

    #[test]
    fn Resolve_ExplicitDeny_Denied() {
        let cap = Capability::Net("smtp".to_string());
        let requested = vec![cap.clone()];
        let policy = policy_with("read-file", vec![(cap.clone(), Decision::Deny)]);

        let result = resolve(&requested, &policy, "read-file");

        assert!(result.denied.contains(&cap));
        assert!(!result.is_granted(&cap));
    }

    #[test]
    fn Resolve_ExplicitGrant_Granted() {
        let cap = Capability::FsRead(PathBuf::from("/tmp/input.txt"));
        let requested = vec![cap.clone()];
        let policy = policy_with("read-file", vec![(cap.clone(), Decision::Grant)]);

        let result = resolve(&requested, &policy, "read-file");

        assert!(result.is_granted(&cap));
        assert!(result.denied.is_empty());
    }

    #[test]
    fn Resolve_WildcardRequestWithWildcardPolicyGrant_RoutesToPromptNeverAutoGranted() {
        let requested = vec![Capability::NetWildcard];
        // Even an explicit wildcard Grant in the policy must not auto-grant a wildcard request.
        let policy = policy_with("read-file", vec![(Capability::NetWildcard, Decision::Grant)]);

        let result = resolve(&requested, &policy, "read-file");

        assert!(result.prompt.contains(&Capability::NetWildcard));
        assert!(result.granted.is_empty());
    }

    #[test]
    fn Resolve_RequestedButNotInManifest_Ignored() {
        // Policy grants a capability the skill never requested; it must not appear anywhere
        // in the resolution — resolve() only ever narrows `requested`, never widens past it.
        let unrequested_but_granted = Capability::Secret("api_key".to_string());
        let requested: Vec<Capability> = vec![];
        let policy = policy_with(
            "read-file",
            vec![(unrequested_but_granted.clone(), Decision::Grant)],
        );

        let result = resolve(&requested, &policy, "read-file");

        assert!(result.granted.is_empty());
        assert!(result.prompt.is_empty());
        assert!(result.denied.is_empty());
        assert!(!result.is_granted(&unrequested_but_granted));
    }
}

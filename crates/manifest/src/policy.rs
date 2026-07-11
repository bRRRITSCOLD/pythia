//! `PolicyFile`: the *authority* half of the capability model. Entries are keyed
//! `(skill_name, Capability) -> Decision`. Absence of an entry is a distinct state from an
//! explicit `Decision::Deny` at the type level — `decision()` returns `Option<Decision>`, so
//! "unlisted" and "denied" stay separable in code even though `resolve()` treats them
//! identically (fail-closed).

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

use crate::capability::Capability;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Decision {
    Grant,
    Deny,
    Prompt,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PolicyFile {
    #[serde(default)]
    pub skills: HashMap<String, HashMap<Capability, Decision>>,
}

impl PolicyFile {
    /// The authorized decision for `skill_name` requesting `capability`, or `None` if the
    /// policy has no opinion on it at all (an unlisted capability — distinct from an explicit
    /// `Deny`, though `resolve()` treats both as denied).
    pub fn decision(&self, skill_name: &str, capability: &Capability) -> Option<Decision> {
        self.skills.get(skill_name)?.get(capability).copied()
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn Policy_ParseValidToml_RoundTrips() {
        let toml_src = r#"
            [skills.read-file]
            "fs:read:/tmp/input.txt" = "grant"
            "net:smtp" = "deny"
        "#;

        let policy: PolicyFile = toml::from_str(toml_src).expect("valid policy parses");

        assert_eq!(
            policy.decision(
                "read-file",
                &Capability::FsRead(PathBuf::from("/tmp/input.txt"))
            ),
            Some(Decision::Grant)
        );
        assert_eq!(
            policy.decision("read-file", &Capability::Net("smtp".to_string())),
            Some(Decision::Deny)
        );

        let serialized = toml::to_string(&policy).expect("policy serializes");
        let round_tripped: PolicyFile =
            toml::from_str(&serialized).expect("serialized policy re-parses");
        assert_eq!(round_tripped, policy);
    }

    #[test]
    fn Policy_UnlistedCapability_ReturnsNoneNotDeny() {
        let policy = PolicyFile::default();

        let result = policy.decision("read-file", &Capability::Net("smtp".to_string()));

        assert_eq!(result, None);
    }
}

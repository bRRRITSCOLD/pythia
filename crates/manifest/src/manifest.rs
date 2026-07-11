//! `SkillManifest`: the *request* half of the capability model. A skill declares what it
//! wants; it never declares what it gets — that's the policy's job (see `policy.rs`).

use serde::{Deserialize, Serialize};

use crate::capability::Capability;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SkillManifest {
    pub name: String,
    #[serde(default)]
    pub requested: Vec<Capability>,
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn Manifest_ParseValidToml_RoundTrips() {
        let toml_src = r#"
            name = "read-file"
            requested = ["fs:read:/tmp/input.txt", "net:smtp"]
        "#;

        let manifest: SkillManifest = toml::from_str(toml_src).expect("valid manifest parses");

        assert_eq!(manifest.name, "read-file");
        assert_eq!(
            manifest.requested,
            vec![
                Capability::FsRead(PathBuf::from("/tmp/input.txt")),
                Capability::Net("smtp".to_string()),
            ]
        );

        let serialized = toml::to_string(&manifest).expect("manifest serializes");
        let round_tripped: SkillManifest =
            toml::from_str(&serialized).expect("serialized manifest re-parses");
        assert_eq!(round_tripped, manifest);
    }

    #[test]
    fn Manifest_ParseMalformedCapabilityString_ErrorsNotPanics() {
        let toml_src = r#"
            name = "read-file"
            requested = ["not-a-real-capability"]
        "#;

        let result: Result<SkillManifest, _> = toml::from_str(toml_src);

        assert!(result.is_err());
    }

    #[test]
    fn Manifest_ParseMissingRequested_DefaultsToEmpty() {
        let toml_src = r#"name = "no-capabilities""#;

        let manifest: SkillManifest = toml::from_str(toml_src).expect("valid manifest parses");

        assert_eq!(manifest.requested, Vec::new());
    }
}

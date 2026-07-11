//! `declare_manifest!` — the skill-authoring entry point for declaring a manifest.
//!
//! A skill states its name and requested capabilities; this renders them into the TOML shape
//! `pythia_manifest::SkillManifest` parses, reusing that crate's `Capability`/`SkillManifest`
//! types directly rather than re-deriving the schema here.

use pythia_manifest::{Capability, CapabilityParseError, SkillManifest};

/// Builds a `SkillManifest` from a skill name and its requested capability strings (e.g.
/// `"fs:read:/notes"`), then renders it to TOML. Returns `CapabilityParseError` if any
/// requested string isn't in the known capability vocabulary — a skill author's typo becomes a
/// build-time/test-time error, not a silently-empty manifest.
pub fn build_manifest_toml(name: &str, requested: &[&str]) -> Result<String, CapabilityParseError> {
    let requested = requested
        .iter()
        .map(|s| s.parse::<Capability>())
        .collect::<Result<Vec<_>, _>>()?;

    let manifest = SkillManifest {
        name: name.to_string(),
        requested,
    };

    Ok(toml::to_string(&manifest).expect("SkillManifest always serializes to TOML"))
}

/// Declares a skill's manifest and generates `skill_manifest_toml()`, returning it rendered as
/// TOML. Panics at call time if a requested capability string is malformed — a skill's own
/// manifest declaration is fixed at compile time, so this is a programmer error, not runtime
/// input to handle gracefully.
///
/// ```ignore
/// pythia_skill_sdk::declare_manifest! {
///     name: "read-file",
///     requested: ["fs:read:/notes"],
/// }
/// ```
#[macro_export]
macro_rules! declare_manifest {
    (name: $name:expr, requested: [$($cap:expr),* $(,)?] $(,)?) => {
        /// This skill's manifest, rendered as TOML matching `pythia_manifest::SkillManifest`.
        pub fn skill_manifest_toml() -> ::std::string::String {
            $crate::manifest::build_manifest_toml($name, &[$($cap),*])
                .expect("declare_manifest!: requested capability string is malformed")
        }
    };
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use std::path::PathBuf;

    use pythia_manifest::{Capability, SkillManifest};

    declare_manifest! {
        name: "read-file",
        requested: ["fs:read:/notes"],
    }

    #[test]
    fn DeclareManifest_ProducesTomlMatchingPythiaManifestSchema() {
        let toml_src = skill_manifest_toml();

        let manifest: SkillManifest =
            toml::from_str(&toml_src).expect("round-trips through pythia-manifest's own parser");

        assert_eq!(manifest.name, "read-file");
        assert_eq!(
            manifest.requested,
            vec![Capability::FsRead(PathBuf::from("/notes"))]
        );
    }

    #[test]
    fn BuildManifestToml_MalformedCapabilityString_ErrorsNotPanics() {
        let result = super::build_manifest_toml("bad-skill", &["not-a-real-capability"]);

        assert!(result.is_err());
    }
}

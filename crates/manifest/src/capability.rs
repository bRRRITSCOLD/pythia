//! The capability string vocabulary: `fs:read:<path>`, `net:<service>`, `secret:<name>`,
//! plus wildcard variants (`fs:read:*`, `net:*`) that are structurally distinct from a
//! concrete grant — a wildcard can never be satisfied by accident because it is a different
//! enum variant, not a special-cased string.

use std::convert::TryFrom;
use std::fmt;
use std::path::PathBuf;
use std::str::FromStr;

use serde::{Deserialize, Serialize};

/// A single capability, parsed from its wire string form.
///
/// Wildcards (`FsReadWildcard`, `NetWildcard`) are separate variants from their concrete
/// counterparts on purpose: nothing in `resolve()` can accidentally treat a wildcard grant
/// as equivalent to a specific-path/service grant.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(try_from = "String", into = "String")]
pub enum Capability {
    FsRead(PathBuf),
    FsReadWildcard,
    Net(String),
    NetWildcard,
    Secret(String),
}

impl Capability {
    /// True for the wildcard variants. Used by `resolve()` to force wildcard requests through
    /// `Decision::Prompt` regardless of what the policy says (SR-1's wildcard clause).
    pub fn is_wildcard(&self) -> bool {
        matches!(self, Capability::FsReadWildcard | Capability::NetWildcard)
    }
}

/// A capability string that doesn't match the known vocabulary. Parsing returns this instead
/// of panicking, so malformed manifests/policies fail as data errors, not process crashes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CapabilityParseError(pub String);

impl fmt::Display for CapabilityParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "invalid capability string: {:?}", self.0)
    }
}

impl std::error::Error for CapabilityParseError {}

impl FromStr for Capability {
    type Err = CapabilityParseError;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        let parts: Vec<&str> = s.splitn(3, ':').collect();
        match parts.as_slice() {
            ["fs", "read", "*"] => Ok(Capability::FsReadWildcard),
            ["fs", "read", path] if !path.is_empty() => {
                Ok(Capability::FsRead(PathBuf::from(*path)))
            }
            ["net", "*"] => Ok(Capability::NetWildcard),
            ["net", service] if !service.is_empty() => Ok(Capability::Net((*service).to_string())),
            ["secret", name] if !name.is_empty() => Ok(Capability::Secret((*name).to_string())),
            _ => Err(CapabilityParseError(s.to_string())),
        }
    }
}

impl TryFrom<String> for Capability {
    type Error = CapabilityParseError;

    fn try_from(value: String) -> Result<Self, Self::Error> {
        value.parse()
    }
}

impl fmt::Display for Capability {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Capability::FsRead(path) => write!(f, "fs:read:{}", path.display()),
            Capability::FsReadWildcard => write!(f, "fs:read:*"),
            Capability::Net(service) => write!(f, "net:{service}"),
            Capability::NetWildcard => write!(f, "net:*"),
            Capability::Secret(name) => write!(f, "secret:{name}"),
        }
    }
}

impl From<Capability> for String {
    fn from(value: Capability) -> Self {
        value.to_string()
    }
}

#[cfg(test)]
#[allow(non_snake_case)]
mod tests {
    use super::*;

    #[test]
    fn Capability_ParseFsReadConcretePath_ReturnsFsRead() {
        let cap: Capability = "fs:read:/tmp/data.txt".parse().unwrap();
        assert_eq!(cap, Capability::FsRead(PathBuf::from("/tmp/data.txt")));
    }

    #[test]
    fn Capability_ParseFsReadWildcard_ReturnsDistinctWildcardVariant() {
        let cap: Capability = "fs:read:*".parse().unwrap();
        assert_eq!(cap, Capability::FsReadWildcard);
        assert_ne!(cap, Capability::FsRead(PathBuf::from("*")));
        assert!(cap.is_wildcard());
    }

    #[test]
    fn Capability_ParseNetService_ReturnsNet() {
        let cap: Capability = "net:smtp".parse().unwrap();
        assert_eq!(cap, Capability::Net("smtp".to_string()));
        assert!(!cap.is_wildcard());
    }

    #[test]
    fn Capability_ParseNetWildcard_ReturnsDistinctWildcardVariant() {
        let cap: Capability = "net:*".parse().unwrap();
        assert_eq!(cap, Capability::NetWildcard);
        assert!(cap.is_wildcard());
    }

    #[test]
    fn Capability_ParseSecretName_ReturnsSecret() {
        let cap: Capability = "secret:api_key".parse().unwrap();
        assert_eq!(cap, Capability::Secret("api_key".to_string()));
    }

    #[test]
    fn Capability_ParseUnknownVocabulary_ErrorsNotPanics() {
        let result: Result<Capability, _> = "bogus:thing".parse();
        assert!(result.is_err());
    }

    #[test]
    fn Capability_ParseFsReadMissingPath_ErrorsNotPanics() {
        let result: Result<Capability, _> = "fs:read".parse();
        assert!(result.is_err());
    }

    #[test]
    fn Capability_ParseEmptyString_ErrorsNotPanics() {
        let result: Result<Capability, _> = "".parse();
        assert!(result.is_err());
    }

    #[test]
    fn Capability_DisplayRoundTrip_MatchesOriginalString() {
        for s in ["fs:read:/a/b", "fs:read:*", "net:smtp", "net:*", "secret:k"] {
            let cap: Capability = s.parse().unwrap();
            assert_eq!(cap.to_string(), s);
        }
    }
}

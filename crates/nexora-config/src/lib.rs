//! Environment-driven configuration for NEXORA Rust services.
//!
//! Configuration is parsed and validated once at startup. Invalid values fail
//! fast with an actionable error instead of producing partial configuration.

use std::collections::HashMap;
use std::env;
use std::net::SocketAddr;
use std::str::FromStr;
use std::time::Duration;

use thiserror::Error;

/// Default HTTP listen address for Rust core services.
pub const DEFAULT_HTTP_ADDR: &str = "0.0.0.0:8081";
/// Default OTLP gRPC endpoint used by the local collector.
pub const DEFAULT_OTLP_ENDPOINT: &str = "http://localhost:4317";
/// Default graceful-shutdown budget in milliseconds.
pub const DEFAULT_SHUTDOWN_TIMEOUT_MS: u64 = 10_000;
/// Lowest accepted graceful-shutdown budget.
pub const MIN_SHUTDOWN_TIMEOUT_MS: u64 = 1_000;
/// Highest accepted graceful-shutdown budget.
pub const MAX_SHUTDOWN_TIMEOUT_MS: u64 = 60_000;

/// Deployment environment.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Environment {
    /// Local development.
    Development,
    /// Automated tests.
    Test,
    /// Pre-production.
    Staging,
    /// Production. Enables stricter validation and secret handling.
    Production,
}

impl Environment {
    /// Stable string used in logs and telemetry resource attributes.
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Development => "development",
            Self::Test => "test",
            Self::Staging => "staging",
            Self::Production => "production",
        }
    }

    /// Whether this environment is production.
    #[must_use]
    pub const fn is_production(self) -> bool {
        matches!(self, Self::Production)
    }
}

impl FromStr for Environment {
    type Err = ConfigError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value.to_ascii_lowercase().as_str() {
            "development" | "dev" => Ok(Self::Development),
            "test" => Ok(Self::Test),
            "staging" => Ok(Self::Staging),
            "production" | "prod" => Ok(Self::Production),
            _ => Err(ConfigError::Invalid {
                key: "NEXORA_ENVIRONMENT".to_owned(),
                value: value.to_owned(),
                reason: "expected development, test, staging, or production".to_owned(),
            }),
        }
    }
}

/// Validated service configuration.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ServiceConfig {
    /// Logical service name used in logs, metrics, and traces.
    pub service_name: String,
    /// Deployment environment.
    pub environment: Environment,
    /// HTTP listen address.
    pub http_addr: SocketAddr,
    /// OTLP gRPC endpoint. An empty value disables trace export.
    pub otlp_endpoint: String,
    /// `tracing` filter directive.
    pub log_level: String,
    /// Graceful-shutdown budget.
    pub shutdown_timeout: Duration,
}

impl ServiceConfig {
    /// Load configuration from process environment variables.
    pub fn from_env(default_service: &str) -> Result<Self, ConfigError> {
        let variables: HashMap<String, String> = env::vars().collect();
        Self::from_map(default_service, &variables)
    }

    /// Load configuration from an explicit map. Used by tests and tooling.
    pub fn from_map(
        default_service: &str,
        variables: &HashMap<String, String>,
    ) -> Result<Self, ConfigError> {
        let get = |key: &str| {
            variables
                .get(key)
                .map(|value| value.trim())
                .filter(|value| !value.is_empty())
        };

        let service_name = get("NEXORA_SERVICE_NAME")
            .unwrap_or(default_service)
            .to_owned();
        if service_name.is_empty() {
            return Err(ConfigError::Invalid {
                key: "NEXORA_SERVICE_NAME".to_owned(),
                value: service_name,
                reason: "must not be empty".to_owned(),
            });
        }

        let environment = get("NEXORA_ENVIRONMENT")
            .map_or(Ok(Environment::Development), Environment::from_str)?;

        let http_addr_raw = get("NEXORA_HTTP_ADDR").unwrap_or(DEFAULT_HTTP_ADDR);
        let http_addr = http_addr_raw
            .parse::<SocketAddr>()
            .map_err(|_| ConfigError::Invalid {
                key: "NEXORA_HTTP_ADDR".to_owned(),
                value: http_addr_raw.to_owned(),
                reason: "expected a socket address such as 0.0.0.0:8081".to_owned(),
            })?;

        let otlp_endpoint = variables
            .get("NEXORA_OTEL_EXPORTER_OTLP_ENDPOINT")
            .map_or(DEFAULT_OTLP_ENDPOINT, |value| value.trim())
            .to_owned();

        let log_level = get("NEXORA_LOG_LEVEL")
            .unwrap_or("info")
            .to_ascii_lowercase();
        if !matches!(
            log_level.as_str(),
            "trace" | "debug" | "info" | "warn" | "error" | "off"
        ) {
            return Err(ConfigError::Invalid {
                key: "NEXORA_LOG_LEVEL".to_owned(),
                value: log_level,
                reason: "expected trace, debug, info, warn, error, or off".to_owned(),
            });
        }

        let timeout_raw =
            get("NEXORA_SHUTDOWN_TIMEOUT_MS").map_or(Ok(DEFAULT_SHUTDOWN_TIMEOUT_MS), |value| {
                value.parse::<u64>().map_err(|_| ConfigError::Invalid {
                    key: "NEXORA_SHUTDOWN_TIMEOUT_MS".to_owned(),
                    value: value.to_owned(),
                    reason: "expected an integer number of milliseconds".to_owned(),
                })
            })?;
        if !(MIN_SHUTDOWN_TIMEOUT_MS..=MAX_SHUTDOWN_TIMEOUT_MS).contains(&timeout_raw) {
            return Err(ConfigError::Invalid {
                key: "NEXORA_SHUTDOWN_TIMEOUT_MS".to_owned(),
                value: timeout_raw.to_string(),
                reason: format!(
                    "must be between {MIN_SHUTDOWN_TIMEOUT_MS} and {MAX_SHUTDOWN_TIMEOUT_MS}"
                ),
            });
        }

        Ok(Self {
            service_name,
            environment,
            http_addr,
            otlp_endpoint,
            log_level,
            shutdown_timeout: Duration::from_millis(timeout_raw),
        })
    }

    /// Whether OTLP trace export is configured.
    #[must_use]
    pub fn otlp_enabled(&self) -> bool {
        !self.otlp_endpoint.is_empty()
    }
}

/// Configuration validation failure.
#[derive(Debug, Error, PartialEq, Eq)]
pub enum ConfigError {
    /// A value is absent, malformed, or outside its accepted range.
    #[error("invalid configuration {key}={value:?}: {reason}")]
    Invalid {
        /// Environment variable name.
        key: String,
        /// Rejected value.
        value: String,
        /// Human-readable reason.
        reason: String,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    fn map(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
            .collect()
    }

    #[test]
    fn defaults_are_valid_for_development() {
        let config = ServiceConfig::from_map("core-template", &HashMap::new()).unwrap();
        assert_eq!(config.service_name, "core-template");
        assert_eq!(config.environment, Environment::Development);
        assert_eq!(config.http_addr, "0.0.0.0:8081".parse().unwrap());
        assert!(config.otlp_enabled());
        assert_eq!(config.shutdown_timeout, Duration::from_millis(10_000));
    }

    #[test]
    fn values_are_trimmed_and_overridden() {
        let config = ServiceConfig::from_map(
            "core-template",
            &map(&[
                ("NEXORA_SERVICE_NAME", "  trading-core  "),
                ("NEXORA_ENVIRONMENT", "staging"),
                ("NEXORA_HTTP_ADDR", "127.0.0.1:9000"),
                ("NEXORA_LOG_LEVEL", "debug"),
                ("NEXORA_SHUTDOWN_TIMEOUT_MS", "2500"),
            ]),
        )
        .unwrap();
        assert_eq!(config.service_name, "trading-core");
        assert_eq!(config.environment, Environment::Staging);
        assert_eq!(config.http_addr, "127.0.0.1:9000".parse().unwrap());
        assert_eq!(config.log_level, "debug");
        assert_eq!(config.shutdown_timeout, Duration::from_millis(2_500));
    }

    #[test]
    fn production_is_recognised() {
        let config =
            ServiceConfig::from_map("risk-engine", &map(&[("NEXORA_ENVIRONMENT", "PRODUCTION")]))
                .unwrap();
        assert!(config.environment.is_production());
    }

    #[test]
    fn empty_otlp_endpoint_disables_export() {
        let config = ServiceConfig::from_map(
            "core-template",
            &map(&[("NEXORA_OTEL_EXPORTER_OTLP_ENDPOINT", "")]),
        )
        .unwrap();
        assert!(!config.otlp_enabled());
    }

    #[test]
    fn invalid_environment_is_rejected() {
        let error = ServiceConfig::from_map("core-template", &map(&[("NEXORA_ENVIRONMENT", "qa")]))
            .unwrap_err();
        assert!(matches!(
            error,
            ConfigError::Invalid { ref key, .. } if key == "NEXORA_ENVIRONMENT"
        ));
    }

    #[test]
    fn invalid_socket_address_is_rejected() {
        let error = ServiceConfig::from_map(
            "core-template",
            &map(&[("NEXORA_HTTP_ADDR", "localhost:8081")]),
        )
        .unwrap_err();
        assert!(matches!(
            error,
            ConfigError::Invalid { ref key, .. } if key == "NEXORA_HTTP_ADDR"
        ));
    }

    #[test]
    fn shutdown_timeout_bounds_are_enforced() {
        for value in ["0", "999", "60001"] {
            let error = ServiceConfig::from_map(
                "core-template",
                &map(&[("NEXORA_SHUTDOWN_TIMEOUT_MS", value)]),
            )
            .unwrap_err();
            assert!(matches!(
                error,
                ConfigError::Invalid { ref key, .. } if key == "NEXORA_SHUTDOWN_TIMEOUT_MS"
            ));
        }
    }

    #[test]
    fn invalid_log_level_is_rejected() {
        let error =
            ServiceConfig::from_map("core-template", &map(&[("NEXORA_LOG_LEVEL", "verbose")]))
                .unwrap_err();
        assert!(matches!(
            error,
            ConfigError::Invalid { ref key, .. } if key == "NEXORA_LOG_LEVEL"
        ));
    }
}

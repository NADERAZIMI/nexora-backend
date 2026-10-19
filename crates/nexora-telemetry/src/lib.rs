//! Observable-service bootstrap for NEXORA Rust services.
//!
//! [`init`] installs a JSON `tracing` subscriber, an OTLP trace pipeline, and a
//! Prometheus recorder. Call [`TelemetryGuard::shutdown`] after the HTTP server
//! has drained so buffered spans are flushed.

use metrics_exporter_prometheus::{PrometheusBuilder, PrometheusHandle};
use nexora_config::ServiceConfig;
use opentelemetry::KeyValue;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry_otlp::{SpanExporter, WithExportConfig};
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::trace::SdkTracerProvider;
use thiserror::Error;
use tracing_subscriber::EnvFilter;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;

/// Telemetry initialization failure.
#[derive(Debug, Error)]
pub enum TelemetryError {
    /// The log filter could not be parsed.
    #[error("invalid log filter: {0}")]
    Filter(String),
    /// The OTLP exporter could not be constructed.
    #[error("failed to initialize OTLP trace exporter: {0}")]
    Otlp(String),
    /// The Prometheus recorder could not be installed.
    #[error("failed to install Prometheus recorder: {0}")]
    Prometheus(String),
}

/// Owns telemetry resources for the lifetime of a service.
pub struct TelemetryGuard {
    tracer_provider: Option<SdkTracerProvider>,
    prometheus: PrometheusHandle,
}

impl TelemetryGuard {
    /// Cloneable handle used by the `/metrics` endpoint.
    #[must_use]
    pub fn prometheus_handle(&self) -> PrometheusHandle {
        self.prometheus.clone()
    }

    /// Flush and stop trace export. Safe to call once during shutdown.
    pub fn shutdown(&self) {
        if let Some(provider) = &self.tracer_provider
            && let Err(error) = provider.shutdown()
        {
            tracing::warn!(%error, "failed to shut down tracer provider");
        }
    }
}

/// Install logging, tracing, and metrics for a service.
pub fn init(config: &ServiceConfig) -> Result<TelemetryGuard, TelemetryError> {
    let filter = EnvFilter::try_new(&config.log_level)
        .map_err(|error| TelemetryError::Filter(error.to_string()))?;

    let prometheus = PrometheusBuilder::new()
        .install_recorder()
        .map_err(|error| TelemetryError::Prometheus(error.to_string()))?;

    let fmt_layer = tracing_subscriber::fmt::layer()
        .json()
        .with_current_span(true)
        .with_span_list(true);

    let (tracer_provider, otel_layer) = if config.otlp_enabled() {
        let exporter = SpanExporter::builder()
            .with_tonic()
            .with_endpoint(config.otlp_endpoint.clone())
            .build()
            .map_err(|error| TelemetryError::Otlp(error.to_string()))?;

        let resource = Resource::builder()
            .with_service_name(config.service_name.clone())
            .with_attribute(KeyValue::new(
                "deployment.environment.name",
                config.environment.as_str().to_owned(),
            ))
            .build();

        let provider = SdkTracerProvider::builder()
            .with_batch_exporter(exporter)
            .with_resource(resource)
            .build();

        opentelemetry::global::set_tracer_provider(provider.clone());
        let tracer = provider.tracer("nexora");
        let layer = tracing_opentelemetry::layer().with_tracer(tracer);
        (Some(provider), Some(layer))
    } else {
        (None, None)
    };

    tracing_subscriber::registry()
        .with(filter)
        .with(fmt_layer)
        .with(otel_layer)
        .try_init()
        .map_err(|error| TelemetryError::Filter(error.to_string()))?;

    Ok(TelemetryGuard {
        tracer_provider,
        prometheus,
    })
}

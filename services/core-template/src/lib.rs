//! Reference Rust service skeleton.
//!
//! The template intentionally exposes only liveness, readiness, metrics, and a
//! trivial hello endpoint. Trading, ledger, and risk behaviour is delivered in
//! later phases.

use std::time::Instant;

use axum::extract::{Request, State};
use axum::http::{HeaderMap, HeaderName, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use metrics::{counter, histogram};
use metrics_exporter_prometheus::PrometheusHandle;
use serde::Serialize;
use tower_http::catch_panic::CatchPanicLayer;
use tower_http::limit::RequestBodyLimitLayer;
use tower_http::request_id::{MakeRequestUuid, PropagateRequestIdLayer, SetRequestIdLayer};
use tower_http::trace::{DefaultMakeSpan, DefaultOnResponse, TraceLayer};
use tracing::Level;

/// Shared state for the reference service.
#[derive(Clone)]
pub struct AppState {
    service_name: String,
    version: String,
    prometheus: PrometheusHandle,
}

impl AppState {
    /// Construct service state.
    #[must_use]
    pub fn new(service_name: String, version: String, prometheus: PrometheusHandle) -> Self {
        Self {
            service_name,
            version,
            prometheus,
        }
    }
}

/// Build the HTTP router.
pub fn app(state: AppState) -> Router {
    let request_id_header = HeaderName::from_static("x-request-id");

    Router::new()
        .route("/healthz", get(health))
        .route("/readyz", get(ready))
        .route("/metrics", get(metrics))
        .route("/v1/hello", get(hello))
        .layer(middleware::from_fn_with_state(
            state.clone(),
            record_metrics,
        ))
        .layer(
            TraceLayer::new_for_http()
                .make_span_with(DefaultMakeSpan::new().level(Level::INFO))
                .on_response(DefaultOnResponse::new().level(Level::INFO)),
        )
        .layer(CatchPanicLayer::new())
        .layer(RequestBodyLimitLayer::new(1024 * 1024))
        .layer(PropagateRequestIdLayer::new(request_id_header.clone()))
        .layer(SetRequestIdLayer::new(request_id_header, MakeRequestUuid))
        .with_state(state)
}

#[derive(Serialize)]
struct HealthResponse {
    status: &'static str,
    service: String,
    version: String,
}

#[derive(Serialize)]
struct HelloResponse {
    message: &'static str,
    service: String,
    request_id: String,
}

async fn health(State(state): State<AppState>) -> Json<HealthResponse> {
    Json(HealthResponse {
        status: "ok",
        service: state.service_name,
        version: state.version,
    })
}

async fn ready(State(state): State<AppState>) -> Json<HealthResponse> {
    Json(HealthResponse {
        status: "ready",
        service: state.service_name,
        version: state.version,
    })
}

async fn metrics(State(state): State<AppState>) -> impl IntoResponse {
    (
        StatusCode::OK,
        [(
            header::CONTENT_TYPE,
            "text/plain; version=0.0.4; charset=utf-8",
        )],
        state.prometheus.render(),
    )
}

async fn hello(State(state): State<AppState>, headers: HeaderMap) -> Json<HelloResponse> {
    let request_id = headers
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("unknown")
        .to_owned();

    Json(HelloResponse {
        message: "nexora backend baseline",
        service: state.service_name,
        request_id,
    })
}

async fn record_metrics(State(state): State<AppState>, request: Request, next: Next) -> Response {
    let method = request.method().to_string();
    let path = request.uri().path().to_owned();
    let started = Instant::now();

    let response = next.run(request).await;
    let status = response.status().as_u16().to_string();

    counter!(
        "http_requests_total",
        "service" => state.service_name.clone(),
        "method" => method,
        "path" => path,
        "status" => status,
    )
    .increment(1);

    histogram!(
        "http_request_duration_seconds",
        "service" => state.service_name.clone(),
    )
    .record(started.elapsed().as_secs_f64());

    response
}

#[cfg(test)]
mod tests {
    use std::sync::OnceLock;

    use axum::body::Body;
    use http_body_util::BodyExt;
    use metrics_exporter_prometheus::{PrometheusBuilder, PrometheusHandle};
    use tower::ServiceExt;

    use super::*;

    fn state() -> AppState {
        static RECORDER: OnceLock<PrometheusHandle> = OnceLock::new();
        let handle = RECORDER
            .get_or_init(|| {
                PrometheusBuilder::new()
                    .install_recorder()
                    .expect("install Prometheus recorder")
            })
            .clone();
        AppState::new("core-template".to_owned(), "0.1.0".to_owned(), handle)
    }

    async fn body_text(response: Response) -> String {
        let bytes = response
            .into_body()
            .collect()
            .await
            .expect("read body")
            .to_bytes();
        String::from_utf8(bytes.to_vec()).expect("utf-8 body")
    }

    #[tokio::test]
    async fn health_endpoint_is_json() {
        let response = app(state())
            .oneshot(
                Request::builder()
                    .uri("/healthz")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let text = body_text(response).await;
        assert!(text.contains("\"status\":\"ok\""));
        assert!(text.contains("core-template"));
    }

    #[tokio::test]
    async fn readiness_endpoint_reports_ready() {
        let response = app(state())
            .oneshot(
                Request::builder()
                    .uri("/readyz")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert!(body_text(response).await.contains("\"ready\""));
    }

    #[tokio::test]
    async fn request_id_is_generated_and_echoed() {
        let response = app(state())
            .oneshot(
                Request::builder()
                    .uri("/v1/hello")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let request_id = response
            .headers()
            .get("x-request-id")
            .expect("request id header")
            .to_str()
            .unwrap()
            .to_owned();
        assert!(!request_id.is_empty());
        assert!(body_text(response).await.contains(&request_id));
    }

    #[tokio::test]
    async fn client_request_id_is_preserved() {
        let response = app(state())
            .oneshot(
                Request::builder()
                    .uri("/v1/hello")
                    .header("x-request-id", "req-b01-test")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(
            response.headers().get("x-request-id").unwrap(),
            "req-b01-test"
        );
    }

    #[tokio::test]
    async fn metrics_include_request_counter() {
        let router = app(state());
        let _ = router
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/v1/hello")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        let response = router
            .oneshot(
                Request::builder()
                    .uri("/metrics")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let text = body_text(response).await;
        assert!(text.contains("http_requests_total"));
        assert!(text.contains("path=\"/v1/hello\""));
    }
}

//! Rust core service entrypoint.

use nexora_config::ServiceConfig;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let config = ServiceConfig::from_env("core-template")?;
    let telemetry = nexora_telemetry::init(&config)?;

    let state = core_template::AppState::new(
        config.service_name.clone(),
        env!("CARGO_PKG_VERSION").to_owned(),
        telemetry.prometheus_handle(),
    );

    let listener = tokio::net::TcpListener::bind(config.http_addr).await?;
    tracing::info!(
        service = %config.service_name,
        environment = %config.environment.as_str(),
        address = %config.http_addr,
        "service listening"
    );

    let result = axum::serve(listener, core_template::app(state))
        .with_graceful_shutdown(shutdown_signal())
        .await;

    telemetry.shutdown();

    result?;
    Ok(())
}

async fn shutdown_signal() {
    let ctrl_c = async {
        if let Err(error) = tokio::signal::ctrl_c().await {
            tracing::error!(%error, "failed to listen for ctrl-c");
        }
    };

    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut signal) => {
                signal.recv().await;
            }
            Err(error) => tracing::error!(%error, "failed to listen for SIGTERM"),
        }
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        () = ctrl_c => {},
        () = terminate => {},
    }

    tracing::info!("shutdown signal received");
}

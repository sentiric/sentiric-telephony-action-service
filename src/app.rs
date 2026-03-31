use crate::config::AppConfig;
use crate::pubsub::ghost_publisher::GhostPublisher;
use crate::server::{grpc, http};
use anyhow::Result;
use std::sync::Arc;
use tokio::signal;
use tracing::info;

pub async fn run(config: AppConfig) -> Result<()> {
    let cfg = Arc::new(config);

    // Ghost Publisher (RabbitMQ)
    let publisher = GhostPublisher::new(cfg.rabbitmq_url.clone(), cfg.tenant_id.clone());

    // HTTP Healthz Server
    let http_addr = format!("0.0.0.0:{}", cfg.http_port).parse()?;
    let http_server = tokio::spawn(http::start_server(http_addr));

    // gRPC Server
    let grpc_addr = format!("0.0.0.0:{}", cfg.grpc_port).parse()?;
    let grpc_server = tokio::spawn(grpc::start_server(grpc_addr, cfg.clone(), publisher));

    info!(event = "SERVERS_RUNNING", "Servisler dinlemeye başladı.");

    tokio::select! {
        _ = signal::ctrl_c() => {
            info!(event="SIGINT_RECEIVED", "Sistem zarif bir şekilde kapatılıyor (Graceful Shutdown)...");
        }
        _ = http_server => {
            info!(event="HTTP_SERVER_STOPPED", "HTTP Sunucusu durdu.");
        }
        _ = grpc_server => {
            info!(event="GRPC_SERVER_STOPPED", "gRPC Sunucusu durdu.");
        }
    }

    Ok(())
}

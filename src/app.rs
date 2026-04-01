// File: sentiric-telephony-action-service/src/app.rs
use crate::config::AppConfig;
use crate::pubsub::ghost_publisher::GhostPublisher;
use crate::server::{grpc, http};
use anyhow::{anyhow, Result};
use std::sync::Arc;
use tokio::signal;
use tracing::{error, info};

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
        res = http_server => {
            // [ARCH-COMPLIANCE] Hata yutulması engellendi. Exit 1 ile çıkması sağlandı.
            error!(event="HTTP_SERVER_STOPPED", result=?res, "HTTP Sunucusu beklenmedik şekilde durdu.");
            return Err(anyhow!("HTTP Server stopped unexpectedly: {:?}", res));
        }
        res = grpc_server => {
            // [ARCH-COMPLIANCE] gRPC task'ından dönen Result dışarıya paslandı.
            error!(event="GRPC_SERVER_STOPPED", result=?res, "gRPC Sunucusu beklenmedik şekilde durdu.");
            match res {
                Ok(Err(e)) => return Err(anyhow!("gRPC Server failed: {}", e)),
                _ => return Err(anyhow!("gRPC Server stopped unexpectedly")),
            }
        }
    }

    Ok(())
}

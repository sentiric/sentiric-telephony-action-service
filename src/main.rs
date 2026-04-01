mod app;
mod clients;
mod config;
mod pubsub;
mod server;
mod telemetry;

use crate::config::AppConfig;
use crate::telemetry::SutsFormatter;
use std::io::Write;
use tracing::{error, info};
use tracing_subscriber::{fmt, prelude::*, EnvFilter, Registry}; // EKLENDİ

fn main() -> Result<(), Box<dyn std::error::Error>> {
    // EKLENDİ
    dotenv::dotenv().ok();

    let config = match AppConfig::load() {
        Ok(cfg) => cfg,
        Err(e) => {
            // Tamponu atlayıp doğrudan stderr'e zorla yazdır
            let _ = writeln!(std::io::stderr(), "{{\"schema_v\":\"1.0.0\",\"severity\":\"FATAL\",\"event\":\"CONFIG_ERROR\",\"message\":\"{}\"}}", e);
            return Err(e.into());
        }
    };

    let env_filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    let suts_formatter = SutsFormatter::new(
        "telephony-action-service".to_string(),
        env!("CARGO_PKG_VERSION").to_string(),
        config.env.clone(),
        config.tenant_id.clone(),
    );

    let subscriber = Registry::default()
        .with(env_filter)
        .with(fmt::layer().event_format(suts_formatter));
    tracing::subscriber::set_global_default(subscriber).expect("Failed to set tracing subscriber");

    info!(
        event = "SYSTEM_STARTUP",
        "🚀 Telephony Action Service başlatılıyor (SUTS v4.0)"
    );

    let mut builder = tokio::runtime::Builder::new_multi_thread();
    builder.enable_all();
    if let Ok(threads_str) = std::env::var("WORKER_THREADS") {
        if let Ok(threads) = threads_str.parse::<usize>() {
            info!(
                event = "EDGE_LIMITS_APPLIED",
                threads = threads,
                "Sınırlandırılmış CPU kaynakları uygulanıyor."
            );
            builder.worker_threads(threads);
        }
    }

    let runtime = builder.build().expect("Tokio runtime oluşturulamadı");

    runtime.block_on(async {
        if let Err(e) = app::run(config).await {
            error!(event="SYSTEM_CRASH", error=%e, "Sistem kritik bir hata nedeniyle çöktü");
            // [KRİTİK FIX]: Log'un havada kaybolmasını önlemek için std::process::exit öncesi stderr'e bas!
            let _ = writeln!(std::io::stderr(), "{{\"schema_v\":\"1.0.0\",\"severity\":\"FATAL\",\"event\":\"SYSTEM_CRASH\",\"message\":\"{}\"}}", e);
            std::process::exit(1);
        }
    });

    Ok(())
}

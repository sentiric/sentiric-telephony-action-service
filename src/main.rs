mod app;
mod clients;
mod config;
mod pubsub;
mod server;
mod telemetry;

use crate::config::AppConfig;
use crate::telemetry::SutsFormatter;
use tracing::{error, info};
use tracing_subscriber::{fmt, prelude::*, EnvFilter, Registry};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    // DEĞİŞİKLİK 1
    dotenv::dotenv().ok();

    let config = match AppConfig::load() {
        Ok(cfg) => cfg,
        Err(e) => {
            eprintln!("[ARCH-COMPLIANCE] FATAL ERROR during config load: {}", e);
            return Err(e.into()); // DEĞİŞİKLİK 2
        }
    };

    // SUTS v4.0 Logging Setup
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

    // [ARCH-COMPLIANCE] Edge Resource Limits
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
            // std::process::exit(1) SİLİNDİ, BUNUN YERİNE:
        }
    });

    Ok(()) // DEĞİŞİKLİK 3
}

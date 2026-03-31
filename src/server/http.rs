use axum::{routing::get, Router};
use std::net::SocketAddr;
use tokio::net::TcpListener;

pub async fn start_server(addr: SocketAddr) -> anyhow::Result<()> {
    let app = Router::new().route("/healthz", get(|| async { "{\"status\": \"ok\"}" }));
    let listener = TcpListener::bind(&addr).await?;
    axum::serve(listener, app).await?;
    Ok(())
}

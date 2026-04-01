// File: sentiric-telephony-action-service/src/clients/media_client.rs
use anyhow::{anyhow, Result};
use sentiric_contracts::sentiric::media::v1::media_service_client::MediaServiceClient;
use std::str::FromStr;
use tonic::metadata::MetadataValue;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint, Identity};
use tracing::info;

#[derive(Clone)]
pub struct SecureMediaClient {
    pub inner: MediaServiceClient<Channel>,
}

impl SecureMediaClient {
    pub async fn connect(
        url: &str,
        cert_path: &str,
        key_path: &str,
        ca_path: &str,
    ) -> Result<Self> {
        info!(event="MEDIA_CLIENT_CONNECT", target=%url, "media-service'e mTLS bağlantısı kuruluyor (Lazy)...");

        if url.starts_with("http://") {
            return Err(anyhow!(
                "[ARCH-COMPLIANCE] HTTP bağlantısı yasaktır. mTLS (https://) kullanılmalıdır."
            ));
        }

        let cert = tokio::fs::read(cert_path).await?;
        let key = tokio::fs::read(key_path).await?;
        let ca_cert = tokio::fs::read(ca_path).await?;

        let identity = Identity::from_pem(cert, key);
        let ca = Certificate::from_pem(ca_cert);

        let tls_config = ClientTlsConfig::new()
            .domain_name("sentiric.cloud")
            .ca_certificate(ca)
            .identity(identity);

        // [ARCH-COMPLIANCE] Senkron `.connect().await` yerine `.connect_lazy()` kullanıldı.
        // Bu sayede hedef servis henüz ayakta değilse bile sistem çökmez, arkada reconnect dener.
        let channel = Endpoint::from_shared(url.to_string())?
            .tls_config(tls_config)?
            .connect_lazy();

        Ok(Self {
            inner: MediaServiceClient::new(channel),
        })
    }

    pub fn inject_metadata<T>(
        &self,
        mut req: tonic::Request<T>,
        trace_id: &str,
        span_id: &str,
        tenant_id: &str,
    ) -> tonic::Request<T> {
        if let Ok(val) = MetadataValue::from_str(trace_id) {
            req.metadata_mut().insert("x-trace-id", val);
        }
        if let Ok(val) = MetadataValue::from_str(span_id) {
            req.metadata_mut().insert("x-span-id", val);
        }
        if let Ok(val) = MetadataValue::from_str(tenant_id) {
            req.metadata_mut().insert("x-tenant-id", val);
        }
        req
    }
}

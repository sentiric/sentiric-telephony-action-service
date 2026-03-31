use anyhow::{anyhow, Result};
use std::env;

#[derive(Debug, Clone)]
pub struct AppConfig {
    pub env: String,
    pub tenant_id: String,
    pub http_port: u16,
    pub grpc_port: u16,

    // mTLS
    pub tls_cert_path: String,
    pub tls_key_path: String,
    pub tls_ca_path: String,

    // Dependencies
    pub media_service_url: String,
    pub rabbitmq_url: String,

    // AI SDK Params
    pub stt_gateway_url: String,
    pub dialog_service_url: String,
    pub tts_gateway_url: String,
}

impl AppConfig {
    pub fn load() -> Result<Self> {
        let tenant_id = env::var("TENANT_ID")
            .map_err(|_| anyhow!("[ARCH-COMPLIANCE] TENANT_ID env var zorunludur!"))?;
        if tenant_id.is_empty() {
            return Err(anyhow!("[ARCH-COMPLIANCE] TENANT_ID boş olamaz"));
        }

        let tls_cert_path = env::var("TELEPHONY_ACTION_SERVICE_CERT_PATH")
            .map_err(|_| anyhow!("[ARCH-COMPLIANCE] mTLS sertifikası (CERT_PATH) zorunludur!"))?;
        let tls_key_path = env::var("TELEPHONY_ACTION_SERVICE_KEY_PATH")
            .map_err(|_| anyhow!("[ARCH-COMPLIANCE] mTLS anahtarı (KEY_PATH) zorunludur!"))?;
        let tls_ca_path = env::var("GRPC_TLS_CA_PATH")
            .map_err(|_| anyhow!("[ARCH-COMPLIANCE] Root CA sertifikası zorunludur!"))?;

        Ok(Self {
            env: env::var("ENV").unwrap_or_else(|_| "production".to_string()),
            tenant_id,
            http_port: env::var("TELEPHONY_ACTION_SERVICE_HTTP_PORT")
                .unwrap_or_else(|_| "13050".to_string())
                .parse()?,
            grpc_port: env::var("TELEPHONY_ACTION_SERVICE_GRPC_PORT")
                .unwrap_or_else(|_| "13051".to_string())
                .parse()?,
            tls_cert_path,
            tls_key_path,
            tls_ca_path,
            media_service_url: env::var("MEDIA_SERVICE_TARGET_GRPC_URL")
                .unwrap_or_else(|_| "https://media-service:13031".to_string()),
            rabbitmq_url: env::var("RABBITMQ_URL")
                .unwrap_or_else(|_| "amqp://localhost:5672".to_string()),
            stt_gateway_url: env::var("STT_GATEWAY_TARGET_GRPC_URL")
                .unwrap_or_else(|_| "https://stt-gateway-service:15021".to_string()),
            dialog_service_url: env::var("DIALOG_SERVICE_TARGET_GRPC_URL")
                .unwrap_or_else(|_| "https://dialog-service:12061".to_string()),
            tts_gateway_url: env::var("TTS_GATEWAY_TARGET_GRPC_URL")
                .unwrap_or_else(|_| "https://tts-gateway-service:14011".to_string()),
        })
    }
}

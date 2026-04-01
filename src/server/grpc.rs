use crate::clients::media_client::SecureMediaClient;
use crate::config::AppConfig;
use crate::pubsub::ghost_publisher::GhostPublisher;
use anyhow::Result;
use futures::StreamExt;
use sentiric_ai_pipeline_sdk::{PipelineOrchestrator, SdkConfig};
use sentiric_contracts::sentiric::media::v1::{RecordAudioRequest, StreamAudioToCallRequest};
use sentiric_contracts::sentiric::telephony::v1::telephony_action_service_server::{
    TelephonyActionService, TelephonyActionServiceServer,
};
use sentiric_contracts::sentiric::telephony::v1::{
    BridgeCallRequest, BridgeCallResponse, PlayAudioRequest, PlayAudioResponse, RunPipelineRequest,
    RunPipelineResponse, SendTextMessageRequest, SendTextMessageResponse, SpeakTextRequest,
    SpeakTextResponse, StartRecordingRequest, StartRecordingResponse, StopRecordingRequest,
    StopRecordingResponse, TerminateCallRequest, TerminateCallResponse,
};
use std::sync::Arc;
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::{
    transport::{Certificate, Identity, Server, ServerTlsConfig},
    Request, Response, Status,
};
use tracing::{error, info, Instrument};

pub async fn start_server(
    addr: std::net::SocketAddr,
    config: Arc<AppConfig>,
    publisher: GhostPublisher,
) -> Result<()> {
    let cert = tokio::fs::read(&config.tls_cert_path).await?;
    let key = tokio::fs::read(&config.tls_key_path).await?;
    let ca_cert = tokio::fs::read(&config.tls_ca_path).await?;

    let identity = Identity::from_pem(cert, key);
    let client_ca = Certificate::from_pem(ca_cert);

    let tls_config = ServerTlsConfig::new()
        .identity(identity)
        .client_ca_root(client_ca);

    let media_client = SecureMediaClient::connect(
        &config.media_service_url,
        &config.tls_cert_path,
        &config.tls_key_path,
        &config.tls_ca_path,
    )
    .await?;

    let service = TelephonyService {
        config: config.clone(),
        publisher,
        media_client,
    };

    Server::builder()
        .tls_config(tls_config)?
        .add_service(TelephonyActionServiceServer::new(service))
        .serve(addr)
        .await?;

    Ok(())
}

pub struct TelephonyService {
    config: Arc<AppConfig>,
    publisher: GhostPublisher,
    media_client: SecureMediaClient,
}

impl TelephonyService {
    fn extract_meta<T>(req: &Request<T>) -> (String, String, String) {
        let tid = req
            .metadata()
            .get("x-trace-id")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("unknown")
            .to_string();
        let sid = req
            .metadata()
            .get("x-span-id")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("0000")
            .to_string();
        let ten = req
            .metadata()
            .get("x-tenant-id")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("unknown")
            .to_string();
        (tid, sid, ten)
    }
}

#[tonic::async_trait]
impl TelephonyActionService for TelephonyService {
    type RunPipelineStream = ReceiverStream<Result<RunPipelineResponse, Status>>;

    async fn run_pipeline(
        &self,
        request: Request<RunPipelineRequest>,
    ) -> Result<Response<Self::RunPipelineStream>, Status> {
        // [ARCH-COMPLIANCE FIX]: Request::new(()) silindi. Orijinal gRPC Request'inden context okunuyor.
        let (trace_id, span_id, tenant_id) = Self::extract_meta(&request);
        let req = request.into_inner();
        let call_id = req.call_id.clone();
        let server_rtp_port = req
            .media_info
            .as_ref()
            .map(|m| m.server_rtp_port)
            .unwrap_or(0);

        info!(event="PIPELINE_INIT", trace_id=%trace_id, span_id=%span_id, tenant_id=%tenant_id, call_id=%call_id, "📞 Pipeline başlatılıyor.");

        let (response_tx, response_rx) = mpsc::channel(10);
        let _ = response_tx
            .send(Ok(RunPipelineResponse {
                state: 1,
                message: "Pipeline booting...".into(),
            }))
            .await;

        let sdk_config = SdkConfig {
            stt_gateway_url: self.config.stt_gateway_url.clone(),
            dialog_service_url: self.config.dialog_service_url.clone(),
            tts_gateway_url: self.config.tts_gateway_url.clone(),
            tls_ca_path: self.config.tls_ca_path.clone(),
            tls_cert_path: self.config.tls_cert_path.clone(),
            tls_key_path: self.config.tls_key_path.clone(),
            language_code: req.language_code.clone(),
            system_prompt_id: req.system_prompt_id.clone(),
            tts_voice_id: req.tts_model_id.clone(),
            tts_sample_rate: 8000,
            edge_mode: false,
        };

        let orchestrator = PipelineOrchestrator::new(sdk_config)
            .await
            .map_err(|e| Status::internal(e.to_string()))?;

        let m_client = self.media_client.clone();
        let publisher = self.publisher.clone();
        let cid_clone = call_id.clone();
        let tid_clone = trace_id.clone();

        tokio::spawn(
            async move {
                let _ = response_tx
                    .send(Ok(RunPipelineResponse {
                        state: 2,
                        message: "Running".into(),
                    }))
                    .await;

                let record_req = m_client.inject_metadata(
                    Request::new(RecordAudioRequest {
                        server_rtp_port,
                        target_sample_rate: Some(16000),
                    }),
                    &trace_id,
                    &span_id,
                    &tenant_id,
                );

                let mut m_client_inner = m_client.inner.clone();
                let mut record_stream = match m_client_inner.record_audio(record_req).await {
                    Ok(res) => res.into_inner(),
                    Err(e) => {
                        error!(event="MEDIA_RX_FAIL", trace_id=%trace_id, span_id=%span_id, tenant_id=%tenant_id, error=%e, "Media RX stream açılamadı.");
                        publisher.publish_terminate_request(&cid_clone, &tid_clone, "MEDIA_RX_FAIL").await;
                        return;
                    }
                };

                let (sdk_rx_tx, sdk_rx_rx) = mpsc::channel(100);
                tokio::spawn(async move {
                    while let Some(Ok(res)) = record_stream.next().await {
                        if sdk_rx_tx.send(res.audio_data).await.is_err() {
                            break;
                        }
                    }
                });

                let (sdk_tx_tx, mut sdk_tx_rx) = mpsc::channel(100);
                let (media_tx_tx, media_tx_rx) = mpsc::channel(100);

                let stream_req = m_client.inject_metadata(
                    Request::new(ReceiverStream::new(media_tx_rx)),
                    &trace_id,
                    &span_id,
                    &tenant_id,
                );

                let c_id = cid_clone.clone();
                tokio::spawn(async move {
                    while let Some(chunk) = sdk_tx_rx.recv().await {
                        let msg = StreamAudioToCallRequest {
                            call_id: c_id.clone(),
                            audio_chunk: chunk,
                        };
                        if media_tx_tx.send(msg).await.is_err() {
                            break;
                        }
                    }
                });

                if let Err(e) = m_client_inner.stream_audio_to_call(stream_req).await {
                    error!(event="MEDIA_TX_FAIL", trace_id=%trace_id, span_id=%span_id, tenant_id=%tenant_id, error=%e, "Media TX stream açılamadı.");
                }

                match orchestrator
                    .run_pipeline(
                        req.session_id.clone(),
                        "system".into(),
                        trace_id.clone(),
                        span_id.clone(),
                        tenant_id.clone(),
                        sdk_rx_rx,
                        sdk_tx_tx,
                    )
                    .await
                {
                    Ok(_) => {
                        info!(event="PIPELINE_COMPLETE", trace_id=%trace_id, span_id=%span_id, tenant_id=%tenant_id, call_id=%cid_clone, "Pipeline doğal olarak tamamlandı.");
                        publisher.publish_terminate_request(&cid_clone, &tid_clone, "PIPELINE_COMPLETE").await;
                    }
                    Err(e) => {
                        error!(event="PIPELINE_ERROR", trace_id=%trace_id, span_id=%span_id, tenant_id=%tenant_id, error=%e, "Pipeline hata verdi.");
                        publisher.publish_terminate_request(&cid_clone, &tid_clone, "PIPELINE_ERROR").await;
                    }
                }

                let _ = response_tx
                    .send(Ok(RunPipelineResponse {
                        state: 3,
                        message: "Finished".into(),
                    }))
                    .await;
            }
            .instrument(tracing::Span::current()),
        );

        Ok(Response::new(ReceiverStream::new(response_rx)))
    }

    async fn play_audio(
        &self,
        _r: Request<PlayAudioRequest>,
    ) -> Result<Response<PlayAudioResponse>, Status> {
        Ok(Response::new(PlayAudioResponse { success: true }))
    }
    async fn terminate_call(
        &self,
        _r: Request<TerminateCallRequest>,
    ) -> Result<Response<TerminateCallResponse>, Status> {
        Ok(Response::new(TerminateCallResponse { success: true }))
    }
    async fn send_text_message(
        &self,
        _r: Request<SendTextMessageRequest>,
    ) -> Result<Response<SendTextMessageResponse>, Status> {
        Ok(Response::new(SendTextMessageResponse { success: true }))
    }
    async fn start_recording(
        &self,
        _r: Request<StartRecordingRequest>,
    ) -> Result<Response<StartRecordingResponse>, Status> {
        Ok(Response::new(StartRecordingResponse { success: true }))
    }
    async fn stop_recording(
        &self,
        _r: Request<StopRecordingRequest>,
    ) -> Result<Response<StopRecordingResponse>, Status> {
        Ok(Response::new(StopRecordingResponse { success: true }))
    }
    async fn speak_text(
        &self,
        _r: Request<SpeakTextRequest>,
    ) -> Result<Response<SpeakTextResponse>, Status> {
        Ok(Response::new(SpeakTextResponse {
            success: true,
            message: "Mocked".into(),
        }))
    }
    async fn bridge_call(
        &self,
        _r: Request<BridgeCallRequest>,
    ) -> Result<Response<BridgeCallResponse>, Status> {
        Ok(Response::new(BridgeCallResponse {
            success: true,
            message: "Mocked".into(),
        }))
    }
}

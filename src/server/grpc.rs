// File: sentiric-telephony-action-service/src/server/grpc.rs
use crate::clients::media_client::SecureMediaClient;
use crate::config::AppConfig;
use crate::pubsub::ghost_publisher::GhostPublisher;
use anyhow::Result;
use futures::StreamExt;
use sentiric_ai_pipeline_sdk::{PipelineEvent, PipelineOrchestrator, SdkConfig};
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
    #[allow(dead_code)]
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

    fn calculate_rms(samples: &[i16]) -> f32 {
        if samples.is_empty() {
            return 0.0;
        }
        let sum: f64 = samples.iter().map(|&s| (s as f64) * (s as f64)).sum();
        (sum / samples.len() as f64).sqrt() as f32
    }
}

#[tonic::async_trait]
impl TelephonyActionService for TelephonyService {
    type RunPipelineStream = ReceiverStream<Result<RunPipelineResponse, Status>>;

    async fn run_pipeline(
        &self,
        request: Request<RunPipelineRequest>,
    ) -> Result<Response<Self::RunPipelineStream>, Status> {
        let (trace_id, span_id, tenant_id) = Self::extract_meta(&request);
        let req = request.into_inner();
        let call_id = req.call_id.clone();
        let server_rtp_port = req
            .media_info
            .as_ref()
            .map(|m| m.server_rtp_port)
            .unwrap_or(0);

        info!(event="PIPELINE_INIT", trace_id=%trace_id, call_id=%call_id, "📞 Telekom Pipeline başlatılıyor.");

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
            tts_sample_rate: 16000,
            edge_mode: req.edge_mode,
            listen_only_mode: false,
            speak_only_mode: false,
            chat_only_mode: false,
        };

        let orchestrator = PipelineOrchestrator::new(sdk_config)
            .await
            .map_err(|e| Status::internal(e.to_string()))?;

        let m_client = self.media_client.clone();
        let cid_clone = call_id.clone();
        let publisher_clone = self.publisher.clone();

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
                        error!(event="MEDIA_RX_FAIL", error=%e, "Media RX stream açılamadı.");
                        return;
                    }
                };

                let (sdk_rx_tx, sdk_rx_rx) =
                    mpsc::channel::<sentiric_ai_pipeline_sdk::PipelineInputEvent>(100);

                tokio::spawn(async move {
                    let mut last_speech_time = std::time::Instant::now();
                    let mut is_speaking = false;
                    let mut sent_eos = false;
                    let silence_threshold = 200.0;
                    let silence_timeout = std::time::Duration::from_millis(1500);

                    while let Some(Ok(res)) = record_stream.next().await {
                        let samples: Vec<i16> = res
                            .audio_data
                            .chunks_exact(2)
                            .map(|c| i16::from_le_bytes([c[0], c[1]]))
                            .collect();

                        let rms = TelephonyService::calculate_rms(&samples);

                        if rms > silence_threshold {
                            last_speech_time = std::time::Instant::now();
                            is_speaking = true;
                            sent_eos = false;
                        } else if is_speaking && last_speech_time.elapsed() > silence_timeout {
                            is_speaking = false;
                        }

                        if is_speaking || last_speech_time.elapsed() <= silence_timeout {
                            if sdk_rx_tx
                                .send(sentiric_ai_pipeline_sdk::PipelineInputEvent::Audio(
                                    res.audio_data,
                                ))
                                .await
                                .is_err()
                            {
                                break;
                            }
                        } else if !sent_eos {
                            let _ = sdk_rx_tx
                                .send(sentiric_ai_pipeline_sdk::PipelineInputEvent::Audio(vec![]))
                                .await;
                            sent_eos = true;
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

                let loop_trace_id = trace_id.clone();
                let loop_tenant_id = tenant_id.clone();
                let c_id = cid_clone.clone();
                let inner_publisher = publisher_clone.clone();

                tokio::spawn(async move {
                    while let Some(event) = sdk_tx_rx.recv().await {
                        match event {
                            PipelineEvent::Audio(chunk) => {
                                let msg = StreamAudioToCallRequest {
                                    call_id: c_id.clone(),
                                    audio_chunk: chunk,
                                };
                                if media_tx_tx.send(msg).await.is_err() {
                                    break;
                                }
                            }
                            // [ARCH-COMPLIANCE FIX]: SDK v0.1.16 session_id eklendi
                            PipelineEvent::AcousticMoodShifted { session_id: evt_sess_id, previous_mood, current_mood, arousal_shift, valence_shift, speaker_id } => {
                                let payload = serde_json::json!({
                                    "trace_id": loop_trace_id,
                                    "session_id": evt_sess_id, // Crystalline için eklendi
                                    "call_id": c_id,
                                    "previous_mood": previous_mood,
                                    "current_mood": current_mood,
                                    "arousal_shift": arousal_shift,
                                    "valence_shift": valence_shift,
                                    "speaker_id": speaker_id
                                });
                                use sentiric_contracts::sentiric::event::v1::GenericEvent;
                                use prost::Message;
                                let event_msg = GenericEvent {
                                    event_type: "acoustic.mood.shifted".to_string(),
                                    trace_id: loop_trace_id.clone(),
                                    timestamp: Some(prost_types::Timestamp {
                                        seconds: chrono::Utc::now().timestamp(),
                                        nanos: chrono::Utc::now().timestamp_subsec_nanos() as i32,
                                    }),
                                    tenant_id: loop_tenant_id.clone(),
                                    payload_json: payload.to_string(),
                                };
                                let mut buf = Vec::new();
                                if event_msg.encode(&mut buf).is_ok() {
                                    inner_publisher.publish_raw("acoustic.mood.shifted", buf).await;
                                    tracing::info!(event="MOOD_SHIFT_PROPAGATED", trace_id=%loop_trace_id, "Acoustic anomaly detected and published to RMQ.");
                                }
                            }
                            _ => {}
                        }
                    }
                });

                let mut m_client_rpc = m_client_inner.clone();
                tokio::spawn(async move {
                    let _ = m_client_rpc.stream_audio_to_call(stream_req).await;
                });

                let (_interrupt_tx, interrupt_rx) = mpsc::channel(10);
                let _ = orchestrator
                    .run_pipeline(
                        req.session_id.clone(),
                        "system".into(),
                        trace_id.clone(),
                        span_id.clone(),
                        tenant_id.clone(),
                        sdk_rx_rx,
                        sdk_tx_tx,
                        interrupt_rx,
                    )
                    .await;

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

// File: sentiric-telephony-action-service/src/pubsub/ghost_publisher.rs
// use chrono::Utc;
use lapin::{options::*, BasicProperties, Connection, ConnectionProperties};
// use serde_json::json;
use std::collections::VecDeque;
use std::sync::Arc;
use tokio::sync::{mpsc, Mutex};
use tokio::time::{sleep, Duration};
use tracing::{debug, error, info, warn};

// [ARCH-COMPLIANCE] Clippy Fix: Type Complexity uyarısını gidermek için Type Alias kullanımı.
type RmqPayload = (String, Vec<u8>);
type SharedGhostBuffer = Arc<Mutex<VecDeque<RmqPayload>>>;

const MAX_BUFFER_CAPACITY: usize = 1000;
const MAX_BACKOFF_SECS: u64 = 60; // Thundering herd koruması için max bekleme

#[derive(Clone)]
#[allow(dead_code)] // [ARCH-COMPLIANCE FIX]
pub struct GhostPublisher {
    tx: mpsc::Sender<RmqPayload>,
    tenant_id: String,
}

impl GhostPublisher {
    pub fn new(amqp_url: String, tenant_id: String) -> Self {
        let (tx, mut rx) = mpsc::channel::<RmqPayload>(1000);
        let buffer: SharedGhostBuffer =
            Arc::new(Mutex::new(VecDeque::with_capacity(MAX_BUFFER_CAPACITY)));

        // RX Task: Yeni olayları alır ve RAM'de tutar
        let buffer_clone = buffer.clone();
        tokio::spawn(async move {
            while let Some(msg) = rx.recv().await {
                let mut guard = buffer_clone.lock().await;
                if guard.len() >= MAX_BUFFER_CAPACITY {
                    guard.pop_front(); // FIFO Drop
                    warn!(event="GHOST_BUFFER_OVERFLOW", "RabbitMQ kapalı, buffer doldu. Eski olaylar RAM'den siliniyor (Graceful Degradation).");
                }
                guard.push_back(msg);
            }
        });

        // TX Task: RabbitMQ bağlantısını dener ve buffer'ı eritir
        let worker_buffer = buffer.clone();
        tokio::spawn(async move {
            let mut backoff = 1; // [ARCH-COMPLIANCE] Exponential Backoff Init

            loop {
                match Connection::connect(&amqp_url, ConnectionProperties::default()).await {
                    Ok(conn) => {
                        backoff = 1; // Bağlantı başarılı, backoff sıfırlanır
                        if let Ok(channel) = conn.create_channel().await {
                            info!(
                                event = "RMQ_CONNECTED",
                                "Ghost Publisher: RabbitMQ bağlantısı sağlandı. RAM'deki olaylar eritiliyor..."
                            );

                            loop {
                                let msg_opt = {
                                    let mut guard = worker_buffer.lock().await;
                                    guard.pop_front()
                                };

                                if let Some((routing_key, payload)) = msg_opt {
                                    let res = channel
                                        .basic_publish(
                                            "sentiric_events",
                                            &routing_key,
                                            BasicPublishOptions::default(),
                                            &payload,
                                            BasicProperties::default().with_delivery_mode(2),
                                        )
                                        .await;

                                    if res.is_err() {
                                        error!(
                                            event = "RMQ_PUBLISH_FAIL",
                                            "Mesaj gönderilemedi, RAM'e iade ediliyor."
                                        );

                                        // [ARCH-COMPLIANCE] OOM Koruması: Geri koyarken kapasite aşımını önle
                                        let mut guard = worker_buffer.lock().await;
                                        if guard.len() >= MAX_BUFFER_CAPACITY {
                                            warn!(event="GHOST_BUFFER_FULL_DISCARD", "Ring buffer tamamen dolu, başarısız olan en eski mesaj kalıcı olarak silindi.");
                                        } else {
                                            guard.push_front((routing_key, payload));
                                        }
                                        break; // Kopukluk var, dış döngüye (reconnect) dön
                                    } else {
                                        debug!(event="RMQ_PUBLISH_SUCCESS", routing_key=%routing_key, "Olay başarıyla RMQ'ya aktarıldı.");
                                    }
                                } else {
                                    sleep(Duration::from_millis(100)).await;
                                }

                                if conn.status().state() != lapin::ConnectionState::Connected {
                                    break;
                                }
                            }
                        }
                    }
                    Err(e) => {
                        warn!(event="RMQ_CONNECT_FAIL", error=%e, backoff_secs=backoff, "RabbitMQ ulaşılamaz durumda. Ghost Publisher devrede (Veriler RAM'de birikiyor).");
                    }
                }

                // [ARCH-COMPLIANCE] Reconnection Storm'u Engelle (Exponential Backoff)
                sleep(Duration::from_secs(backoff)).await;
                backoff = std::cmp::min(backoff * 2, MAX_BACKOFF_SECS);
            }
        });

        Self { tx, tenant_id }
    }

    #[allow(dead_code)] // [ARCH-COMPLIANCE FIX]
    pub async fn publish_terminate_request(&self, call_id: &str, trace_id: &str, reason: &str) {
        use chrono::Utc;
        use prost::Message;
        use sentiric_contracts::sentiric::event::v1::GenericEvent;
        use serde_json::json;

        // Anayasa gereği payload'un kendisi stringified JSON, zarf ise Protobuf GenericEvent olmalıdır
        let payload_json = json!({
            "callId": call_id,
            "reason": reason
        })
        .to_string();

        let event = GenericEvent {
            event_type: "call.terminate.request".to_string(),
            trace_id: trace_id.to_string(),
            timestamp: Some(prost_types::Timestamp {
                seconds: Utc::now().timestamp(),
                nanos: Utc::now().timestamp_subsec_nanos() as i32,
            }),
            tenant_id: self.tenant_id.clone(),
            payload_json,
        };

        let mut data = Vec::new();
        if event.encode(&mut data).is_ok() {
            let _ = self
                .tx
                .send(("call.terminate.request".to_string(), data))
                .await;
        }
    }
}

use chrono::Utc;
use lapin::{options::*, BasicProperties, Connection, ConnectionProperties};
use serde_json::json;
use std::collections::VecDeque;
use std::sync::Arc;
use tokio::sync::{mpsc, Mutex};
use tokio::time::{sleep, Duration};
use tracing::{debug, error, info, warn};

// [ARCH-COMPLIANCE] Clippy Fix: Type Complexity uyarısını gidermek için Type Alias kullanımı.
type RmqPayload = (String, Vec<u8>);
type SharedGhostBuffer = Arc<Mutex<VecDeque<RmqPayload>>>;

#[derive(Clone)]
pub struct GhostPublisher {
    tx: mpsc::Sender<RmqPayload>,
    tenant_id: String, // [ARCH-COMPLIANCE] Event payloadlarına eklenmek üzere saklanıyor
}

impl GhostPublisher {
    pub fn new(amqp_url: String, tenant_id: String) -> Self {
        let (tx, mut rx) = mpsc::channel::<RmqPayload>(1000);
        let buffer: SharedGhostBuffer = Arc::new(Mutex::new(VecDeque::with_capacity(1000)));

        let buffer_clone = buffer.clone();
        tokio::spawn(async move {
            while let Some(msg) = rx.recv().await {
                let mut guard = buffer_clone.lock().await;
                if guard.len() >= 1000 {
                    guard.pop_front(); // FIFO Drop
                    warn!(event="GHOST_BUFFER_OVERFLOW", "RabbitMQ kapalı, buffer doldu. Eski olaylar RAM'den siliniyor (Graceful Degradation).");
                }
                guard.push_back(msg);
            }
        });

        let worker_buffer = buffer.clone();
        tokio::spawn(async move {
            loop {
                match Connection::connect(&amqp_url, ConnectionProperties::default()).await {
                    Ok(conn) => {
                        if let Ok(channel) = conn.create_channel().await {
                            info!(
                                event = "RMQ_CONNECTED",
                                "Ghost Publisher: RabbitMQ bağlantısı sağlandı."
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
                                        worker_buffer
                                            .lock()
                                            .await
                                            .push_front((routing_key, payload));
                                        break; // Kopukluk var, dış döngüye dön
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
                        warn!(event="RMQ_CONNECT_FAIL", error=%e, "RabbitMQ ulaşılamaz durumda. Ghost Publisher devrede (Veriler RAM'de birikiyor).");
                    }
                }
                sleep(Duration::from_secs(5)).await;
            }
        });

        Self { tx, tenant_id }
    }

    pub async fn publish_terminate_request(&self, call_id: &str, trace_id: &str, reason: &str) {
        let payload = json!({
            "eventType": "call.terminate.request",
            "traceId": trace_id,
            "tenantId": self.tenant_id,
            "timestamp": Utc::now().to_rfc3339(),
            "payloadJson": json!({
                "callId": call_id,
                "reason": reason
            }).to_string()
        });

        let data = serde_json::to_vec(&payload).unwrap_or_default();
        let _ = self
            .tx
            .send(("call.terminate.request".to_string(), data))
            .await;
    }
}

# 📞 Sentiric Telephony Action Service (v1.0.1)

[![Status](https://img.shields.io/badge/status-production_ready-neon_green.svg)]()
[![Architecture](https://img.shields.io/badge/arch-Async_Rust-orange.svg)]()
[![Compliance](https://img.shields.io/badge/compliance-SUTS_v4.0-blue.svg)]()

**Sentiric Telephony Action Service (TAS)**, platformun **"Telekom İcracısı" (Execution Engine)** rolünü üstlenir. Agent servisinden veya doğrudan iş akışlarından (Workflow) gelen emirleri alır ve düşük seviyeli medya ile yapay zeka katmanlarını köprüler. 

**v1.0.1 Mimarisi** ile birlikte bu servis tamamen Rust'a taşınmış olup, iş mantığı SDK'ya ( `sentiric-ai-pipeline-sdk` ) devredilmiş ve kilitlenmesiz (lock-free) bir köprü (bridge) haline getirilmiştir.

---

## 🏛️ Mimari Prensipler (Platform Anayasası)

1. **Yapay Zeka Mantığı Yasaktır:** Bu servis kendi içerisinde prompt oluşturmaz, STT döngüsü yazmaz. Sadece `sentiric-ai-pipeline-sdk`'yı çağırır.
2. **Ghost Publisher (Graceful Degradation):** Eğer mesaj kuyruğuna (RabbitMQ) erişim koparsa sistem **ÇÖKMEZ (Panic-Free)**. İletilemeyen olaylar RAM üzerinde Ring Buffer'da (Maks 1000 adet) tutulur, ağ gelince iletilir. Dolduğunda sessizce (FIFO) eski olaylar silinir.
3. **Sıfır Güven (Zero Trust mTLS):** Diğer servislere yapılan gRPC isteklerinde `http://` protokolü kesinlikle reddedilir. Tüm iletişim sertifika tabanlı (mTLS) yürütülür.
4. **Log as Data (SUTS v4.0):** Standart `stdout` veya `println!` kullanımı yasaktır. Tüm loglar JSON formatında, makine tarafından okunabilir (`trace_id`, `span_id` ve `tenant_id` dahil) olarak basılır.

---

## ⚙️ Yapılandırma (Environment Variables)

Bu servis aşağıdaki çevre değişkenleri ile kontrol edilir:

| Değişken | Açıklama | Varsayılan |
| :--- | :--- | :--- |
| `ENV` | Çalışma ortamı (`production`, `development`) | `production` |
| `TENANT_ID` | **[ZORUNLU]** Veri izolasyonu için kiracı kimliği | *Yok (Bail atar)* |
| `WORKER_THREADS` | Nano-Edge (IoT) ortamları için CPU çekirdek limiti | *Tüm Çekirdekler* |
| `TELEPHONY_ACTION_SERVICE_HTTP_PORT` | Healthz (`/healthz`) portu | `13050` |
| `TELEPHONY_ACTION_SERVICE_GRPC_PORT` | gRPC Sunucu portu | `13051` |
| `MEDIA_SERVICE_TARGET_GRPC_URL` | Medya köprüsü hedef adresi | `https://media-service:13031` |
| `RABBITMQ_URL` | Ghost Publisher için mesaj kuyruğu URL'i | `amqp://localhost:5672` |
| `STT_GATEWAY_TARGET_GRPC_URL` | AI Pipeline SDK Parametresi (STT) | `https://stt-gateway...` |
| `DIALOG_SERVICE_TARGET_GRPC_URL` | AI Pipeline SDK Parametresi (LLM) | `https://dialog-serv...` |
| `TTS_GATEWAY_TARGET_GRPC_URL` | AI Pipeline SDK Parametresi (TTS) | `https://tts-gateway...` |
| `TELEPHONY_ACTION_SERVICE_CERT_PATH` | Sunucu Sertifikası (mTLS) | *Zorunlu* |
| `TELEPHONY_ACTION_SERVICE_KEY_PATH` | Sunucu Anahtarı (mTLS) | *Zorunlu* |
| `GRPC_TLS_CA_PATH` | Ortak CA Kök Sertifikası | *Zorunlu* |

---

## 🚀 Derleme ve Çalıştırma (Development)

Proje, SRE süreçlerini otomatize eden bir `Makefile` ile gelir.

```bash
# Kod formatlama, linter kontrolü ve testleri çalıştır (Teslimat Öncesi Zorunlu)
make all

# Release build al
make build

# Yerel ortamda çalıştırmak için örnek komut
TENANT_ID="sentiric_demo" \
TELEPHONY_ACTION_SERVICE_CERT_PATH="./certs/cert.pem" \
TELEPHONY_ACTION_SERVICE_KEY_PATH="./certs/key.pem" \
GRPC_TLS_CA_PATH="./certs/ca.crt" \
make run
```

---

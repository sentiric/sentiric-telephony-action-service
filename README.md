# 📞 Sentiric Telephony Action Service

[![Status](https://img.shields.io/badge/status-active-success.svg)]()
[![Language](https://img.shields.io/badge/language-Go_1.24-blue.svg)]()
[![Protocol](https://img.shields.io/badge/protocol-gRPC_Stream-green.svg)]()

**Telephony Action Service (TAS)**, Sentiric platformunun "Gerçek Zamanlı Ses İşleme Motoru"dur. Telefon hattından (Media Service) gelen ham sesi alır, yapay zeka servisleri (STT/Dialog/TTS) arasında dolaştırır ve sonucu tekrar sese çevirerek hatta basar.

## 🎯 Mimari Rolü

Bu servis bir **Orkestratör** değil, bir **İcracıdır (Executor)**.
*   **Emri Veren:** `Agent Service` (Süreci başlatır).
*   **İşi Yapan:** `Telephony Action Service` (Sesi taşır, söz kesmeyi yönetir).

### Pipeline Akışı (Full-Duplex)
1.  **Kulak:** `Media Service` -> RTP Sesini Alır.
2.  **Algı:** `STT Gateway` -> Sesi Metne Çevirir.
3.  **Beyin:** `Dialog Service` -> Metni Anlar ve Cevap Üretir.
4.  **Ağız:** `TTS Gateway` -> Cevabı Sese Çevirir.
5.  **Yayın:** `Media Service` -> Sesi Kullanıcıya Çalar.

## 🚀 Kurulum ve Çalıştırma

### Gereksinimler
*   Docker & Docker Compose
*   `sentiric-certificates` (Bir üst dizinde olmalı)

### Hızlı Başlangıç
```bash
# 1. Ortam dosyasını hazırla
make setup

# 2. Servisi başlat
make up

# 3. Logları izle
make logs
```

### Konfigürasyon (.env)
| Değişken | Açıklama |
|---|---|
| `TELEPHONY_ACTION_SERVICE_GRPC_PORT` | Dinleme portu (Default: 13111) |
| `MEDIA_SERVICE_TARGET_GRPC_URL` | Media Service adresi |
| `STT_GATEWAY_TARGET_GRPC_URL` | STT Gateway adresi |
| `DIALOG_SERVICE_TARGET_GRPC_URL` | Dialog Service adresi |
| `TTS_GATEWAY_TARGET_GRPC_URL` | TTS Gateway adresi |

## 🔒 Güvenlik (mTLS)
Bu servis **Zero Trust** mimarisine uygundur.
*   **Server:** İstemcilerden (Agent Service) geçerli bir sertifika bekler.
*   **Client:** Diğer servislere (Media, STT, vb.) bağlanırken kendi sertifikasını sunar.

Sertifikalar `sentiric-certificates` reposundan yönetilir.

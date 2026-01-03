# 📞 Sentiric Telephony Action Service - Görev ve Yol Haritası

Bu belge, servisin mevcut durumunu, bilinen teknik borçları ve prodüksiyon ortamına geçiş için yapılması gereken kritik entegrasyon görevlerini içerir.

## ✅ Tamamlananlar (v1.0.0)
- [x] **Altyapı:** Dockerfile, Makefile ve Docker Compose kurulumu.
- [x] **Güvenlik:** mTLS (Client & Server) entegrasyonu ve sertifika yönetimi.
- [x] **Core Pipeline:** `RunPipeline` metodunun, `Media` -> `STT` -> `Dialog` -> `TTS` akışını yöneten Go rutinleri.
- [x] **Mock Test:** Servisin tek başına ayağa kalktığı ve gRPC isteklerini kabul ettiği doğrulandı.

---

## 🚧 Kritik Teknik Borçlar (High Priority)

### 1. Gerçek Medya Entegrasyonu ve RTP Testi
Şu anki testler `localhost:10000` gibi mock bir port kullanıyor.
- [ ] **Görev:** `MediaService`'ten gerçek bir RTP oturumu (`AllocatePort`) alınıp, `RunPipeline`'a bu portun verilmesi.
- [ ] **Görev:** `RunPipelineRequest` protosunun güncellenmiş `MediaInfo` alanını kullanarak dinamik port dinlemesi yapılması.
- [ ] **Test:** `media-service/examples/call_simulator` kullanılarak gerçek ses verisi gönderilmesi ve TAS'ın bu sesi STT'ye aktardığının doğrulanması.

### 2. Full-Stack Docker Compose (Entegrasyon Ortamı)
Mevcut `docker-compose.yml` sadece servisi ayağa kaldırır.
- [ ] **Görev:** `docker-compose.integration.yml` dosyası oluşturulmalı.
- [ ] **İçerik:** `media-service`, `stt-gateway`, `tts-gateway`, `dialog-service` ve `minio` servislerini içermeli.
- [ ] **Hedef:** Tek komutla (`make up-full`) tüm ses işleme zincirinin ayağa kalkması.

### 3. Production Hardening
- [ ] **Görev:** Graceful Shutdown süresinin (termination grace period) uzun süren çağrılar için optimize edilmesi (şu an 5sn, ideali 30sn+).
- [ ] **Görev:** Prometheus metriklerinin (aktif çağrı sayısı, pipeline hataları, STT gecikmesi) implemente edilmesi.

---

## 🧪 Nasıl Test Edilir?

### Standalone (Tek Başına)
```bash
make up       # Servisi başlat
make logs     # Logları izle
# Ayrı terminalde:
grpcurl -authority telephony-action-service \
  -cacert ../sentiric-certificates/certs/ca.crt \
  -cert ../sentiric-certificates/certs/agent-service.crt \
  -key ../sentiric-certificates/certs/agent-service.key \
  -d '{"call_id": "test", "session_id": "sess-1"}' \
  localhost:13111 sentiric.telephony.v1.TelephonyActionService/RunPipeline
# 📞 Sentiric Telephony Action Service - Görev Listesi

Bu servisin mevcut ve gelecekteki tüm geliştirme görevleri, platformun merkezi görev yönetimi reposu olan **`sentiric-tasks`**'ta yönetilmektedir.

➡️ **[Aktif Görev Panosuna Git](https://github.com/sentiric/sentiric-tasks/blob/main/TASKS.md)**

---
Bu belge, servise özel, çok küçük ve acil görevler için geçici bir not defteri olarak kullanılabilir.

## Faz 1: Minimal İşlevsellik (INFRA-02)
- [x] Temel Go projesi ve Dockerfile oluşturuldu.
- [x] Tüm RPC'ler için gRPC sunucusu iskeleti (`PlayAudio`, `TerminateCall`, vb.) eklendi.
- [ ] Media Service, TTS Gateway ve Signaling/B2BUA için gRPC istemcileri eklenecek. (ORCH-01)
- [ ] `PlayAudio` RPC'sinin entegre testleri yazılacak. (ORCH-02)
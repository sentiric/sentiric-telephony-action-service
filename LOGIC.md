# 🧬 Telephony Action Logic
1. **Pipeline Orchestration:** `ai-pipeline-sdk`'yı başlatır ve Media Service'ten gelen RTP akışını (16kHz PCM) SDK'ya enjekte eder.
2. **Deadlock Guard:** Media Service'e yapılan asenkron stream çağrıları bloklanmamalıdır (Tokio spawn).
3. **Unified Bridge:** Handover sinyali geldiğinde AI Pipeline'ı susturup, paketleri ajanın WebSocket'ine "Relay" modunda aktarır.

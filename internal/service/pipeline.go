package service

import (
	"context"
	"fmt"
	"io"
	"sync"
    // "time" kaldırıldı

	"github.com/rs/zerolog"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	"google.golang.org/grpc/metadata"
)

type PipelineManager struct {
	clients *client.Clients
	log     zerolog.Logger
}

func NewPipelineManager(clients *client.Clients, log zerolog.Logger) *PipelineManager {
	return &PipelineManager{clients: clients, log: log}
}

// RunPipeline: Ses döngüsünü başlatır ve yönetir.
func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, rtpPort uint32) error {
	log := pm.log.With().Str("call_id", callID).Str("session_id", sessionID).Logger()
	log.Info().Msg("🚀 Pipeline Başlatılıyor: Media <-> STT <-> Dialog <-> TTS")

	// 1. Metadata (TraceID) Hazırla
	md := metadata.Pairs("x-trace-id", sessionID)
	outCtx := metadata.NewOutgoingContext(ctx, md)

	// -------------------------------------------------------------------------
	// ADIM 1: TÜM BAĞLANTILARI AÇ (STREAM SETUP)
	// -------------------------------------------------------------------------

	// A. Media Record (Kulak)
	mediaRecStream, err := pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{
		ServerRtpPort: rtpPort,
		TargetSampleRate: nil, // Default 16000
	})
	if err != nil { return fmt.Errorf("media record stream failed: %w", err) }

	// B. Media Playback (Ağız)
	mediaPlayStream, err := pm.clients.Media.StreamAudioToCall(outCtx)
	if err != nil { return fmt.Errorf("media play stream failed: %w", err) }
	// Handshake: Call ID'yi bildir
	if err := mediaPlayStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return fmt.Errorf("media play handshake failed: %w", err)
	}

	// C. STT (Kulak -> Metin)
	sttStream, err := pm.clients.STT.TranscribeStream(outCtx)
	if err != nil { return fmt.Errorf("stt stream failed: %w", err) }

	// D. Dialog (Beyin)
	dialogStream, err := pm.clients.Dialog.StreamConversation(outCtx)
	if err != nil { return fmt.Errorf("dialog stream failed: %w", err) }
	// Handshake: Session Config
	if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); err != nil { return fmt.Errorf("dialog config failed: %w", err) }

	log.Info().Msg("✅ Tüm stream bağlantıları kuruldu. Veri akışı başlıyor.")

	// -------------------------------------------------------------------------
	// ADIM 2: KOORDİNASYON VE SÖZ KESME MEKANİZMASI
	// -------------------------------------------------------------------------
	
	// Interruption kontrolü için
	var ttsCancelFunc context.CancelFunc
	var ttsMutex sync.Mutex

	// Hata yakalama
	errChan := make(chan error, 10)
	// Alt rutinleri beklemek için
	var wg sync.WaitGroup

	// Pipeline'ın genel iptali için context
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// -------------------------------------------------------------------------
	// ADIM 3: GOROUTINE'LER (PARALEL GÖREVLER)
	// -------------------------------------------------------------------------

	// TASK 1: Media -> STT (Sesi Taşı)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sttStream.CloseSend()
		log.Debug().Msg("Task 1 Başladı: Media -> STT")
		
		for {
			chunk, err := mediaRecStream.Recv()
			if err == io.EOF { return }
			if err != nil { errChan <- fmt.Errorf("media recv: %w", err); return }

			// STT'ye gönder
			if err := sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData}); err != nil {
				errChan <- fmt.Errorf("stt send: %w", err)
				return
			}
		}
	}()

	// TASK 2: STT -> Dialog (Metni Taşı ve Söz Kesmeyi Yönet)
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Debug().Msg("Task 2 Başladı: STT -> Dialog")
		
		for {
			res, err := sttStream.Recv()
			if err == io.EOF { return }
			if err != nil { errChan <- fmt.Errorf("stt recv: %w", err); return }

			// INTERRUPTION LOGIC
			// Eğer kullanıcı konuşuyorsa (Partial result) ve henüz final değilse:
			// Mevcut TTS'i sustur.
			if !res.IsFinal && len(res.PartialTranscription) > 2 {
				ttsMutex.Lock()
				if ttsCancelFunc != nil {
					log.Warn().Str("input", res.PartialTranscription).Msg("🛑 SÖZ KESME: TTS Durduruluyor.")
					ttsCancelFunc() // Mevcut TTS işlemini iptal et
					ttsCancelFunc = nil
				}
				ttsMutex.Unlock()
			}

			if res.IsFinal {
				log.Info().Str("user_text", res.PartialTranscription).Msg("🗣️ Kullanıcı")
				
				// Dialog'a ilet
				if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_TextInput{TextInput: res.PartialTranscription},
				}); err != nil { errChan <- err; return }

				if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
				}); err != nil { errChan <- err; return }
			}
		}
	}()

	// TASK 3: Dialog -> TTS (Metni Sese Çevir)
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Debug().Msg("Task 3 Başladı: Dialog -> TTS")

		for {
			dRes, err := dialogStream.Recv()
			if err == io.EOF { return }
			if err != nil { errChan <- fmt.Errorf("dialog recv: %w", err); return }

			text := dRes.GetTextResponse()
			if text != "" {
				log.Info().Str("ai_text", text).Msg("🤖 AI")

				// Yeni TTS Context oluştur (İptal edilebilir)
				ttsCtx, tCancel := context.WithCancel(outCtx)
				
				ttsMutex.Lock()
				if ttsCancelFunc != nil { ttsCancelFunc() } // Önceki varsa kapat
				ttsCancelFunc = tCancel
				ttsMutex.Unlock()

				// TTS işlemini bloklamadan (asenkron değil, stream içinde sıralı) yapmalıyız
				// Basitlik için burada bloklayarak gönderiyoruz (Cümle cümle).
				
				if err := pm.streamTTS(ttsCtx, text, mediaPlayStream); err != nil {
					// İptal hatası normaldir (Söz kesme)
					if err != context.Canceled {
						log.Error().Err(err).Msg("TTS Stream Hatası")
					}
				}
			}
		}
	}()

	// Hata veya Bitiş Bekle
	select {
	case <-ctx.Done():
		log.Info().Msg("Pipeline kapatılıyor.")
	case err := <-errChan:
		log.Error().Err(err).Msg("Pipeline kritik hata ile sonlandı.")
		return err
	}
	
	return nil
}

// streamTTS: Metni TTS servisine gönderir ve gelen sesi Media servisine basar.
func (pm *PipelineManager) streamTTS(
	ctx context.Context, 
	text string, 
	mediaStream mediav1.MediaService_StreamAudioToCallClient,
) error {
	
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text: text,
		VoiceId: "coqui:default",
		TextType: ttsv1.TextType_TEXT_TYPE_TEXT,
		AudioConfig: &ttsv1.AudioConfig{
            // KRİTİK DÜZELTME: 24000Hz (Coqui Default) yerine 16000Hz.
            // Media Service içindeki G.711 encoder'ı 16kHz input bekler.
            // Bu uyumsuzluk düzeltilince ses hızı normale dönecek.
			SampleRateHertz: 16000, 
			AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE,
		},
        // Hız ayarı (İsteğe bağlı, normal hız için 1.0)
        Prosody: &ttsv1.ProsodyConfig{
            Rate: 1.0,
        },
	}

	ttsStream, err := pm.clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil { return err }

	for {
		chunk, err := ttsStream.Recv()
		if err == io.EOF { 
            log.Info().Msg("TTS akışı tamamlandı (EOF).")
            return nil 
        }
		if err != nil { 
            log.Error().Err(err).Msg("TTS akış hatası")
            return err 
        }

		// --- YENİ LOG ---
        log.Debug().Int("bytes", len(chunk.AudioContent)).Msg("TTS chunk alındı, Media'ya gönderiliyor")

		if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent}); err != nil {
            log.Error().Err(err).Msg("Media stream send hatası")
			return err
		}
	
	}
}
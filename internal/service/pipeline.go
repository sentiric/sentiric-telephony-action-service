package service

import (
	"context"
	"io"
	"sync"

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

// RunPipeline: Bir telefon çağrısı için uçtan uca ses döngüsünü başlatır.
func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, rtpPort uint32) error {
	log := pm.log.With().Str("call_id", callID).Str("session_id", sessionID).Logger()
	log.Info().Msg("Telephony Pipeline başlatılıyor...")

	// Context'e TraceID ekle
	ctx = metadata.AppendToOutgoingContext(ctx, "x-trace-id", sessionID)

	// 1. STT Stream Başlat
	sttStream, err := pm.clients.STT.TranscribeStream(ctx)
	if err != nil {
		return pm.logError(err, "STT stream başlatılamadı")
	}

	// 2. Dialog Stream Başlat
	dialogStream, err := pm.clients.Dialog.StreamConversation(ctx)
	if err != nil {
		return pm.logError(err, "Dialog stream başlatılamadı")
	}
	// Dialog Config Gönder
	if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); err != nil {
		return pm.logError(err, "Dialog config gönderilemedi")
	}

	// 3. Media Input Stream (Kulak) Başlat
	// Media Service'ten gelen RTP paketlerini dinle
	mediaRecStream, err := pm.clients.Media.RecordAudio(ctx, &mediav1.RecordAudioRequest{
		ServerRtpPort: rtpPort,
		TargetSampleRate: nil, // Default 16k
	})
	if err != nil {
		return pm.logError(err, "Media record stream başlatılamadı")
	}

	// 4. Media Output Stream (Ağız) Başlat
	// TTS'ten gelen sesi Media Service'e basmak için
	mediaPlayStream, err := pm.clients.Media.StreamAudioToCall(ctx)
	if err != nil {
		return pm.logError(err, "Media playback stream başlatılamadı")
	}
	// İlk mesajda CallID gönder
	if err := mediaPlayStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return pm.logError(err, "Media playback handshake başarısız")
	}

	// --- KOORDİNASYON KANALLARI ---
	// Söz kesme (Interruption) sinyali için
	interruptChan := make(chan bool, 1)
	
	var wg sync.WaitGroup

	// GÖREV A: Ses Taşıma (Media -> STT)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			chunk, err := mediaRecStream.Recv()
			if err == io.EOF || err != nil {
				log.Warn().Err(err).Msg("Media input kesildi")
				sttStream.CloseSend()
				return
			}
			// STT'ye gönder
			if err := sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData}); err != nil {
				log.Error().Err(err).Msg("STT send error")
				return
			}
		}
	}()

	// GÖREV B: STT Sonuçlarını İşle (STT -> Dialog & Interruption)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			res, err := sttStream.Recv()
			if err == io.EOF || err != nil {
				log.Warn().Err(err).Msg("STT output kesildi")
				return
			}

			// Eğer kullanıcı konuşuyorsa (Partial result bile olsa), TTS'i sustur
			if len(res.PartialTranscription) > 5 { // Gürültü filtresi
				select {
				case interruptChan <- true:
					log.Debug().Msg("🗣️ Söz kesme algılandı (Interruption)")
				default:
				}
			}

			if res.IsFinal {
				log.Info().Str("text", res.PartialTranscription).Msg("👤 Kullanıcı (STT Final)")
				
				// Dialog'a metni gönder
				dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_TextInput{TextInput: res.PartialTranscription},
				})
				// Final olduğunu bildir (Dialog LLM'e sorsun)
				dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
				})
			}
		}
	}()

	// GÖREV C: Dialog -> TTS (Metin Gelince Sese Çevir)
	// Bu kısım biraz karmaşık: Her cümle için yeni bir TTS stream'i açmak gerekebilir
	// veya TTS Gateway tek stream üzerinden çalışıyorsa ona göre ayarlanmalı.
	// Şimdilik sırayla işliyoruz.
	wg.Add(1)
	go func() {
		defer wg.Done()
		
		// TTS Streaming için kuyruk (Sıralı konuşma)
		// Basitlik için her gelen metni anında TTS'e gönderiyoruz
		// Interruption geldiğinde bu döngü içindeki TTS işlemi iptal edilmeli (Gelişmiş versiyonda)
		
		for {
			dRes, err := dialogStream.Recv()
			if err == io.EOF || err != nil { return }

			if text := dRes.GetTextResponse(); text != "" {
				log.Info().Str("text", text).Msg("🤖 AI Yanıtı")
				
				// TTS İsteği (Stream)
				ttsReq := &ttsv1.SynthesizeStreamRequest{
					Text: text,
					VoiceId: "coqui:default", // Config'den gelmeli
					TextType: ttsv1.TextType_TEXT_TYPE_TEXT,
					AudioConfig: &ttsv1.AudioConfig{SampleRateHertz: 8000, AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE},
				}
				
				ttsStream, err := pm.clients.TTS.SynthesizeStream(ctx, ttsReq)
				if err != nil {
					log.Error().Err(err).Msg("TTS stream başlatılamadı")
					continue
				}

				// TTS -> Media
				for {
					// Interruption kontrolü
					select {
					case <-interruptChan:
						log.Warn().Msg("⛔ TTS susturuldu (Interruption)")
						goto NEXT_TURN // TTS döngüsünü kır, sonraki Dialog mesajına geç
					default:
					}

					chunk, err := ttsStream.Recv()
					if err == io.EOF { break }
					if err != nil { break }

					// Media Service'e gönder
					if err := mediaPlayStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent}); err != nil {
						log.Error().Err(err).Msg("Media play error")
						return
					}
				}
				NEXT_TURN:
			}
		}
	}()

	wg.Wait()
	return nil
}

func (pm *PipelineManager) logError(err error, msg string) error {
	pm.log.Error().Err(err).Msg(msg)
	return err
}
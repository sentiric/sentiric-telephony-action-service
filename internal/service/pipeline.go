package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
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

	md := metadata.Pairs("x-trace-id", sessionID)
	outCtx := metadata.NewOutgoingContext(ctx, md)

	// 1. STREAM BAĞLANTILARI
	mediaRecStream, err := pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{
		ServerRtpPort: rtpPort,
		TargetSampleRate: nil,
	})
	if err != nil { return fmt.Errorf("media record stream failed: %w", err) }

	mediaPlayStream, err := pm.clients.Media.StreamAudioToCall(outCtx)
	if err != nil { return fmt.Errorf("media play stream failed: %w", err) }
	
	if err := mediaPlayStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return fmt.Errorf("media play handshake failed: %w", err)
	}

	sttStream, err := pm.clients.STT.TranscribeStream(outCtx)
	if err != nil { return fmt.Errorf("stt stream failed: %w", err) }

	dialogStream, err := pm.clients.Dialog.StreamConversation(outCtx)
	if err != nil { return fmt.Errorf("dialog stream failed: %w", err) }
	
	if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); err != nil { return fmt.Errorf("dialog config failed: %w", err) }

	log.Info().Msg("✅ Tüm stream bağlantıları kuruldu.")

	// 2. KONTROL YAPILARI
	var ttsCancelFunc context.CancelFunc
	var ttsMutex sync.Mutex
	errChan := make(chan error, 10)
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// TASK 1: Media -> STT
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sttStream.CloseSend()
		for {
			chunk, err := mediaRecStream.Recv()
			if err == io.EOF { return }
			if err != nil { errChan <- fmt.Errorf("media recv: %w", err); return }
			if err := sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData}); err != nil {
				errChan <- fmt.Errorf("stt send: %w", err)
				return
			}
		}
	}()

	// TASK 2: STT -> Dialog (Söz Kesme Dahil)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			res, err := sttStream.Recv()
			if err == io.EOF { return }
			if err != nil { errChan <- fmt.Errorf("stt recv: %w", err); return }

			// Söz Kesme (Interruption) Mantığı
			if !res.IsFinal && len(res.PartialTranscription) > 5 {
				ttsMutex.Lock()
				if ttsCancelFunc != nil {
					log.Warn().Str("input", res.PartialTranscription).Msg("🛑 SÖZ KESME: TTS Durduruluyor.")
					ttsCancelFunc()
					ttsCancelFunc = nil
				}
				ttsMutex.Unlock()
			}

			if res.IsFinal {
				log.Info().Str("user_text", res.PartialTranscription).Msg("🗣️ Kullanıcı")
				if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_TextInput{TextInput: res.PartialTranscription},
				}); err != nil { errChan <- err; return }
				
				if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
				}); err != nil { errChan <- err; return }
			}
		}
	}()

	// TASK 3: Dialog -> TTS (AKILLI BUFFERLAMA)
	wg.Add(1)
	go func() {
		defer wg.Done()
		
		// Cümle birleştirme tamponu
		var sentenceBuffer strings.Builder
		
		for {
			dRes, err := dialogStream.Recv()
			if err == io.EOF { return }
			if err != nil { errChan <- fmt.Errorf("dialog recv: %w", err); return }

			// LLM'den gelen parçayı al
			token := dRes.GetTextResponse()
			if token == "" { continue }
			
			sentenceBuffer.WriteString(token)
			currentText := sentenceBuffer.String()

			// Cümle Bitiş Kontrolü (Noktalama işaretleri)
			// Sadece cümlenin sonunda noktalama varsa ve cümle yeterince uzunsa gönder.
			isSentenceEnd := strings.ContainsAny(token, ".?!:;") || strings.HasSuffix(strings.TrimSpace(currentText), ".")
			isLongEnough := len(currentText) > 50 // Çok uzun cümleleri beklemeden gönder (latency için)
			
			// Eğer cümle bittiyse VEYA çok uzadıysa VEYA final sinyali geldiyse
			shouldFlush := isSentenceEnd || isLongEnough || dRes.GetIsFinalResponse()

			if shouldFlush {
				textToSend := strings.TrimSpace(currentText)
				
				if textToSend != "" {
					log.Info().Str("tts_text", textToSend).Msg("🤖 TTS'e Cümle Gönderiliyor")

					ttsCtx, tCancel := context.WithCancel(outCtx)
					
					ttsMutex.Lock()
					if ttsCancelFunc != nil { ttsCancelFunc() }
					ttsCancelFunc = tCancel
					ttsMutex.Unlock()

					// Bloklayıcı çağrı (Sıralı konuşma için)
					if err := pm.streamTTS(ttsCtx, textToSend, mediaPlayStream); err != nil {
						if err != context.Canceled {
							log.Error().Err(err).Msg("TTS Stream Hatası")
						}
					}
				}
				// Buffer'ı temizle
				sentenceBuffer.Reset()
			}
		}
	}()

	select {
	case <-ctx.Done():
		log.Info().Msg("Pipeline kapatılıyor.")
	case err := <-errChan:
		log.Error().Err(err).Msg("Pipeline kritik hata ile sonlandı.")
		return err
	}
	
	return nil
}

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
			SampleRateHertz: 16000, 
			AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE,
		},
        Prosody: &ttsv1.ProsodyConfig{ Rate: 1.0 },
	}

	ttsStream, err := pm.clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil { return err }

	for {
		chunk, err := ttsStream.Recv()
		if err == io.EOF { 
            pm.log.Debug().Msg("TTS cümle tamamlandı.")
            return nil 
        }
		if err != nil { return err }

		if len(chunk.AudioContent) > 0 {
			if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent}); err != nil {
				return err
			}
		}
	}
}
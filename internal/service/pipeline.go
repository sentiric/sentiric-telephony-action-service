// sentiric-telephony-action-service/internal/service/pipeline.go
package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	eventv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/event/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"google.golang.org/grpc/metadata"
)

// KRİTİK DÜZELTME: PipelineManager struct tanımı eklendi (Hata 6)
type PipelineManager struct {
	clients  *client.Clients
	log      zerolog.Logger
	mediator *Mediator
}

func NewPipelineManager(clients *client.Clients, log zerolog.Logger) *PipelineManager {
	return &PipelineManager{
		clients:  clients,
		log:      log,
		mediator: NewMediator(clients, log),
	}
}

func (pm *PipelineManager) GetClients() *client.Clients {
	return pm.clients
}

// RunPipeline: Ses döngüsünü başlatır ve yönetir (RX, TX, Sinyal).
func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, rtpPort uint32) error {
	log := pm.log.With().Str("call_id", callID).Str("session_id", sessionID).Logger()
	log.Info().Msg("🚀 Pipeline Başlatılıyor: Media <-> STT <-> Dialog <-> TTS")

	md := metadata.Pairs("x-trace-id", sessionID)
	outCtx := metadata.NewOutgoingContext(ctx, md)

	// 1. STREAM BAĞLANTILARI
	mediaRecStream, err := pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{ServerRtpPort: rtpPort})
	if err != nil {
		return fmt.Errorf("media record stream failed: %w", err)
	}

	sttStream, err := pm.clients.STT.TranscribeStream(outCtx)
	if err != nil {
		return fmt.Errorf("stt stream failed: %w", err)
	}

	dialogStream, err := pm.clients.Dialog.StreamConversation(outCtx)
	if err != nil {
		return fmt.Errorf("dialog stream failed: %w", err)
	}

	// Dialog Config Handshake
	if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); err != nil {
		return fmt.Errorf("dialog config failed: %w", err)
	}

	log.Info().Msg("✅ Tüm stream bağlantıları kuruldu.")

	// 2. KONTROL YAPILARI
	var ttsCancelFunc context.CancelFunc
	var ttsMutex sync.Mutex
	errChan := make(chan error, 10)
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// MediaInfo'yu oluştur (KRİTİK DÜZELTME: Doğru tip eventv1.MediaInfo)
	mediaInfo := &eventv1.MediaInfo{
		ServerRtpPort: rtpPort,
		// CallerRtpAddr boş bırakıldı, Latching'e güveniliyor
	}

	// TASK 1: Media -> STT
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sttStream.CloseSend()
		for {
			chunk, err := mediaRecStream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				errChan <- fmt.Errorf("media recv: %w", err)
				return
			}
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
			if err == io.EOF {
				return
			}
			if err != nil {
				errChan <- fmt.Errorf("stt recv: %w", err)
				return
			}

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
				}); err != nil {
					errChan <- err
					return
				}

				if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
				}); err != nil {
					errChan <- err
					return
				}
			}
		}
	}()

	// TASK 3: Dialog -> TTS (TX)
	wg.Add(1)
	go func() {
		defer wg.Done()

		var sentenceBuffer strings.Builder

		for {
			dRes, err := dialogStream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				errChan <- fmt.Errorf("dialog recv: %w", err)
				return
			}

			token := dRes.GetTextResponse()
			if token == "" {
				continue
			}

			sentenceBuffer.WriteString(token)
			currentText := sentenceBuffer.String()

			// Cümle Bitiş Kontrolü
			isSentenceEnd := strings.ContainsAny(token, ".?!:;") || strings.HasSuffix(strings.TrimSpace(currentText), ".")
			isLongEnough := len(currentText) > 50

			shouldFlush := isSentenceEnd || isLongEnough || dRes.GetIsFinalResponse()

			if shouldFlush {
				textToSend := strings.TrimSpace(currentText)

				if textToSend != "" {
					log.Info().Str("tts_text", textToSend).Msg("🤖 TTS'e Cümle Gönderiliyor")

					ttsCtx, tCancel := context.WithCancel(outCtx)

					ttsMutex.Lock()
					if ttsCancelFunc != nil {
						ttsCancelFunc()
					}
					ttsCancelFunc = tCancel
					ttsMutex.Unlock()

					// MEDIATOR'A YETKİ DEVRİ (SpeakText)
					if err := pm.mediator.SpeakText(ttsCtx, callID, textToSend, "coqui:default", mediaInfo); err != nil {
						if err != context.Canceled {
							log.Error().Err(err).Msg("TTS Stream Hatası")
						}
					}

					ttsMutex.Lock()
					ttsCancelFunc = nil
					ttsMutex.Unlock()
				}
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

	wg.Wait()
	return nil
}

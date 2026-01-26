// internal/service/pipeline.go
package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

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

func (pm *PipelineManager) GetClients() *client.Clients {
	return pm.clients
}

// RunPipeline: Sesli iletişim döngüsünü başlatır (Full-Duplex)
func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, rtpPort uint32) error {
	logger := pm.log.With().Str("call_id", callID).Str("session_id", sessionID).Logger()
	logger.Info().Msg("🚀 Ses Pipeline'ı Başlatılıyor...")

	// Metadata Propagation (Trace ID)
	md := metadata.Pairs("x-trace-id", sessionID)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// 1. STREAM'LERI HAZIRLA
	// Media -> Receive (Kullanıcıyı Duy)
	mediaRecStream, err := pm.clients.Media.RecordAudio(ctx, &mediav1.RecordAudioRequest{
		ServerRtpPort: rtpPort,
		TargetSampleRate: nil, // Default 16k
	})
	if err != nil { return fmt.Errorf("media record stream failed: %w", err) }

	// Media -> Send (Kullanıcıya Konuş)
	mediaPlayStream, err := pm.clients.Media.StreamAudioToCall(ctx)
	if err != nil { return fmt.Errorf("media play stream failed: %w", err) }
	// Handshake
	if err := mediaPlayStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return fmt.Errorf("media handshake failed: %w", err)
	}

	// STT -> Transcribe
	sttStream, err := pm.clients.STT.TranscribeStream(ctx)
	if err != nil { return fmt.Errorf("stt stream failed: %w", err) }

	// Dialog -> Logic
	dialogStream, err := pm.clients.Dialog.StreamConversation(ctx)
	if err != nil { return fmt.Errorf("dialog stream failed: %w", err) }
	// Dialog Config
	if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); err != nil { return fmt.Errorf("dialog config failed: %w", err) }

	logger.Info().Msg("✅ Tüm kanallar aktif. Dinleme başlıyor.")

	// 2. KOORDİNASYON & SÖZ KESME (BARGE-IN)
	// TTS işlemini iptal etmek için kullanılacak context ve kilit
	var ttsCancelFunc context.CancelFunc
	var ttsMutex sync.Mutex
	
	errChan := make(chan error, 10)
	// Ana pipeline iptali için
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	var wg sync.WaitGroup

	// --- TASK A: Media -> STT (Dinleme Hattı) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer sttStream.CloseSend() // STT'ye "ses bitti" de
		for {
			select {
			case <-ctx.Done():
				return
			default:
				chunk, err := mediaRecStream.Recv()
				if err == io.EOF { return }
				if err != nil {
					errChan <- fmt.Errorf("media recv error: %w", err)
					return
				}
				// Gelen sesi STT'ye aktar
				if err := sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData}); err != nil {
					errChan <- fmt.Errorf("stt send error: %w", err)
					return
				}
			}
		}
	}()

	// --- TASK B: STT -> Dialog (Anlama Hattı & Interruption) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				res, err := sttStream.Recv()
				if err == io.EOF { return }
				if err != nil {
					errChan <- fmt.Errorf("stt recv error: %w", err)
					return
				}

				// INTERRUPTION LOGIC
				// Eğer kullanıcı konuşuyorsa (ve metin boş değilse), mevcut TTS'i sustur.
				transcription := strings.TrimSpace(res.PartialTranscription)
				if len(transcription) > 2 {
					ttsMutex.Lock()
					if ttsCancelFunc != nil {
						logger.Warn().Str("interrupt", transcription).Msg("🛑 SÖZ KESME: TTS durduruluyor.")
						ttsCancelFunc() // Aktif TTS'i iptal et
						ttsCancelFunc = nil
					}
					ttsMutex.Unlock()
				}

				// Final sonuçsa Dialog'a gönder
				if res.IsFinal {
					logger.Info().Str("user", transcription).Msg("🗣️ Kullanıcı")
					if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
						Payload: &dialogv1.StreamConversationRequest_TextInput{TextInput: transcription},
					}); err != nil {
						errChan <- err
						return
					}
					// Dialog'a "Girdi Bitti" sinyali
					if err := dialogStream.Send(&dialogv1.StreamConversationRequest{
						Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
					}); err != nil {
						errChan <- err
						return
					}
				}
			}
		}
	}()

	// --- TASK C: Dialog -> TTS (Konuşma Hattı - Smart Buffering) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		var sentenceBuffer strings.Builder
		
		for {
			select {
			case <-ctx.Done():
				return
			default:
				dRes, err := dialogStream.Recv()
				if err == io.EOF { return }
				if err != nil {
					errChan <- fmt.Errorf("dialog recv error: %w", err)
					return
				}

				token := dRes.GetTextResponse()
				if token == "" { continue }

				sentenceBuffer.WriteString(token)
				currentText := sentenceBuffer.String()

				// Cümle bitişi kontrolü (. ? ! : \n)
				// veya tampon çok dolduysa
				isEnd := strings.ContainsAny(token, ".?!:\n") || dRes.GetIsFinalResponse()
				if isEnd && len(strings.TrimSpace(currentText)) > 0 {
					textToSpeak := currentText
					sentenceBuffer.Reset()
					
					logger.Info().Str("ai", textToSpeak).Msg("🤖 AI Yanıtlıyor")

					// Yeni bir iptal edilebilir context oluştur
					ttsCtx, tCancel := context.WithCancel(ctx)
					
					ttsMutex.Lock()
					// Önceki varsa iptal et (gerçi Dialog sırayla gönderir ama güvenlik için)
					if ttsCancelFunc != nil { ttsCancelFunc() }
					ttsCancelFunc = tCancel
					ttsMutex.Unlock()

					// TTS işlemini bloklayarak yap (sırayla konuşsun)
					if err := pm.streamTTS(ttsCtx, textToSpeak, mediaPlayStream); err != nil {
						// Eğer context cancelled hatasıysa (söz kesildiyse) bu bir hata değildir
						if err != context.Canceled && !strings.Contains(err.Error(), "canceled") {
							logger.Error().Err(err).Msg("TTS oynatma hatası")
						}
					}
					
					// İş bitince temizle
					ttsMutex.Lock()
					if ttsCancelFunc != nil { ttsCancelFunc = nil } // Kendimizi temizle
					tCancel() // Kaynakları bırak
					ttsMutex.Unlock()
				}
			}
		}
	}()

	// Hata veya Kapanma Bekle
	select {
	case <-ctx.Done():
		logger.Info().Msg("Pipeline normal şekilde kapatıldı.")
	case err := <-errChan:
		logger.Error().Err(err).Msg("Pipeline kritik hata ile sonlandı.")
		return err
	}

	return nil
}

// streamTTS: Metni TTS'e gönderir ve gelen sesi Media'ya basar.
func (pm *PipelineManager) streamTTS(
	ctx context.Context, 
	text string, 
	mediaStream mediav1.MediaService_StreamAudioToCallClient,
) error {
	
	// TTS İsteği
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text: text,
		VoiceId: "coqui:default", // Config'den alınabilir
		AudioConfig: &ttsv1.AudioConfig{
			SampleRateHertz: 16000, 
			AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE,
		},
	}

	ttsStream, err := pm.clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil { return err }

	// TTS'den Media'ya Akış
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			chunk, err := ttsStream.Recv()
			if err == io.EOF { return nil }
			if err != nil { return err }

			if len(chunk.AudioContent) > 0 {
				if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{
					AudioChunk: chunk.AudioContent,
				}); err != nil {
					return err
				}
			}
		}
	}
}
// sentiric-telephony-action-service/internal/service/pipeline.go
package service

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rs/zerolog"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	eventv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/event/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"google.golang.org/grpc/metadata"
)

type PipelineManager struct {
	clients  *client.Clients
	config   *config.Config
	log      zerolog.Logger
	mediator *Mediator
}

func NewPipelineManager(clients *client.Clients, cfg *config.Config, log zerolog.Logger) *PipelineManager {
	return &PipelineManager{
		clients:  clients,
		config:   cfg,
		log:      log,
		mediator: NewMediator(clients, cfg, log),
	}
}

// triggerFailsafe: AI çöktüğünde müşteriye sessizlik yerine arıza anonsu okutur.
func (pm *PipelineManager) triggerFailsafe(ctx context.Context, callID string, rtpPort uint32, targetAddr string, l zerolog.Logger) {
	l.Warn().Str("event", "FAILSAFE_TRIGGERED").Msg("🛡️ AI motorları çöktü. Failsafe (Arıza Anonsu) devreye giriyor.")

	// Media servisine "Teknik Sorun" anonsunu çalması emri verilir.
	_, err := pm.clients.Media.PlayAudio(ctx, &mediav1.PlayAudioRequest{
		AudioUri:      "file://audio/tr/system/technical_difficulty.wav",
		ServerRtpPort: rtpPort,
		RtpTargetAddr: targetAddr,
	})

	if err != nil {
		l.Error().Err(err).Str("event", "FAILSAFE_ERROR").Msg("🚨 Failsafe anonsu bile çalınamadı! Media Service ulaşılamaz durumda.")
	} else {
		// Anonsun okunması için sisteme süre tanıyoruz (Anons 6 saniye civarıdır)
		time.Sleep(6 * time.Second)
		l.Info().Str("event", "FAILSAFE_COMPLETED").Msg("✅ Arıza anonsu okundu. Çağrı artık güvenle sonlandırılabilir.")
	}
}

func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, mediaInfo *eventv1.MediaInfo) error {
	l := pm.log.With().Str("call_id", callID).Str("trace_id", sessionID).Logger()

	l.Info().Str("event", "PIPELINE_STARTING").Msg("🔄 AI Ses Boru Hattı (Full-Duplex) başlatılıyor...")

	// 1. Trace ID Propagation
	md := metadata.Pairs("x-trace-id", sessionID)
	outCtx := metadata.NewOutgoingContext(ctx, md)
	targetSampleRate := uint32(pm.config.PipelineSampleRate)

	// =========================================================================
	// [CRITICAL FIX]: AGGRESSIVE UPSTREAM CONNECTION RETRIES & FAILSAFE
	// =========================================================================

	var mediaRecStream mediav1.MediaService_RecordAudioClient
	var sttStream sttv1.SttGatewayService_TranscribeStreamClient
	var dialogStream dialogv1.DialogService_StreamConversationClient
	var err error

	// 2. Media Recording Stream Bağlantısı
	for attempt := 1; attempt <= 5; attempt++ {
		mediaRecStream, err = pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{
			ServerRtpPort:    mediaInfo.GetServerRtpPort(),
			TargetSampleRate: toPtrUint32(targetSampleRate),
		})
		if err == nil {
			break
		}
		l.Warn().Err(err).Int("attempt", attempt).Str("event", "UPSTREAM_CONNECT_RETRY").Str("target", "media-service").Msg("⏳ Media Service bağlantısı başarısız, tekrar deneniyor...")
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		l.Error().Err(err).Str("event", "UPSTREAM_CONNECT_FATAL").Msg("❌ Media Service'e ulaşılamadı. Failsafe çalınamaz, çağrı düşecek.")
		return fmt.Errorf("media_service_connect_fatal: %w", err)
	}

	// 3. STT Gateway Stream Bağlantısı
	for attempt := 1; attempt <= 5; attempt++ {
		sttStream, err = pm.clients.STT.TranscribeStream(outCtx)
		if err == nil {
			break
		}
		l.Warn().Err(err).Int("attempt", attempt).Str("event", "UPSTREAM_CONNECT_RETRY").Str("target", "stt-gateway").Msg("⏳ STT Gateway bağlantısı başarısız, tekrar deneniyor...")
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		l.Error().Err(err).Str("event", "UPSTREAM_CONNECT_FATAL").Msg("❌ STT Gateway'e ulaşılamadı. Failsafe tetikleniyor.")
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return fmt.Errorf("stt_gateway_connect_fatal: %w", err)
	}

	// 4. Dialog Service Stream Bağlantısı
	for attempt := 1; attempt <= 5; attempt++ {
		dialogStream, err = pm.clients.Dialog.StreamConversation(outCtx)
		if err == nil {
			break
		}
		l.Warn().Err(err).Int("attempt", attempt).Str("event", "UPSTREAM_CONNECT_RETRY").Str("target", "dialog-service").Msg("⏳ Dialog Service bağlantısı başarısız, tekrar deneniyor...")
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		l.Error().Err(err).Str("event", "UPSTREAM_CONNECT_FATAL").Msg("❌ Dialog Service'e ulaşılamadı. Failsafe tetikleniyor.")
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return fmt.Errorf("dialog_service_connect_fatal: %w", err)
	}

	l.Info().Str("event", "PIPELINE_UPSTREAMS_READY").Msg("✅ Tüm AI Motorlarına (STT, Dialog, TTS) başarıyla bağlanıldı.")

	// İlk mesajı Dialog servisine gönder (Config Handshake)
	if sendErr := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); sendErr != nil {
		l.Error().Err(sendErr).Str("event", "DIALOG_HANDSHAKE_ERROR").Msg("❌ Dialog servisine ilk yapılandırma gönderilemedi.")
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return fmt.Errorf("dialog_service_send_fatal: %w", sendErr)
	}

	var ttsCancelFunc context.CancelFunc
	var ttsMutex sync.Mutex
	wg := &sync.WaitGroup{}
	errChan := make(chan error, 5)

	// --- RX LOOP (Media -> STT) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			chunk, err := mediaRecStream.Recv()
			if err == io.EOF {
				l.Debug().Str("event", "MEDIA_RX_EOF").Msg("Media RX stream kapandı.")
				return
			}
			if err != nil {
				l.Error().Err(err).Str("event", "MEDIA_RX_ERROR").Msg("Media ses alımında hata.")
				errChan <- err
				return
			}
			if err := sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData}); err != nil {
				l.Warn().Err(err).Str("event", "STT_TX_ERROR").Msg("STT servisine ses paketi gönderilemedi.")
				return
			}
		}
	}()

	// --- STT PROCESS LOOP (STT -> Dialog & Barge-in) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			res, err := sttStream.Recv()
			if err == io.EOF {
				l.Debug().Str("event", "STT_RX_EOF").Msg("STT RX stream kapandı.")
				return
			}
			if err != nil {
				l.Error().Err(err).Str("event", "STT_RX_ERROR").Msg("STT'den metin alımında hata.")
				errChan <- err
				return
			}

			// [BARGE-IN LOGIC]
			if !res.IsFinal && len(res.PartialTranscription) > 3 {
				ttsMutex.Lock()
				if ttsCancelFunc != nil {
					l.Warn().Str("event", "BARGE_IN_DETECTED").Str("partial", res.PartialTranscription).Msg("🔇 Barge-in (Söz Kesme) algılandı. TTS susturuluyor.")
					ttsCancelFunc()
					ttsCancelFunc = nil
				}
				ttsMutex.Unlock()
			}

			if res.IsFinal {
				l.Info().Str("event", "USER_SPEECH_FINAL").Str("transcript", res.PartialTranscription).Msg("🗣️ Kullanıcı cümleyi tamamladı.")
				_ = dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_TextInput{TextInput: res.PartialTranscription},
				})
				_ = dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
				})
			}
		}
	}()

	// --- TX LOOP (Dialog -> TTS -> Media) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			dRes, err := dialogStream.Recv()
			if err == io.EOF {
				l.Debug().Str("event", "DIALOG_RX_EOF").Msg("Dialog RX stream kapandı.")
				return
			}
			if err != nil {
				l.Error().Err(err).Str("event", "DIALOG_RX_ERROR").Msg("Dialog'dan cevap alımında hata.")
				errChan <- err
				return
			}

			sentence := dRes.GetTextResponse()
			if sentence == "" {
				continue
			}

			l.Debug().Str("event", "AI_RESPONSE_GENERATED").Str("sentence", sentence).Msg("🧠 AI cevap üretti. Sese dönüştürülüyor...")

			ttsCtx, tCancel := context.WithCancel(outCtx)
			ttsMutex.Lock()
			ttsCancelFunc = tCancel
			ttsMutex.Unlock()

			if err := pm.mediator.SpeakText(ttsCtx, callID, sentence, "coqui:default", mediaInfo); err != nil {
				l.Debug().Err(err).Str("event", "TTS_MEDIA_INTERRUPT").Msg("TTS okunurken kesildi veya hata oluştu (Genelde Barge-in sebeplidir).")
			}
		}
	}()

	select {
	case <-ctx.Done():
		l.Info().Str("event", "PIPELINE_CONTEXT_DONE").Msg("🏁 Pipeline ana context'i tamamlandı.")
		return nil
	case err := <-errChan:
		l.Error().Err(err).Str("event", "PIPELINE_ERROR").Msg("🚨 Pipeline asenkron bir hata yüzünden çöktü.")
		// Olası bir akış çökmesinde de failsafe tetikleyelim.
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return err
	}
}

func toPtrUint32(v uint32) *uint32 { return &v }

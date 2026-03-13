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
		l.Error().Err(err).Str("event", "FAILSAFE_ERROR").Msg("🚨 Failsafe anonsu bile çalınamadı!")
	} else {
		// Anonsun okunması için sisteme süre tanıyoruz (Anons 7 saniye civarıdır)
		time.Sleep(8 * time.Second)
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
	// [UX & SRE FIX]: LATENCY MASKING (ÖRTÜŞEN KARŞILAMA)
	// AI motorları uyanırken müşteriyi sessizlikte bırakmamak için
	// arka planda hemen "Sizi asistanımıza bağlıyorum..." anonsunu başlatıyoruz.
	// =========================================================================
	go func() {
		l.Debug().Str("event", "PRE_GREETING_STARTED").Msg("🎵 Kullanıcıya ön-karşılama anonsu dinletiliyor (Latency Masking).")
		_, _ = pm.clients.Media.PlayAudio(outCtx, &mediav1.PlayAudioRequest{
			AudioUri:      "file://audio/tr/system/connecting.wav",
			ServerRtpPort: mediaInfo.GetServerRtpPort(),
			RtpTargetAddr: mediaInfo.GetCallerRtpAddr(),
		})
	}()

	var mediaRecStream mediav1.MediaService_RecordAudioClient
	var sttStream sttv1.SttGatewayService_TranscribeStreamClient
	var dialogStream dialogv1.DialogService_StreamConversationClient
	var err error

	// 2. Media Recording Stream
	for attempt := 1; attempt <= 5; attempt++ {
		mediaRecStream, err = pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{
			ServerRtpPort:    mediaInfo.GetServerRtpPort(),
			TargetSampleRate: toPtrUint32(targetSampleRate),
		})
		if err == nil {
			break
		}
		l.Warn().Err(err).Int("attempt", attempt).Str("event", "UPSTREAM_CONNECT_RETRY").Msg("Media Service retry...")
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return fmt.Errorf("media_service_connect_fatal: %w", err)
	}

	// 3. STT Gateway Stream
	for attempt := 1; attempt <= 5; attempt++ {
		sttStream, err = pm.clients.STT.TranscribeStream(outCtx)
		if err == nil {
			break
		}
		l.Warn().Err(err).Int("attempt", attempt).Str("event", "UPSTREAM_CONNECT_RETRY").Msg("STT Gateway retry...")
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return fmt.Errorf("stt_gateway_connect_fatal: %w", err)
	}

	// 4. Dialog Service Stream
	for attempt := 1; attempt <= 5; attempt++ {
		dialogStream, err = pm.clients.Dialog.StreamConversation(outCtx)
		if err == nil {
			break
		}
		l.Warn().Err(err).Int("attempt", attempt).Str("event", "UPSTREAM_CONNECT_RETRY").Msg("Dialog Service retry...")
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return fmt.Errorf("dialog_service_connect_fatal: %w", err)
	}

	l.Info().Str("event", "PIPELINE_UPSTREAMS_READY").Msg("✅ Tüm AI Motorlarına (STT, Dialog, TTS) başarıyla bağlanıldı.")

	if sendErr := dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	}); sendErr != nil {
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
				return
			}
			if err != nil {
				errChan <- err
				return
			}
			_ = sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData})
		}
	}()

	// --- STT PROCESS LOOP (STT -> Dialog & Barge-in) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			res, err := sttStream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				errChan <- err
				return
			}

			if !res.IsFinal && len(res.PartialTranscription) > 3 {
				ttsMutex.Lock()
				if ttsCancelFunc != nil {
					l.Warn().Str("event", "BARGE_IN_DETECTED").Msg("🔇 Söz Kesme algılandı. TTS susturuluyor.")
					ttsCancelFunc()
					ttsCancelFunc = nil
				}
				ttsMutex.Unlock()
			}

			if res.IsFinal {
				l.Info().Str("event", "USER_SPEECH_FINAL").Str("transcript", res.PartialTranscription).Msg("🗣️ Kullanıcı konuştu.")
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
				return
			}
			if err != nil {
				errChan <- err
				return
			}

			sentence := dRes.GetTextResponse()
			if sentence == "" {
				continue
			}

			ttsCtx, tCancel := context.WithCancel(outCtx)
			ttsMutex.Lock()
			ttsCancelFunc = tCancel
			ttsMutex.Unlock()

			_ = pm.mediator.SpeakText(ttsCtx, callID, sentence, "coqui:default", mediaInfo)
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errChan:
		l.Error().Err(err).Str("event", "PIPELINE_ERROR").Msg("🚨 Pipeline asenkron hatası.")
		pm.triggerFailsafe(outCtx, callID, mediaInfo.GetServerRtpPort(), mediaInfo.GetCallerRtpAddr(), l)
		return err
	}

}

func toPtrUint32(v uint32) *uint32 { return &v }

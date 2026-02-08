// sentiric-telephony-action-service/internal/service/pipeline.go
package service

import (
	"context"
	"io"
	"sync"

	"github.com/rs/zerolog"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	eventv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/event/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config" // Config import edildi
	"google.golang.org/grpc/metadata"
)

type PipelineManager struct {
	clients  *client.Clients
	config   *config.Config // Config struct'a eklendi
	log      zerolog.Logger
	mediator *Mediator
}

// NewPipelineManager: Config parametresi eklendi ve Mediator'a geçirildi.
func NewPipelineManager(clients *client.Clients, cfg *config.Config, log zerolog.Logger) *PipelineManager {
	return &PipelineManager{
		clients:  clients,
		config:   cfg,
		log:      log,
		mediator: NewMediator(clients, cfg, log), // Config Mediator'a iletiliyor
	}
}

func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, mediaInfo *eventv1.MediaInfo) error {
	l := pm.log.With().Str("call_id", callID).Str("trace_id", sessionID).Logger()

	// 1. Trace ID Propagation
	md := metadata.Pairs("x-trace-id", sessionID)
	outCtx := metadata.NewOutgoingContext(ctx, md)

	// 2. [NAT TRAVERSAL]: Hole Punching tetikle
	pm.mediator.TriggerHolePunching(outCtx, mediaInfo)

	// [MİMARİ DÜZELTME]: Hardcoded 16000 yerine Config kullanılıyor.
	// STT motoru için ideal örnekleme hızı buradan belirlenir.
	targetSampleRate := uint32(pm.config.PipelineSampleRate)

	// 3. Media Recording (Inbound) Stream
	l.Debug().Uint32("target_sr", targetSampleRate).Msg("Media Service inbound stream başlatılıyor...")
	mediaRecStream, err := pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{
		ServerRtpPort:    mediaInfo.GetServerRtpPort(),
		TargetSampleRate: toPtrUint32(targetSampleRate), // ARTIK HARDCODED DEĞİL
	})
	if err != nil {
		return err
	}

	// 4. STT Gateway Stream
	sttStream, err := pm.clients.STT.TranscribeStream(outCtx)
	if err != nil {
		return err
	}

	// 5. Dialog Service Stream
	dialogStream, err := pm.clients.Dialog.StreamConversation(outCtx)
	if err != nil {
		return err
	}

	// Init Dialog
	_ = dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	})

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

			if err := sttStream.Send(&sttv1.TranscribeStreamRequest{AudioChunk: chunk.AudioData}); err != nil {
				l.Warn().Err(err).Msg("STT stream send error.")
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
				return
			}
			if err != nil {
				errChan <- err
				return
			}

			// [BARGE-IN LOGIC]: Kullanıcı konuşmaya başladıysa mevcut TTS'i kes.
			if !res.IsFinal && len(res.PartialTranscription) > 3 {
				ttsMutex.Lock()
				if ttsCancelFunc != nil {
					l.Warn().Str("partial", res.PartialTranscription).Msg("🔇 Barge-in detected. Cancelling TTS.")
					ttsCancelFunc() // TTS context'ini iptal et
					ttsCancelFunc = nil
				}
				ttsMutex.Unlock()
			}

			if res.IsFinal {
				l.Info().Str("transcript", res.PartialTranscription).Msg("🗣️ User final utterance.")
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

			// Her cümle için yeni bir iptal edilebilir context
			ttsCtx, tCancel := context.WithCancel(outCtx)
			ttsMutex.Lock()
			ttsCancelFunc = tCancel
			ttsMutex.Unlock()

			// Mediator üzerinden seslendir
			// Config'den gelen sample rate burada otomatik kullanılıyor (Mediator içinde)
			if err := pm.mediator.SpeakText(ttsCtx, callID, sentence, "coqui:default", mediaInfo); err != nil {
				l.Debug().Err(err).Msg("TTS/Media playback interrupted or failed.")
			}
		}
	}()

	// Cleanup
	select {
	case <-ctx.Done():
		return nil
	case err := <-errChan:
		return err
	}
}

func toPtrUint32(v uint32) *uint32 { return &v }

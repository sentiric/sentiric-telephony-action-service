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
	"google.golang.org/grpc/metadata"
)

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

func (pm *PipelineManager) RunPipeline(ctx context.Context, callID, sessionID, userID string, mediaInfo *eventv1.MediaInfo) error {
	l := pm.log.With().Str("call_id", callID).Str("trace_id", sessionID).Logger()

	md := metadata.Pairs("x-trace-id", sessionID)
	outCtx := metadata.NewOutgoingContext(ctx, md)

	pm.mediator.TriggerHolePunching(outCtx, mediaInfo)

	mediaRecStream, err := pm.clients.Media.RecordAudio(outCtx, &mediav1.RecordAudioRequest{ServerRtpPort: mediaInfo.GetServerRtpPort()})
	if err != nil {
		return err
	}

	sttStream, err := pm.clients.STT.TranscribeStream(outCtx)
	if err != nil {
		return err
	}

	dialogStream, err := pm.clients.Dialog.StreamConversation(outCtx)
	if err != nil {
		return err
	}

	_ = dialogStream.Send(&dialogv1.StreamConversationRequest{
		Payload: &dialogv1.StreamConversationRequest_Config{
			Config: &dialogv1.ConversationConfig{SessionId: sessionID, UserId: userID},
		},
	})

	var ttsCancelFunc context.CancelFunc
	var ttsMutex sync.Mutex
	wg := &sync.WaitGroup{}
	errChan := make(chan error, 5)

	// RX Loop
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

	// STT Process Loop
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
					l.Warn().Msg("🔇 Barge-in detected.")
					ttsCancelFunc()
					ttsCancelFunc = nil
				}
				ttsMutex.Unlock()
			}

			if res.IsFinal {
				_ = dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_TextInput{TextInput: res.PartialTranscription},
				})
				_ = dialogStream.Send(&dialogv1.StreamConversationRequest{
					Payload: &dialogv1.StreamConversationRequest_IsFinalInput{IsFinalInput: true},
				})
			}
		}
	}()

	// TX Loop
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

			if err := pm.mediator.SpeakText(ttsCtx, callID, sentence, "coqui:default", mediaInfo); err != nil {
				l.Debug().Err(err).Msg("TTS stream inactive.")
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errChan:
		return err
	}
}

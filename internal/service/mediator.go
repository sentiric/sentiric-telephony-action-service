// sentiric-telephony-action-service/internal/service/mediator.go
package service

import (
	"context"
	"fmt"
	"io"

	"github.com/rs/zerolog"
	eventv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/event/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
)

type Mediator struct {
	Clients *client.Clients
	log     zerolog.Logger
}

func NewMediator(clients *client.Clients, log zerolog.Logger) *Mediator {
	return &Mediator{Clients: clients, log: log}
}

// SpeakText: gRPC ve Pipeline tarafından ortak kullanılan seslendirme motoru.
// Bu fonksiyon context iptali ile "Barge-in" (Söz Kesme) desteği sunar.
func (m *Mediator) SpeakText(ctx context.Context, callID, text, voiceID string, mediaInfo *eventv1.MediaInfo) error {
	l := m.log.With().Str("call_id", callID).Logger()

	// 1. TTS İstek Hazırlığı
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text:    text,
		VoiceId: voiceID,
		AudioConfig: &ttsv1.AudioConfig{
			SampleRateHertz: 16000,
			AudioFormat:     ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE,
		},
	}

	// 2. TTS Stream'i Başlat
	ttsStream, err := m.Clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil {
		l.Error().Err(err).Msg("❌ TTS Stream başlatılamadı.")
		return err
	}

	// 3. Media Service Stream'i Başlat
	mediaStream, err := m.Clients.Media.StreamAudioToCall(ctx)
	if err != nil {
		return fmt.Errorf("media outbound stream hatası: %w", err)
	}

	// Handshake: CallID gönder
	if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return fmt.Errorf("media handshake failed: %w", err)
	}

	// 4. TX Loop: TTS -> Media
	for {
		select {
		case <-ctx.Done():
			l.Warn().Msg("🛑 TTS stream cancelled.")
			return ctx.Err()
		default:
			chunk, err := ttsStream.Recv()
			if err == io.EOF {
				goto Finish
			}
			if err != nil {
				return fmt.Errorf("tts recv error: %w", err)
			}

			if len(chunk.AudioContent) > 0 {
				if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent}); err != nil {
					return fmt.Errorf("media send error: %w", err)
				}
			}
		}
	}

Finish:
	_ = mediaStream.CloseSend()
	_, _ = mediaStream.Recv() // Final ACK
	return nil
}

func (m *Mediator) TriggerHolePunching(ctx context.Context, mediaInfo *eventv1.MediaInfo) {
	if mediaInfo.GetCallerRtpAddr() == "" {
		return
	}
	req := &mediav1.PlayAudioRequest{
		AudioUri:      "file://audio/tr/system/nat_warmer.wav",
		ServerRtpPort: mediaInfo.GetServerRtpPort(),
		RtpTargetAddr: mediaInfo.GetCallerRtpAddr(),
	}
	_, _ = m.Clients.Media.PlayAudio(ctx, req)
}

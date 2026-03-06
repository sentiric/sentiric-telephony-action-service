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
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
)

type Mediator struct {
	Clients *client.Clients
	Config  *config.Config
	log     zerolog.Logger
}

func NewMediator(clients *client.Clients, cfg *config.Config, log zerolog.Logger) *Mediator {
	return &Mediator{
		Clients: clients,
		Config:  cfg,
		log:     log,
	}
}

// SpeakText: Metni sese çevirir ve medya servisine stream eder.
func (m *Mediator) SpeakText(ctx context.Context, callID, text, voiceID string, mediaInfo *eventv1.MediaInfo) error {
	l := m.log.With().Str("call_id", callID).Logger()

	targetSampleRate := m.Config.PipelineSampleRate

	// 1. TTS İstek Hazırlığı
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text:    text,
		VoiceId: voiceID,
		AudioConfig: &ttsv1.AudioConfig{
			SampleRateHertz: targetSampleRate,
			AudioFormat:     ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE,
			VolumeGainDb:    0.0,
		},
		Tuning: &ttsv1.TuningParams{
			Speed:             1.0,
			Temperature:       0.75,
			TopP:              0.85,
			TopK:              50,
			RepetitionPenalty: 2.0,
		},
	}

	l.Debug().
		Int32("sample_rate", targetSampleRate).
		Str("voice", voiceID).
		Msg("🎙️ TTS Stream başlatılıyor...")

	// 2. TTS Stream'i Başlat
	ttsStream, err := m.Clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil {
		l.Error().Err(err).Msg("❌ TTS Gateway bağlantı hatası.")
		return err
	}

	// 3. Media Service Stream'i Başlat
	mediaStream, err := m.Clients.Media.StreamAudioToCall(ctx)
	if err != nil {
		return fmt.Errorf("media outbound stream hatası: %w", err)
	}

	// Handshake
	if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return fmt.Errorf("media handshake failed: %w", err)
	}

	// 4. TX Loop: TTS -> Media
	chunkCount := 0
	for {
		select {
		case <-ctx.Done():
			l.Warn().Msg("🛑 TTS stream context tarafından iptal edildi (Barge-in).")
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
				chunkCount++
			}
		}
	}

Finish:
	l.Debug().Int("chunks_sent", chunkCount).Msg("✅ TTS Stream başarıyla tamamlandı.")
	_ = mediaStream.CloseSend()
	_, _ = mediaStream.Recv()
	return nil
}

// NOT: TriggerHolePunching metodu bilinçli olarak silinmiştir, artık Media Service otonom yönetir.

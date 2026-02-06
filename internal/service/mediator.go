// sentiric-telephony-action-service/internal/service/mediator.go
package service

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"
	eventv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/event/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
)

// Mediator, Media ve TTS gibi dış servisleri orkestre eden yardımcı fonksiyonları içerir.
type Mediator struct {
	Clients *client.Clients // KRİTİK DÜZELTME: Clients yapıldı
	log     zerolog.Logger
}

func NewMediator(clients *client.Clients, log zerolog.Logger) *Mediator {
	return &Mediator{Clients: clients, log: log}
}

// SpeakText: Metni sese çevirir ve medya servisine stream eder (Bloklayıcı).
// mediaInfo parametresi eventv1.MediaInfo tipinde olmalıdır.
func (m *Mediator) SpeakText(ctx context.Context, callID, text, voiceID string, mediaInfo *eventv1.MediaInfo) error {
	l := m.log.With().Str("call_id", callID).Logger()
	l.Debug().Str("text", text).Msg("📢 SpeakText: TTS Stream Başlatılıyor")

	// 1. Media Bağlantısı (Outbound Audio Stream)
	mediaStream, err := m.Clients.Media.StreamAudioToCall(ctx)
	if err != nil {
		return fmt.Errorf("media stream açılamadı: %w", err)
	}
	if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{CallId: callID}); err != nil {
		return fmt.Errorf("media stream el sıkışma hatası: %w", err)
	}

	// 2. TTS İsteği (Stream)
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text:        text,
		VoiceId:     voiceID,
		AudioConfig: &ttsv1.AudioConfig{SampleRateHertz: 16000, AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE},
	}
	ttsStream, err := m.Clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil {
		l.Error().Err(err).Msg("❌ TTS Stream Başarısız, Fallback Deneniyor.")
		return m.handleTTSFallback(mediaStream, callID)
	}

	// 3. Loop: TTS -> Media
	for {
		chunk, err := ttsStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			l.Error().Err(err).Msg("❌ TTS stream kesintisi.")
			break
		}

		if len(chunk.AudioContent) > 0 {
			if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent}); err != nil {
				return fmt.Errorf("media stream gönderme hatası: %w", err)
			}
		}
	}

	if err := mediaStream.CloseSend(); err != nil {
		l.Warn().Err(err).Msg("Media stream kapatma uyarısı")
	}

	if _, err := mediaStream.Recv(); err != nil && err != io.EOF {
		l.Warn().Err(err).Msg("Media stream final yanıtı alınırken hata oluştu")
	}

	l.Debug().Msg("✅ SpeakText tamamlandı.")
	return nil
}

// handleTTSFallback: TTS başarısız olursa önceden kaydedilmiş anonsu çalar.
func (m *Mediator) handleTTSFallback(mediaStream mediav1.MediaService_StreamAudioToCallClient, callID string) error {
	fallbackPath := "/sentiric-assets/audio/tr/system/technical_difficulty.wav"
	l := m.log.With().Str("call_id", callID).Logger()

	file, fErr := os.Open(fallbackPath)
	if fErr != nil {
		return fmt.Errorf("TTS başarısız ve fallback dosyası açılamadı: %w", fErr)
	}
	defer file.Close()

	buf := make([]byte, 1024)
	for {
		n, rErr := file.Read(buf)
		if n > 0 {
			if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: buf[:n]}); err != nil {
				return fmt.Errorf("fallback audio gönderme hatası: %w", err)
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return fmt.Errorf("fallback dosya okuma hatası: %w", rErr)
		}
	}

	l.Info().Msg("✅ Fallback audio çalındı.")
	return mediaStream.CloseSend()
}

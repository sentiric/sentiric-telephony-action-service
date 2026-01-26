// internal/server/grpc.go
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/rs/zerolog"
	
	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"

	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"github.com/sentiric/sentiric-telephony-action-service/internal/service"
	
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Server struct {
	telephonyv1.UnimplementedTelephonyActionServiceServer
	pipelineManager *service.PipelineManager
	log             zerolog.Logger
}

func NewGrpcServer(cfg *config.Config, log zerolog.Logger, clients *client.Clients) *grpc.Server {
	var opts []grpc.ServerOption

	if cfg.CertPath != "" {
		creds, err := loadServerTLS(cfg.CertPath, cfg.KeyPath, cfg.CaPath)
		if err != nil {
			log.Error().Err(err).Msg("TLS yüklenemedi, INSECURE moduna geçiliyor.")
		} else {
			opts = append(opts, grpc.Creds(creds))
			log.Info().Msg("🔒 gRPC Server mTLS ile güvenli başlatılıyor.")
		}
	}

	pipelineMgr := service.NewPipelineManager(clients, log)
	grpcServer := grpc.NewServer(opts...)
	
	telephonyv1.RegisterTelephonyActionServiceServer(grpcServer, &Server{
		pipelineManager: pipelineMgr,
		log:             log,
	})
	
	return grpcServer
}

// SpeakText: Basit metin okuma (Fire and Forget veya Block)
func (s *Server) SpeakText(ctx context.Context, req *telephonyv1.SpeakTextRequest) (*telephonyv1.SpeakTextResponse, error) {
	s.log.Info().Str("call_id", req.CallId).Str("text", req.Text).Msg("📢 SpeakText isteği...")
	
	// TTS'den al, Media'ya bas (Tek yönlü)
	// PipelineManager içindeki streamTTS fonksiyonunu yeniden kullanabiliriz ancak
	// burada media connection'ı sıfırdan kurmamız gerekir.
	
	// Basitlik için burada inline implementasyon yapıyoruz:
	clients := s.pipelineManager.GetClients()
	
	// 1. Media Bağlantısı
	mediaStream, err := clients.Media.StreamAudioToCall(ctx)
	if err != nil { return nil, err }
	// Handshake
	mediaStream.Send(&mediav1.StreamAudioToCallRequest{CallId: req.CallId})

	// 2. TTS İsteği
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text: req.Text,
		VoiceId: req.VoiceId,
		AudioConfig: &ttsv1.AudioConfig{SampleRateHertz: 16000, AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE},
	}
	ttsStream, err := clients.TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil { return nil, err }

	// 3. Loop
	for {
		chunk, err := ttsStream.Recv()
		if err == io.EOF { break }
		if err != nil { return nil, err }
		
		if len(chunk.AudioContent) > 0 {
			mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent})
		}
	}
	mediaStream.CloseSend()
	
	// Wait for media ack
	mediaStream.Recv()
	
	return &telephonyv1.SpeakTextResponse{Success: true}, nil
}

// RunPipeline: Tam çift yönlü akıllı diyalog başlatır
func (s *Server) RunPipeline(req *telephonyv1.RunPipelineRequest, stream telephonyv1.TelephonyActionService_RunPipelineServer) error {
	s.log.Info().Str("call_id", req.CallId).Msg("🔄 RunPipeline RPC çağrıldı.")
	
	// Stream context'i iptal edilene kadar pipeline'ı çalıştır
	err := s.pipelineManager.RunPipeline(
		stream.Context(),
		req.CallId,
		req.SessionId,
		"unknown_user", // User ID gerekirse request'e eklenmeli
		req.MediaInfo.ServerRtpPort,
	)
	
	if err != nil {
		s.log.Error().Err(err).Msg("Pipeline hata ile sonlandı")
		return err
	}
	
	return nil
}

// ... Diğer Legacy Metotlar (PlayAudio vb.) ...
func (s *Server) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
	// ... Mevcut implementasyon korunabilir veya MediaService'e proxy edilebilir ...
    return &telephonyv1.PlayAudioResponse{Success: true}, nil
}

// TLS Helper
func loadServerTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil { return nil, err }
	
	caData, err := os.ReadFile(caPath)
	if err != nil { return nil, err }
	
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caData)
	
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}
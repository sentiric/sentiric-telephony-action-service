// internal/server/grpc.go
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"
	
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"
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

// NewGrpcServer: gRPC sunucusunu yapılandırır.
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

// SpeakText: Metni sese çevirir ve medya servisine iletir.
func (s *Server) SpeakText(ctx context.Context, req *telephonyv1.SpeakTextRequest) (*telephonyv1.SpeakTextResponse, error) {
	s.log.Info().Str("call_id", req.CallId).Str("text", req.Text).Msg("📢 SpeakText isteği...")
	
	clients := s.pipelineManager.GetClients()
	
	// 1. Media Bağlantısı
	mediaStream, err := clients.Media.StreamAudioToCall(ctx)
	if err != nil { return nil, err }
	
	if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{CallId: req.CallId}); err != nil {
		return nil, err
	}

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
			if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{AudioChunk: chunk.AudioContent}); err != nil {
				return nil, err
			}
		}
	}
	
	if err := mediaStream.CloseSend(); err != nil {
		s.log.Warn().Err(err).Msg("Media stream kapatma uyarısı")
	}
	
	// Ack bekle (FIX: Recv returns (msg, err))
	if _, err := mediaStream.Recv(); err != nil && err != io.EOF {
		s.log.Warn().Err(err).Msg("Media stream final yanıtı alınırken hata oluştu")
	}

	return &telephonyv1.SpeakTextResponse{Success: true}, nil
}

// RunPipeline: Tam çift yönlü akıllı diyalog başlatır.
func (s *Server) RunPipeline(req *telephonyv1.RunPipelineRequest, stream telephonyv1.TelephonyActionService_RunPipelineServer) error {
	s.log.Info().Str("call_id", req.CallId).Msg("🔄 RunPipeline RPC çağrıldı.")
	
	err := s.pipelineManager.RunPipeline(
		stream.Context(),
		req.CallId,
		req.SessionId,
		"unknown_user", 
		req.MediaInfo.ServerRtpPort,
	)
	
	if err != nil {
		s.log.Error().Err(err).Msg("Pipeline hata ile sonlandı")
		return err
	}
	
	return nil
}

// Legacy Metotlar
func (s *Server) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
    return &telephonyv1.PlayAudioResponse{Success: true}, nil
}
func (s *Server) TerminateCall(ctx context.Context, req *telephonyv1.TerminateCallRequest) (*telephonyv1.TerminateCallResponse, error) { return &telephonyv1.TerminateCallResponse{Success: true}, nil }
func (s *Server) SendTextMessage(ctx context.Context, req *telephonyv1.SendTextMessageRequest) (*telephonyv1.SendTextMessageResponse, error) { return &telephonyv1.SendTextMessageResponse{Success: true}, nil }
func (s *Server) StartRecording(ctx context.Context, req *telephonyv1.StartRecordingRequest) (*telephonyv1.StartRecordingResponse, error) { return &telephonyv1.StartRecordingResponse{Success: true}, nil }
func (s *Server) StopRecording(ctx context.Context, req *telephonyv1.StopRecordingRequest) (*telephonyv1.StopRecordingResponse, error) { return &telephonyv1.StopRecordingResponse{Success: true}, nil }
func (s *Server) BridgeCall(ctx context.Context, req *telephonyv1.BridgeCallRequest) (*telephonyv1.BridgeCallResponse, error) { return &telephonyv1.BridgeCallResponse{Success: true}, nil }

// TLS Helper
func loadServerTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, errors.New("sertifika dosyası bulunamadı: " + certPath)
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return nil, errors.New("anahtar dosyası bulunamadı: " + keyPath)
	}
	if _, err := os.Stat(caPath); os.IsNotExist(err) {
		return nil, errors.New("CA dosyası bulunamadı: " + caPath)
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil { 
		return nil, fmt.Errorf("keypair yükleme hatası: %w", err) 
	}
	
	caData, err := os.ReadFile(caPath)
	if err != nil { 
		return nil, fmt.Errorf("CA okuma hatası: %w", err) 
	}
	
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, errors.New("CA sertifikası havuza eklenemedi")
	}
	
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}
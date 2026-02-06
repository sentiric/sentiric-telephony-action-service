// internal/server/grpc.go
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"net"

	"github.com/rs/zerolog"

	eventv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/event/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"

	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"github.com/sentiric/sentiric-telephony-action-service/internal/service"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// KRİTİK DÜZELTME: Server struct tanımı eklendi (Hata 4, 5)
type Server struct {
	telephonyv1.UnimplementedTelephonyActionServiceServer
	pipelineManager *service.PipelineManager
	mediator        *service.Mediator
	log             zerolog.Logger
}

// KRİTİK DÜZELTME: NewGrpcServer tanımı eklendi (Hata 1)
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
	mediator := service.NewMediator(clients, log)
	grpcServer := grpc.NewServer(opts...)

	telephonyv1.RegisterTelephonyActionServiceServer(grpcServer, &Server{
		pipelineManager: pipelineMgr,
		mediator:        mediator,
		log:             log,
	})

	return grpcServer
}

// KRİTİK DÜZELTME: Start fonksiyonu eklendi (Hata 2)
func Start(grpcServer *grpc.Server, port string) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	return grpcServer.Serve(lis)
}

// KRİTİK DÜZELTME: Stop fonksiyonu eklendi (Hata 3)
func Stop(grpcServer *grpc.Server) {
	grpcServer.GracefulStop()
}

// --- RPC IMPLEMENTATIONS ---

func (s *Server) SpeakText(ctx context.Context, req *telephonyv1.SpeakTextRequest) (*telephonyv1.SpeakTextResponse, error) {
	s.log.Info().Str("call_id", req.CallId).Msg("📢 SpeakText isteği alındı.")

	mediaInfo := req.GetMediaInfo()
	if mediaInfo == nil {
		return nil, errors.New("media_info alanı boş olamaz")
	}

	holePunchReq := &mediav1.PlayAudioRequest{
		AudioUri:      "file://audio/tr/system/nat_warmer.wav",
		ServerRtpPort: mediaInfo.GetServerRtpPort(),
		RtpTargetAddr: mediaInfo.GetCallerRtpAddr(),
	}

	_, err := s.mediator.Clients.Media.PlayAudio(ctx, holePunchReq)
	if err != nil {
		s.log.Warn().Err(err).Msg("Hole Punching komutu gönderilemedi, ses gelmeyebilir.")
	}

	eventMediaInfo := &eventv1.MediaInfo{
		CallerRtpAddr: mediaInfo.GetCallerRtpAddr(),
		ServerRtpPort: mediaInfo.GetServerRtpPort(),
	}

	err = s.mediator.SpeakText(
		ctx,
		req.CallId,
		req.Text,
		req.VoiceId,
		eventMediaInfo,
	)

	if err != nil {
		s.log.Error().Err(err).Msg("SpeakText işlemi başarısız oldu.")
		return &telephonyv1.SpeakTextResponse{Success: false, Message: err.Error()}, err
	}

	return &telephonyv1.SpeakTextResponse{Success: true}, nil
}

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

func (s *Server) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
	return &telephonyv1.PlayAudioResponse{Success: true}, nil
}
func (s *Server) TerminateCall(ctx context.Context, req *telephonyv1.TerminateCallRequest) (*telephonyv1.TerminateCallResponse, error) {
	return &telephonyv1.TerminateCallResponse{Success: true}, nil
}
func (s *Server) SendTextMessage(ctx context.Context, req *telephonyv1.SendTextMessageRequest) (*telephonyv1.SendTextMessageResponse, error) {
	return &telephonyv1.SendTextMessageResponse{Success: true}, nil
}
func (s *Server) StartRecording(ctx context.Context, req *telephonyv1.StartRecordingRequest) (*telephonyv1.StartRecordingResponse, error) {
	return &telephonyv1.StartRecordingResponse{Success: true}, nil
}
func (s *Server) StopRecording(ctx context.Context, req *telephonyv1.StopRecordingRequest) (*telephonyv1.StopRecordingResponse, error) {
	return &telephonyv1.StopRecordingResponse{Success: true}, nil
}
func (s *Server) BridgeCall(ctx context.Context, req *telephonyv1.BridgeCallRequest) (*telephonyv1.BridgeCallResponse, error) {
	return &telephonyv1.BridgeCallResponse{Success: true}, nil
}

// TLS Helper
func loadServerTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("keypair yükleme hatası: %w", err)
	}

	caData, err := ioutil.ReadFile(caPath)
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

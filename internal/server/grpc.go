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
	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"

	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"github.com/sentiric/sentiric-telephony-action-service/internal/service"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Server struct {
	telephonyv1.UnimplementedTelephonyActionServiceServer
	pipelineManager *service.PipelineManager
	mediator        *service.Mediator
	log             zerolog.Logger
}

func NewGrpcServer(cfg *config.Config, log zerolog.Logger, clients *client.Clients) *grpc.Server {
	var opts []grpc.ServerOption

	if cfg.CertPath != "" {
		creds, err := loadServerTLS(cfg.CertPath, cfg.KeyPath, cfg.CaPath)
		if err == nil {
			opts = append(opts, grpc.Creds(creds))
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

func Start(grpcServer *grpc.Server, port string) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return err
	}
	return grpcServer.Serve(lis)
}

func Stop(grpcServer *grpc.Server) {
	grpcServer.GracefulStop()
}

// SpeakText: Unary RPC.
func (s *Server) SpeakText(ctx context.Context, req *telephonyv1.SpeakTextRequest) (*telephonyv1.SpeakTextResponse, error) {
	s.log.Info().Str("call_id", req.CallId).Msg("📢 SpeakText RPC")

	if req.MediaInfo == nil {
		return nil, errors.New("media_info required")
	}

	// Tip dönüşümü (telephonyv1.MediaInfo -> eventv1.MediaInfo)
	internalMedia := &eventv1.MediaInfo{
		CallerRtpAddr: req.MediaInfo.CallerRtpAddr,
		ServerRtpPort: req.MediaInfo.ServerRtpPort,
	}

	err := s.mediator.SpeakText(ctx, req.CallId, req.Text, req.VoiceId, internalMedia)
	if err != nil {
		return &telephonyv1.SpeakTextResponse{Success: false}, err
	}

	return &telephonyv1.SpeakTextResponse{Success: true}, nil
}

// RunPipeline: Streaming RPC.
func (s *Server) RunPipeline(req *telephonyv1.RunPipelineRequest, stream telephonyv1.TelephonyActionService_RunPipelineServer) error {
	s.log.Info().Str("call_id", req.CallId).Msg("🔄 RunPipeline RPC")

	if req.MediaInfo == nil {
		return errors.New("media_info required in request")
	}

	// Tip dönüşümü
	internalMedia := &eventv1.MediaInfo{
		CallerRtpAddr: req.MediaInfo.CallerRtpAddr,
		ServerRtpPort: req.MediaInfo.ServerRtpPort,
	}

	return s.pipelineManager.RunPipeline(
		stream.Context(),
		req.CallId,
		req.SessionId,
		"unknown_user",
		internalMedia, // DÜZELTME: Artık *eventv1.MediaInfo tipinde geçiliyor
	)
}

// Legacy / Not implemented stubs
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

func loadServerTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	caData, err := ioutil.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caData)
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}

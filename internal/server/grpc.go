package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	"github.com/rs/zerolog"
	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Server struct {
	telephonyv1.UnimplementedTelephonyActionServiceServer
	pipelineManager *service.PipelineManager
	log             zerolog.Logger
}

func NewGrpcServer(certPath, keyPath, caPath string, log zerolog.Logger, clients *client.Clients) *grpc.Server {
	var opts []grpc.ServerOption

	if certPath != "" && keyPath != "" && caPath != "" {
		if _, err := os.Stat(certPath); err == nil {
			creds, err := loadServerTLS(certPath, keyPath, caPath)
			if err != nil {
				log.Error().Err(err).Msg("TLS yüklenemedi, INSECURE başlatılıyor.")
			} else {
				opts = append(opts, grpc.Creds(creds))
				log.Info().Msg("🔒 gRPC Server mTLS ile başlatılıyor.")
			}
		} else {
			log.Warn().Msg("Sertifika dosyaları bulunamadı, INSECURE başlatılıyor.")
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

// RunPipeline: Server Streaming RPC Implementasyonu
// DÜZELTME: İmzaya dikkat edin. 'req' parametresi eklendi, 'stream' ikinci parametre oldu.
func (s *Server) RunPipeline(req *telephonyv1.RunPipelineRequest, stream telephonyv1.TelephonyActionService_RunPipelineServer) error {
	// ARTIK stream.Recv() ÇAĞIRMIYORUZ. İstek 'req' içinde geldi.
	
	callID := req.CallId
	sessionID := req.SessionId
	
	var rtpPort uint32 = 0
	if req.MediaInfo != nil {
		rtpPort = req.MediaInfo.ServerRtpPort
	} else {
		rtpPort = 10000 // Test varsayılanı
	}

	s.log.Info().Str("call_id", callID).Uint32("rtp_port", rtpPort).Msg("Pipeline başlatılıyor...")

	// İstemciye "Başladım" bilgisini gönder
	stream.Send(&telephonyv1.RunPipelineResponse{
		State: telephonyv1.RunPipelineResponse_STATE_RUNNING,
		Message: "Pipeline initialized.",
	})

	// Pipeline'ı Çalıştır
	err := s.pipelineManager.RunPipeline(stream.Context(), callID, sessionID, "user-1", rtpPort)
	
	if err != nil {
		s.log.Error().Err(err).Msg("Pipeline hatayla sonlandı")
		stream.Send(&telephonyv1.RunPipelineResponse{
			State: telephonyv1.RunPipelineResponse_STATE_ERROR,
			Message: err.Error(),
		})
		return err
	}

	s.log.Info().Str("call_id", callID).Msg("Pipeline başarıyla tamamlandı.")
	stream.Send(&telephonyv1.RunPipelineResponse{
		State: telephonyv1.RunPipelineResponse_STATE_STOPPED,
		Message: "Pipeline finished.",
	})
	
	return nil
}

// Legacy metodlar
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

func Start(grpcServer *grpc.Server, port string) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil { return err }
	return grpcServer.Serve(lis)
}

func Stop(grpcServer *grpc.Server) {
	grpcServer.GracefulStop()
}

func loadServerTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	// X509KeyPair kullanıyoruz (PEM -> DER otomatik dönüşümü için)
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("sunucu sertifikası yüklenemedi: %w", err)
	}

	caCert, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("CA sertifikası okunamadı: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("CA sertifikası havuza eklenemedi")
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}

	return credentials.NewTLS(tlsConfig), nil
}
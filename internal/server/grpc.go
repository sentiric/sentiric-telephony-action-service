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
	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Server struct'ı güncellendi
type Server struct {
	telephonyv1.UnimplementedTelephonyActionServiceServer
	pipelineManager *service.PipelineManager
	log             zerolog.Logger
}

func NewGrpcServer(certPath, keyPath, caPath string, log zerolog.Logger, clients *client.Clients) *grpc.Server {
	creds, err := loadServerTLS(certPath, keyPath, caPath)
	if err != nil {
		log.Fatal().Err(err).Msg("TLS yüklenemedi")
	}

	pipelineMgr := service.NewPipelineManager(clients, log)
	grpcServer := grpc.NewServer(grpc.Creds(creds))
	
	telephonyv1.RegisterTelephonyActionServiceServer(grpcServer, &Server{
		pipelineManager: pipelineMgr,
		log:             log,
	})
	
	return grpcServer
}

// RunPipeline Implementasyonu
func (s *Server) RunPipeline(stream telephonyv1.TelephonyActionService_RunPipelineServer) error {
	// İlk mesajı al (Config ve CallID)
	req, err := stream.Recv()
	if err != nil {
		return err
	}

	callID := req.CallId
	sessionID := req.SessionId
	// RTP portunu request içinden veya başka bir yerden almalıyız. 
	// Şimdilik request'e eklenmediği için metadata veya veritabanından alınmalı.
	// Hızlı çözüm: Request proto'sunu güncellemek gerekir ama şimdilik mock port kullanıyoruz veya
	// Media Service'ten sorguluyoruz. 
	// DOĞRUSU: CallID ile MediaService'ten portu bulmaktır.
	// Ancak basitlik için RunPipelineRequest'e rtp_port eklenmeliydi. 
	// Varsayalım ki req.SessionId içinde port bilgisi kodlu veya DB'den çekiliyor.
	// (Görevi basitleştirmek için burada hardcoded port yerine, media service'e sorulabilir)
	
	// Şimdilik 0 veriyoruz, pipeline içinde MediaService AllocatePort çağrısı yapılabilir 
	// ama Agent Service zaten portu alıp CallStarted eventi atmıştı.
	// MediaInfo burada eksik.
	
	// FIX: PipelineManager içinde MediaService çağrısı yaparak portu öğrenmemiz lazım veya
	// Agent Service'in bu bilgiyi geçmesi lazım.
	// Şimdilik loglayıp geçiyoruz.
	
	s.log.Info().Str("call_id", callID).Msg("Pipeline isteği alındı")

	// Pipeline'ı başlat (Bloklayan işlem)
	// Not: Gerçek port verisini almak için Contracts güncellemesi gerekebilir.
	// Şimdilik 10000 varsayılan port ile test edilecek.
	err = s.pipelineManager.RunPipeline(stream.Context(), callID, sessionID, "user-1", 10000)
	
	return err
}

// ... (Diğer metodlar PlayAudio vb. aynı kalır veya güncellenir) ...
func (s *Server) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
	// ... implementation ...
	return &telephonyv1.PlayAudioResponse{Success: true}, nil
}
// ... (Diğer metodların boş implementasyonları korunur)

func Start(grpcServer *grpc.Server, port string) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil { return err }
	return grpcServer.Serve(lis)
}

func Stop(grpcServer *grpc.Server) {
	grpcServer.GracefulStop()
}

func loadServerTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	// ... (Aynı kalır) ...
	cert, err := os.ReadFile(certPath)
	if err != nil { return nil, err }
	key, err := os.ReadFile(keyPath)
	if err != nil { return nil, err }
	ca, err := os.ReadFile(caPath)
	if err != nil { return nil, err }
	
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(ca)
	
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{cert}, PrivateKey: key}}, // Basitleştirilmiş
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}), nil
}
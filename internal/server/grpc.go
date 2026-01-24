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

// SpeakText: Metni sese çevirip çalar.
func (s *Server) SpeakText(ctx context.Context, req *telephonyv1.SpeakTextRequest) (*telephonyv1.SpeakTextResponse, error) {
	s.log.Info().Str("call_id", req.CallId).Str("text", req.Text).Msg("SpeakText isteği alındı.")
	
	// 1. TTS Gateway'den ses al
	ttsReq := &ttsv1.SynthesizeRequest{
		Text: req.Text,
		VoiceId: req.VoiceId,
	}
	
	ttsResp, err := s.pipelineManager.GetClients().TTS.Synthesize(ctx, ttsReq)
	if err != nil {
		s.log.Error().Err(err).Msg("TTS sentezleme hatası")
		return nil, err
	}
	
	s.log.Info().Int("bytes", len(ttsResp.AudioContent)).Msg("TTS sentezi başarılı.")

	// 2. Media Service'e sesi gönder
	stream, err := s.pipelineManager.GetClients().Media.StreamAudioToCall(ctx)
	if err != nil {
		 s.log.Error().Err(err).Msg("Media stream açılamadı")
		 return nil, err
	}
	
	// İlk mesaj (Call ID ile)
	if err := stream.Send(&mediav1.StreamAudioToCallRequest{
		CallId: req.CallId,
		AudioChunk: nil,
	}); err != nil {
		s.log.Error().Err(err).Msg("İlk medya paketi gönderilemedi")
		return nil, err
	}
	
	// Chunking
	const chunkSize = 4096
	audioData := ttsResp.AudioContent
	for i := 0; i < len(audioData); i += chunkSize {
		end := i + chunkSize
		if end > len(audioData) { end = len(audioData) }
		
		if err := stream.Send(&mediav1.StreamAudioToCallRequest{
			CallId: "",
			AudioChunk: audioData[i:end],
		}); err != nil {
			s.log.Error().Err(err).Msg("Ses gönderimi sırasında hata")
			return nil, err
		}
	}
	
	// [DÜZELTME]: Bi-Directional Stream Kapatma Mantığı
	// Önce gönderimi kapatıyoruz.
	if err := stream.CloseSend(); err != nil {
		s.log.Error().Err(err).Msg("Stream send kapatılamadı")
		return nil, err
	}

	// Sonra sunucudan gelecek olası hata/durum mesajlarını tüketiyoruz.
	// Media Service hata dönerse burada yakalarız.
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break // Sunucu da kapattı, her şey yolunda.
		}
		if err != nil {
			s.log.Error().Err(err).Msg("Media Service stream hatası döndü")
			return nil, err
		}
	}
	
	s.log.Info().Msg("SpeakText başarıyla tamamlandı (TTS -> Media).")
	return &telephonyv1.SpeakTextResponse{Success: true, Message: "Played"}, nil
}

// Diğer Legacy metodlar
func (s *Server) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
	return &telephonyv1.PlayAudioResponse{Success: true}, nil
}
func (s *Server) RunPipeline(req *telephonyv1.RunPipelineRequest, stream telephonyv1.TelephonyActionService_RunPipelineServer) error {
	return nil 
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
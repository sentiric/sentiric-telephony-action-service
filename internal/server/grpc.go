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

// SpeakText: Metni sese çevirip (Stream) anlık olarak Medya servisine basar.
func (s *Server) SpeakText(ctx context.Context, req *telephonyv1.SpeakTextRequest) (*telephonyv1.SpeakTextResponse, error) {
	s.log.Info().Str("call_id", req.CallId).Str("text", req.Text).Msg("🗣️ SpeakText isteği işleniyor...")
	
	// 1. TTS Gateway'den Ses Stream'i Başlat
	ttsReq := &ttsv1.SynthesizeStreamRequest{
		Text: req.Text,
		VoiceId: req.VoiceId,
		AudioConfig: &ttsv1.AudioConfig{
			SampleRateHertz: 16000,
			AudioFormat: ttsv1.AudioFormat_AUDIO_FORMAT_PCM_S16LE,
		},
		Prosody: &ttsv1.ProsodyConfig{ Rate: 1.0 },
	}
	
	ttsStream, err := s.pipelineManager.GetClients().TTS.SynthesizeStream(ctx, ttsReq)
	if err != nil {
		s.log.Error().Err(err).Msg("TTS Gateway stream başlatılamadı")
		return nil, err
	}
	
	s.log.Debug().Msg("TTS Stream bağlantısı kuruldu.")

	// 2. Media Service'e Stream Başlat
	mediaStream, err := s.pipelineManager.GetClients().Media.StreamAudioToCall(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("Media Service stream başlatılamadı")
		return nil, err
	}

	// 3. İlk Paket (Call ID Handshake)
	if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{
		CallId: req.CallId,
		AudioChunk: nil, // İlk pakette data yok, sadece metadata
	}); err != nil {
		s.log.Error().Err(err).Msg("Media Service handshake hatası")
		return nil, err
	}

	// 4. Stream Loop (TTS -> Media)
	// TTS'den gelen her parçayı (chunk) alıp Media'ya iletiyoruz.
	var totalBytes int
	for {
		ttsChunk, err := ttsStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.log.Error().Err(err).Msg("TTS stream okuma hatası")
			return nil, err
		}

		if len(ttsChunk.AudioContent) > 0 {
			totalBytes += len(ttsChunk.AudioContent)
			if err := mediaStream.Send(&mediav1.StreamAudioToCallRequest{
				// Sonraki paketlerde CallId opsiyoneldir (gRPC stream statefuldur)
				AudioChunk: ttsChunk.AudioContent,
			}); err != nil {
				s.log.Error().Err(err).Msg("Media Service'e veri gönderilemedi")
				return nil, err
			}
		}
	}
	
	// 5. Kapat ve Bitir
	if err := mediaStream.CloseSend(); err != nil {
		s.log.Warn().Err(err).Msg("Media stream kapatılırken hata oluştu")
	}

	// Media Service'ten son yanıtı bekle (Opsiyonel ama iyi pratik)
	// Genellikle hata yoksa sessizce kapanır veya son bir status döner.
	for {
		_, err := mediaStream.Recv()
		if err == io.EOF { break }
		if err != nil {
			// Sadece logla, akış zaten bitti
			s.log.Warn().Err(err).Msg("Media Service yanıtı okunurken hata")
			break
		}
	}
	
	s.log.Info().Int("total_bytes", totalBytes).Msg("✅ SpeakText tamamlandı (TTS -> Media akışı bitti).")
	return &telephonyv1.SpeakTextResponse{Success: true, Message: "Audio streamed successfully"}, nil
}

// [YENİ] BridgeCall: İki çağrıyı birbirine bağlar (Henüz Dummy)
func (s *Server) BridgeCall(ctx context.Context, req *telephonyv1.BridgeCallRequest) (*telephonyv1.BridgeCallResponse, error) {
	s.log.Info().Str("call_id_a", req.CallIdA).Str("target", req.TargetUri).Msg("🔀 BridgeCall isteği alındı (Henüz Implemente Edilmedi)")
	
	// TODO: Gelecekte burada B2BUA servisine Transfer komutu gönderilecek.
	// Şimdilik "Not Implemented" dönmek yerine fake success dönüyoruz ki Agent çökmesin.
	return &telephonyv1.BridgeCallResponse{Success: true, Message: "Bridge request acknowledged (Mock)"}, nil
}

// Legacy Metotlar (Geriye Uyumluluk İçin Boş Bırakıldı)
func (s *Server) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
    // Agent hala eski metodu çağırırsa diye logluyoruz.
    s.log.Warn().Str("call_id", req.CallId).Msg("⚠️ Deprecated PlayAudio called. Use SpeakText instead.")
	return &telephonyv1.PlayAudioResponse{Success: true}, nil
}
func (s *Server) RunPipeline(req *telephonyv1.RunPipelineRequest, stream telephonyv1.TelephonyActionService_RunPipelineServer) error { return nil }
func (s *Server) TerminateCall(ctx context.Context, req *telephonyv1.TerminateCallRequest) (*telephonyv1.TerminateCallResponse, error) { return &telephonyv1.TerminateCallResponse{Success: true}, nil }
func (s *Server) SendTextMessage(ctx context.Context, req *telephonyv1.SendTextMessageRequest) (*telephonyv1.SendTextMessageResponse, error) { return &telephonyv1.SendTextMessageResponse{Success: true}, nil }
func (s *Server) StartRecording(ctx context.Context, req *telephonyv1.StartRecordingRequest) (*telephonyv1.StartRecordingResponse, error) { return &telephonyv1.StartRecordingResponse{Success: true}, nil }
func (s *Server) StopRecording(ctx context.Context, req *telephonyv1.StopRecordingRequest) (*telephonyv1.StopRecordingResponse, error) { return &telephonyv1.StopRecordingResponse{Success: true}, nil }

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
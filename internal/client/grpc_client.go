package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sipv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/sip/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type Clients struct {
	Media     mediav1.MediaServiceClient
	STT       sttv1.SttGatewayServiceClient
	Dialog    dialogv1.DialogServiceClient
	TTS       ttsv1.TtsGatewayServiceClient
	Signaling sipv1.SipSignalingServiceClient
}

func NewClients(cfg *config.Config) (*Clients, error) {
	log.Info().Msg("🔌 Alt servislere bağlantılar kuruluyor...")

	// Lazy connection: Bağlantılar ilk istekte kurulur.
	mediaConn, err := createConnection(cfg, cfg.MediaServiceURL)
	if err != nil { return nil, err }
	
	sttConn, err := createConnection(cfg, cfg.SttGatewayURL)
	if err != nil { return nil, err }
	
	dialogConn, err := createConnection(cfg, cfg.DialogServiceURL)
	if err != nil { return nil, err }
	
	ttsConn, err := createConnection(cfg, cfg.TtsGatewayURL)
	if err != nil { return nil, err }
	
	sipConn, err := createConnection(cfg, cfg.SipSignalingURL)
	if err != nil { return nil, err }

	log.Info().Msg("✅ Tüm gRPC istemcileri yapılandırıldı (Lazy Connect).")

	return &Clients{
		Media:     mediav1.NewMediaServiceClient(mediaConn),
		STT:       sttv1.NewSttGatewayServiceClient(sttConn),
		Dialog:    dialogv1.NewDialogServiceClient(dialogConn),
		TTS:       ttsv1.NewTtsGatewayServiceClient(ttsConn),
		Signaling: sipv1.NewSipSignalingServiceClient(sipConn),
	}, nil
}

func createConnection(cfg *config.Config, targetURL string) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	// mTLS Kontrolü
	if cfg.CertPath != "" && cfg.KeyPath != "" && cfg.CaPath != "" {
		// Dosyaların varlığını kontrol et
		if _, err := os.Stat(cfg.CertPath); os.IsNotExist(err) {
			log.Warn().Str("path", cfg.CertPath).Msg("Sertifika dosyası bulunamadı, INSECURE moduna geçiliyor.")
			opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		} else {
			clientCert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("cert load error: %w", err)
			}
			caCert, err := os.ReadFile(cfg.CaPath)
			if err != nil {
				return nil, fmt.Errorf("ca load error: %w", err)
			}
			caPool := x509.NewCertPool()
			if !caPool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to append CA")
			}

			serverName := strings.Split(targetURL, ":")[0]
			creds := credentials.NewTLS(&tls.Config{
				Certificates: []tls.Certificate{clientCert},
				RootCAs:      caPool,
				ServerName:   serverName,
			})
			opts = append(opts, grpc.WithTransportCredentials(creds))
		}
	} else {
		log.Warn().Str("target", targetURL).Msg("⚠️ mTLS config eksik, INSECURE bağlanılıyor.")
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// NewClient non-blocking'dir
	return grpc.NewClient(targetURL, opts...)
}
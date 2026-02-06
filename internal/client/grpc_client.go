// internal/client/grpc_client.go
package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings" // Artık kesinlikle kullanılıyor

	"github.com/rs/zerolog/log"

	// Contracts
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
	TTS       ttsv1.TtsGatewayServiceClient
	Dialog    dialogv1.DialogServiceClient
	Signaling sipv1.SipSignalingServiceClient
}

func NewClients(cfg *config.Config) (*Clients, error) {
	log.Info().Msg("🔌 Servis bağlantıları başlatılıyor...")

	// Sertifikaları bir kez yükle
	tlsCreds, err := loadClientTLS(cfg.CertPath, cfg.KeyPath, cfg.CaPath)
	if err != nil {
		log.Warn().Err(err).Msg("TLS sertifikaları yüklenemedi, INSECURE mod denenecek.")
	}

	// Bağlantıları oluştur
	mediaConn, err := connect(cfg.MediaServiceURL, "media-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("media connection failed: %w", err)
	}

	sttConn, err := connect(cfg.SttGatewayURL, "stt-gateway-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("stt connection failed: %w", err)
	}

	ttsConn, err := connect(cfg.TtsGatewayURL, "tts-gateway-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("tts connection failed: %w", err)
	}

	dialogConn, err := connect(cfg.DialogServiceURL, "dialog-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("dialog connection failed: %w", err)
	}

	sipConn, err := connect(cfg.SipSignalingURL, "sip-signaling-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("sip connection failed: %w", err)
	}

	log.Info().Msg("✅ Tüm gRPC istemcileri hazır.")

	return &Clients{
		Media:     mediav1.NewMediaServiceClient(mediaConn),
		STT:       sttv1.NewSttGatewayServiceClient(sttConn),
		TTS:       ttsv1.NewTtsGatewayServiceClient(ttsConn),
		Dialog:    dialogv1.NewDialogServiceClient(dialogConn),
		Signaling: sipv1.NewSipSignalingServiceClient(sipConn),
	}, nil
}

func connect(targetURL string, serverName string, tlsCreds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	// [FIX] URL Sanitization: strings paketini kullanarak http/https ön eklerini temizle.
	// Go gRPC client, target olarak "host:port" formatını bekler, şema istemez.
	cleanTarget := targetURL
	if strings.HasPrefix(cleanTarget, "https://") {
		cleanTarget = strings.TrimPrefix(cleanTarget, "https://")
	} else if strings.HasPrefix(cleanTarget, "http://") {
		cleanTarget = strings.TrimPrefix(cleanTarget, "http://")
	}

	// [FIX] SNI (Server Name Indication) için host adını ayıkla.
	// Port numarasını atıp sadece hostname'i alıyoruz (örn: "media-service:13031" -> "media-service")
	sniServerName := serverName
	if parts := strings.Split(cleanTarget, ":"); len(parts) > 0 && parts[0] != "" {
		// Eğer target içinde mantıklı bir hostname varsa onu da kullanabiliriz,
		// ancak mTLS sertifikalarında genellikle servis adı (serverName) kullanılır.
		// Güvenlik için parametre olarak gelen serverName'i önceliklendiriyoruz.
	}

	if tlsCreds != nil {
		// mTLS aktif
		opts = append(opts, grpc.WithTransportCredentials(tlsCreds))
		// Docker/K8s içindeki sertifika isim eşleşmesi için Authority override
		opts = append(opts, grpc.WithAuthority(sniServerName))
	} else {
		// Insecure fallback
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.NewClient(cleanTarget, opts...)
}

func loadClientTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	// Dosya varlık kontrolü
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, err
	}

	// Sertifikaları oku
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	caPem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		return nil, fmt.Errorf("failed to append CA cert")
	}

	// mTLS Config
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
		// MinVersion:   tls.VersionTLS12, // Güvenlik sıkılaştırması
	}

	return credentials.NewTLS(config), nil
}

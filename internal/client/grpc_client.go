package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sipv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/sip/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	// telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"
	// userv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/user/v1"
	
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type Clients struct {
	// User            userv1.UserServiceClient
	// TelephonyAction telephonyv1.TelephonyActionServiceClient
	Media           mediav1.MediaServiceClient
	STT             sttv1.SttGatewayServiceClient
	TTS             ttsv1.TtsGatewayServiceClient
	Dialog          dialogv1.DialogServiceClient
	Signaling       sipv1.SipSignalingServiceClient
}

func NewClients(cfg *config.Config) (*Clients, error) {
	log.Info().Msg("🔌 Servis bağlantıları kuruluyor...")

	/// ?
	// userConn, err := createConnection(cfg, cfg.UserServiceURL)
	// if err != nil { return nil, err }
	
	// ???
	// telephonyConn, err := createConnection(cfg, cfg.TelephonyActionURL)
	// if err != nil { return nil, err }
	

	
	// Media Service (Eksikse ekleyin)
	mediaConn, err := createConnection(cfg, cfg.MediaServiceURL)
	if err != nil { return nil, err }

	sttConn, err := createConnection(cfg, cfg.SttGatewayURL)
	if err != nil { return nil, err }

	ttsConn, err := createConnection(cfg, cfg.TtsGatewayURL)
	if err != nil { return nil, err }

	dialogConn, err := createConnection(cfg, cfg.DialogServiceURL)
	if err != nil { return nil, err }
	
	sipConn, err := createConnection(cfg, cfg.SipSignalingURL)
	if err != nil { return nil, err }

	log.Info().Msg("✅ İstemciler hazır.")

	return &Clients{
		// User:            userv1.NewUserServiceClient(userConn),
		// TelephonyAction: telephonyv1.NewTelephonyActionServiceClient(telephonyConn),
		Media:           mediav1.NewMediaServiceClient(mediaConn),
		STT:             sttv1.NewSttGatewayServiceClient(sttConn),
		TTS:             ttsv1.NewTtsGatewayServiceClient(ttsConn),
		Dialog:          dialogv1.NewDialogServiceClient(dialogConn),
		Signaling:       sipv1.NewSipSignalingServiceClient(sipConn),
	}, nil
}

func createConnection(cfg *config.Config, targetRawURL string) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption
	
	// [FIX] URL Parsing Logic
	// Hedef URL "https://host:port" formatında gelebilir.
	// Go gRPC client, target olarak "host:port" bekler.
	
	var targetAddr string
	var useTLS bool

	parsedURL, err := url.Parse(targetRawURL)
	if err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") {
		targetAddr = parsedURL.Host // host:port
		useTLS = (parsedURL.Scheme == "https")
	} else {
		// Scheme yoksa olduğu gibi kabul et (Fallback)
		targetAddr = targetRawURL
		useTLS = false // Varsayılan insecure
		log.Warn().Str("url", targetRawURL).Msg("URL scheme parse edilemedi, raw kullanılıyor.")
	}

	if useTLS {
		if cfg.CertPath != "" && cfg.KeyPath != "" && cfg.CaPath != "" {
			if _, err := os.Stat(cfg.CertPath); os.IsNotExist(err) {
				log.Warn().Str("path", cfg.CertPath).Msg("Sertifika bulunamadı, INSECURE'a düşülüyor.")
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

				// ServerName'i adresten çıkar (portu at)
				serverName := strings.Split(targetAddr, ":")[0]
				creds := credentials.NewTLS(&tls.Config{
					Certificates: []tls.Certificate{clientCert},
					RootCAs:      caPool,
					ServerName:   serverName,
				})
				opts = append(opts, grpc.WithTransportCredentials(creds))
				log.Debug().Str("target", targetAddr).Msg("🔒 mTLS bağlantısı hazırlanıyor")
			}
		} else {
			// HTTPS var ama sertifika yok -> Sistem sertifikalarını kullan (Public Web)
			// Ancak bizim sistemde mTLS zorunlu olduğu için bu bir uyarı sebebidir.
			creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
			opts = append(opts, grpc.WithTransportCredentials(creds))
			log.Warn().Str("target", targetAddr).Msg("⚠️ HTTPS var ama yerel sertifika yok, SkipVerify kullanılıyor.")
		}
	} else {
		// HTTP
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.NewClient(targetAddr, opts...)
}
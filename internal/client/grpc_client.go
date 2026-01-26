// internal/client/grpc_client.go
package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	
	// Contracts
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sipv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/sip/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"

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

func NewClients(cfg *config.Config, log zerolog.Logger) (*Clients, error) {
	log.Info().Msg("🔌 Servis bağlantıları başlatılıyor...")

	tlsCreds, err := loadClientTLS(cfg.CertPath, cfg.KeyPath, cfg.CaPath)
	if err != nil {
		log.Warn().Err(err).Msg("TLS sertifikaları yüklenemedi, INSECURE mod denenecek.")
	}

	mediaConn, err := connect(cfg.MediaServiceURL, "media-service", tlsCreds)
	if err != nil { return nil, fmt.Errorf("media connection failed: %w", err) }

	sttConn, err := connect(cfg.SttGatewayURL, "stt-gateway-service", tlsCreds)
	if err != nil { return nil, fmt.Errorf("stt connection failed: %w", err) }

	ttsConn, err := connect(cfg.TtsGatewayURL, "tts-gateway-service", tlsCreds)
	if err != nil { return nil, fmt.Errorf("tts connection failed: %w", err) }

	dialogConn, err := connect(cfg.DialogServiceURL, "dialog-service", tlsCreds)
	if err != nil { return nil, fmt.Errorf("dialog connection failed: %w", err) }

	sipConn, err := connect(cfg.SipSignalingURL, "sip-signaling-service", tlsCreds)
	if err != nil { return nil, fmt.Errorf("sip connection failed: %w", err) }

	log.Info().Msg("✅ Tüm gRPC istemcileri hazır.")

	return &Clients{
		Media:     mediav1.NewMediaServiceClient(mediaConn),
		STT:       sttv1.NewSttGatewayServiceClient(sttConn),
		TTS:       ttsv1.NewTtsGatewayServiceClient(ttsConn),
		Dialog:    dialogv1.NewDialogServiceClient(dialogConn),
		Signaling: sipv1.NewSipSignalingServiceClient(sipConn),
	}, nil
}

func connect(target string, serverName string, tlsCreds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{}
	
	if tlsCreds != nil {
		// Override ServerName for SNI matching inside Docker/K8s
		opts = append(opts, grpc.WithTransportCredentials(tlsCreds))
		opts = append(opts, grpc.WithAuthority(serverName)) 
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.NewClient(target, opts...)
}

func loadClientTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	// Dosya kontrolü
	if _, err := os.Stat(certPath); os.IsNotExist(err) { return nil, err }
	
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil { return nil, err }

	caPem, err := os.ReadFile(caPath)
	if err != nil { return nil, err }

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		return nil, fmt.Errorf("failed to append CA cert")
	}

	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
		// InsecureSkipVerify: true, // DEV ONLY - Production'da kaldırılmalı
	}

	return credentials.NewTLS(config), nil
}
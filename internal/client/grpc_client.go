// sentiric-telephony-action-service/internal/client/grpc_client.go
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
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"

	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type Clients struct {
	Media  mediav1.MediaServiceClient
	STT    sttv1.SttGatewayServiceClient
	TTS    ttsv1.TtsGatewayServiceClient
	Dialog dialogv1.DialogServiceClient
}

func NewClients(cfg *config.Config) (*Clients, error) {
	log.Info().Msg("🔌 Servis bağlantıları (Smart SNI/mTLS) kuruluyor...")

	tlsCreds, err := loadClientTLS(cfg.CertPath, cfg.KeyPath, cfg.CaPath)
	if err != nil {
		log.Warn().Err(err).Msg("mTLS sertifikaları yüklenemedi, INSECURE mod denenecek.")
	}

	mediaConn, err := connect(cfg.MediaServiceURL, "media-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("media fail: %w", err)
	}

	sttConn, err := connect(cfg.SttGatewayURL, "stt-gateway-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("stt fail: %w", err)
	}

	ttsConn, err := connect(cfg.TtsGatewayURL, "tts-gateway-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("tts fail: %w", err)
	}

	dialogConn, err := connect(cfg.DialogServiceURL, "dialog-service", tlsCreds)
	if err != nil {
		return nil, fmt.Errorf("dialog fail: %w", err)
	}

	return &Clients{
		Media:  mediav1.NewMediaServiceClient(mediaConn),
		STT:    sttv1.NewSttGatewayServiceClient(sttConn),
		TTS:    ttsv1.NewTtsGatewayServiceClient(ttsConn),
		Dialog: dialogv1.NewDialogServiceClient(dialogConn),
	}, nil
}

func connect(targetURL string, serverName string, tlsCreds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	// URL Sanitization: Remove schemas
	cleanTarget := targetURL
	for _, prefix := range []string{"https://", "http://"} {
		cleanTarget = strings.TrimPrefix(cleanTarget, prefix)
	}

	if tlsCreds != nil {
		opts = append(opts, grpc.WithTransportCredentials(tlsCreds))
		opts = append(opts, grpc.WithAuthority(serverName)) // SNI Override
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.NewClient(cleanTarget, opts...)
}

func loadClientTLS(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("cert file missing: %s", certPath)
	}

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
		return nil, fmt.Errorf("CA fail")
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
	}), nil
}

package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	dialogv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/dialog/v1"
	mediav1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/media/v1"
	sttv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/stt/v1"
	ttsv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/tts/v1"
	sipv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/sip/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Clients struct {
	Media    mediav1.MediaServiceClient
	STT      sttv1.SttGatewayServiceClient
	Dialog   dialogv1.DialogServiceClient
	TTS      ttsv1.TtsGatewayServiceClient
	Signaling sipv1.SipSignalingServiceClient
}

func NewClients(cfg *config.Config) (*Clients, error) {
	mediaConn, err := createSecureConnection(cfg, cfg.MediaServiceURL)
	if err != nil { return nil, fmt.Errorf("media service connection failed: %w", err) }

	sttConn, err := createSecureConnection(cfg, cfg.SttGatewayURL) // Config'e eklenmeli
	if err != nil { return nil, fmt.Errorf("stt gateway connection failed: %w", err) }

	dialogConn, err := createSecureConnection(cfg, cfg.DialogServiceURL) // Config'e eklenmeli
	if err != nil { return nil, fmt.Errorf("dialog service connection failed: %w", err) }

	ttsConn, err := createSecureConnection(cfg, cfg.TtsGatewayURL)
	if err != nil { return nil, fmt.Errorf("tts gateway connection failed: %w", err) }
	
	sipConn, err := createSecureConnection(cfg, cfg.SipSignalingURL)
	if err != nil { return nil, fmt.Errorf("sip signaling connection failed: %w", err) }

	return &Clients{
		Media:    mediav1.NewMediaServiceClient(mediaConn),
		STT:      sttv1.NewSttGatewayServiceClient(sttConn),
		Dialog:   dialogv1.NewDialogServiceClient(dialogConn),
		TTS:      ttsv1.NewTtsGatewayServiceClient(ttsConn),
		Signaling: sipv1.NewSipSignalingServiceClient(sipConn),
	}, nil
}

func createSecureConnection(cfg *config.Config, targetURL string) (*grpc.ClientConn, error) {
	clientCert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("client cert error: %w", err)
	}

	caCert, err := os.ReadFile(cfg.CaPath)
	if err != nil {
		return nil, fmt.Errorf("ca cert error: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("ca cert append error")
	}

	serverName := strings.Split(targetURL, ":")[0]
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   serverName,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return grpc.DialContext(ctx, targetURL, grpc.WithTransportCredentials(creds), grpc.WithBlock())
}
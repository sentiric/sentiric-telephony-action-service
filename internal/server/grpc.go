// sentiric-telephony-action-service/internal/server/grpc.go
package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewGrpcServer, mTLS yapılandırması ile yeni bir gRPC sunucusu oluşturur.
func NewGrpcServer(certPath, keyPath, caPath string, log zerolog.Logger) *grpc.Server {
	creds, err := loadServerTLS(certPath, keyPath, caPath, log)
	if err != nil {
		log.Fatal().Err(err).Msg("TLS kimlik bilgileri yüklenemedi")
	}

	return grpc.NewServer(grpc.Creds(creds))
}

// Start, verilen gRPC sunucusunu belirtilen portta dinlemeye başlar.
func Start(grpcServer *grpc.Server, port string) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("gRPC portu dinlenemedi: %w", err)
	}
	return grpcServer.Serve(listener)
}

// Stop, gRPC sunucusunu zarif bir şekilde durdurur.
func Stop(grpcServer *grpc.Server) {
	grpcServer.GracefulStop()
}

func loadServerTLS(certPath, keyPath, caPath string, log zerolog.Logger) (credentials.TransportCredentials, error) {
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("sunucu sertifikası yüklenemedi: %w", err)
	}
	caCert, err := ioutil.ReadFile(caPath)
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

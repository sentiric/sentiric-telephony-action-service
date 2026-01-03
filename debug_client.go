package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"

	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	// Yolları kendi sisteminize göre ayarlayın
	certDir := "../sentiric-certificates/certs"
	
	// Agent Service kimliğiyle bağlanıyoruz
	certFile := certDir + "/agent-service.crt"
	keyFile := certDir + "/agent-service.key"
	caFile := certDir + "/ca.crt"

	// Sertifikaları Yükle
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil { log.Fatalf("Cert load err: %v", err) }
	
	caPem, err := os.ReadFile(caFile)
	if err != nil { log.Fatalf("CA load err: %v", err) }
	
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) { log.Fatal("CA append fail") }

	// TLS Config
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
		ServerName:   "telephony-action-service", // SNI: Sertifikadaki isimle eşleşmeli
	}

	// Bağlan
	conn, err := grpc.Dial("localhost:13111", grpc.WithTransportCredentials(credentials.NewTLS(config)))
	if err != nil { log.Fatalf("Dial fail: %v", err) }
	defer conn.Close()

	client := telephonyv1.NewTelephonyActionServiceClient(conn)
	
	// İstek Gönder
	stream, err := client.RunPipeline(context.Background(), &telephonyv1.RunPipelineRequest{
		CallId: "go-debug-call",
		SessionId: "go-debug-session",
	})
	if err != nil { log.Fatalf("RPC fail: %v", err) }

	resp, err := stream.Recv()
	if err != nil { log.Fatalf("Recv fail: %v", err) }
	
	fmt.Printf("BAŞARILI! Sunucu Yanıtı: %s\n", resp.Message)
}
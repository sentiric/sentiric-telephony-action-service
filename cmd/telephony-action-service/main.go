// cmd/telephony-action-service/main.go
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sentiric/sentiric-telephony-action-service/internal/client"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"github.com/sentiric/sentiric-telephony-action-service/internal/logger"
	"github.com/sentiric/sentiric-telephony-action-service/internal/server"
)

var (
	ServiceVersion string = "1.1.0"
)

func main() {
	cfg, err := config.Load()
	if err != nil { panic(err) }

	log := logger.New("telephony-action-service", cfg.Env, cfg.LogLevel)
	log.Info().Str("version", ServiceVersion).Msg("🚀 Servis başlatılıyor...")

	// 1. İstemcileri Başlat
	clients, err := client.NewClients(cfg, log)
	if err != nil {
		log.Fatal().Err(err).Msg("Bağımlı servislere bağlanılamadı")
	}

	// 2. gRPC Sunucusu
	grpcServer := server.NewGrpcServer(cfg, log, clients)
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", cfg.GRPCPort))
	if err != nil { log.Fatal().Err(err).Msg("gRPC portu açılamadı") }

	go func() {
		log.Info().Str("port", cfg.GRPCPort).Msg("gRPC dinleniyor")
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatal().Err(err).Msg("gRPC hatası")
		}
	}()

	// 3. Health Check
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("OK")) })
		log.Info().Str("port", cfg.HttpPort).Msg("HTTP dinleniyor")
		http.ListenAndServe(":"+cfg.HttpPort, nil)
	}()

	// 4. Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	
	log.Warn().Msg("Kapatılıyor...")
	grpcServer.GracefulStop()
	log.Info().Msg("Bitti.")
}
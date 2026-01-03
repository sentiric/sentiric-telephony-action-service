package main

import (
	"context"
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
	ServiceVersion string
	GitCommit      string
	BuildDate      string
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logger henüz hazır olmadığı için panic kullanıyoruz
		panic("Konfigürasyon yüklenemedi: " + err.Error())
	}

	appLog := logger.New("telephony-action-service", cfg.Env, cfg.LogLevel)

	appLog.Info().
		Str("version", ServiceVersion).
		Str("commit", GitCommit).
		Str("build_date", BuildDate).
		Str("profile", cfg.Env).
		Msg("🚀 Sentiric Telephony Action Service başlatılıyor...")

	clients, err := client.NewClients(cfg)
	if err != nil {
		appLog.Fatal().Err(err).Msg("İstemciler başlatılamadı")
	}

	grpcServer := server.NewGrpcServer(cfg.CertPath, cfg.KeyPath, cfg.CaPath, appLog, clients)
	
	// gRPC Server
	go func() {
		appLog.Info().Str("port", cfg.GRPCPort).Msg("gRPC Sunucusu dinleniyor")
		if err := server.Start(grpcServer, cfg.GRPCPort); err != nil {
			appLog.Fatal().Err(err).Msg("gRPC Sunucusu hatayla kapandı")
		}
	}()
	
	// Health Check HTTP Sunucusu
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})
		
		addr := ":" + cfg.HttpPort
		appLog.Info().Str("addr", addr).Msg("HTTP Health Check dinleniyor")
		if err := http.ListenAndServe(addr, nil); err != nil {
			appLog.Error().Err(err).Msg("HTTP sunucusu hatası")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	
	appLog.Warn().Msg("Kapatma sinyali alındı...")
	
	// Graceful shutdown context
	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server.Stop(grpcServer)
	appLog.Info().Msg("Servis durduruldu.")
}
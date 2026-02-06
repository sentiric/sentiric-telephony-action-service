// cmd/telephony-action-service/main.go
package main

import (
	"context"
	"fmt"
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
		fmt.Fprintf(os.Stderr, "Kritik Hata: Konfigürasyon yüklenemedi: %v\n", err)
		os.Exit(1)
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

	// KRİTİK DÜZELTME: server.NewGrpcServer artık var ve çağrılabilir
	grpcServer := server.NewGrpcServer(cfg, appLog, clients)

	// gRPC Server
	go func() {
		appLog.Info().Str("port", cfg.GRPCPort).Msg("gRPC Sunucusu dinleniyor")
		// KRİTİK DÜZELTME: server.Start artık var ve çağrılabilir
		if err := server.Start(grpcServer, cfg.GRPCPort); err != nil && err.Error() != "http: Server closed" {
			appLog.Fatal().Err(err).Msg("gRPC Sunucusu hatayla kapandı")
		}
	}()

	// Health Check HTTP Sunucusu
	httpServer := &http.Server{
		Addr: fmt.Sprintf(":%s", cfg.HttpPort),
	}
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})

		addr := ":" + cfg.HttpPort
		appLog.Info().Str("addr", addr).Msg("HTTP Health Check dinleniyor")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLog.Error().Err(err).Msg("HTTP sunucusu hatası")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	appLog.Warn().Msg("Kapatma sinyali alındı...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// KRİTİK DÜZELTME: server.Stop artık var ve çağrılabilir
	server.Stop(grpcServer)

	if err := httpServer.Shutdown(ctx); err != nil {
		appLog.Error().Err(err).Msg("HTTP sunucusu kapatılırken hata oluştu")
	}

	appLog.Info().Msg("Servis durduruldu.")
}

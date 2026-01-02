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

func main() {
	cfg, err := config.Load()
	if err != nil { panic(err) }
	
	log := logger.New("telephony-action-service", cfg.Env, cfg.LogLevel)

	// İstemcileri başlat
	clients, err := client.NewClients(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Alt servislere bağlanılamadı")
	}

	// Server başlat
	grpcServer := server.NewGrpcServer(cfg.CertPath, cfg.KeyPath, cfg.CaPath, log, clients)
	
	go func() {
		if err := server.Start(grpcServer, cfg.GRPCPort); err != nil {
			log.Fatal().Err(err).Msg("gRPC sunucusu çöktü")
		}
	}()
	
	// ... (Graceful shutdown ve HTTP server kodları aynı kalır) ...
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	server.Stop(grpcServer)
}
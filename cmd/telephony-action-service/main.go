// sentiric-telephony-action-service/cmd/telephony-action-service/main.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/sentiric/sentiric-telephony-action-service/internal/config"
	"github.com/sentiric/sentiric-telephony-action-service/internal/logger"
	"github.com/sentiric/sentiric-telephony-action-service/internal/server"

	telephonyv1 "github.com/sentiric/sentiric-contracts/gen/go/sentiric/telephony/v1"
)

var (
	ServiceVersion string
	GitCommit      string
	BuildDate      string
)

const serviceName = "telephony-action-service"

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Kritik Hata: Konfigürasyon yüklenemedi: %v\n", err)
		os.Exit(1)
	}

	log := logger.New(serviceName, cfg.Env, cfg.LogLevel)

	log.Info().
		Str("version", ServiceVersion).
		Str("commit", GitCommit).
		Str("build_date", BuildDate).
		Str("profile", cfg.Env).
		Msg("🚀 Sentiric Telephony Action Service başlatılıyor...")

	// HTTP ve gRPC sunucularını oluştur
	grpcServer := server.NewGrpcServer(cfg.CertPath, cfg.KeyPath, cfg.CaPath, log)
	httpServer := startHttpServer(cfg.HttpPort, log)

	// gRPC Handler'ı kaydet
	telephonyv1.RegisterTelephonyActionServiceServer(grpcServer, &actionHandler{})

	// gRPC sunucusunu bir goroutine'de başlat
	go func() {
		log.Info().Str("port", cfg.GRPCPort).Msg("gRPC sunucusu dinleniyor...")
		if err := server.Start(grpcServer, cfg.GRPCPort); err != nil && err.Error() != "http: Server closed" {
			log.Error().Err(err).Msg("gRPC sunucusu başlatılamadı")
		}
	}()

	// Graceful shutdown için sinyal dinleyicisi
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Warn().Msg("Kapatma sinyali alındı, servisler durduruluyor...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server.Stop(grpcServer)
	log.Info().Msg("gRPC sunucusu durduruldu.")

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("HTTP sunucusu düzgün kapatılamadı.")
	} else {
		log.Info().Msg("HTTP sunucusu durduruldu.")
	}

	log.Info().Msg("Servis başarıyla durduruldu.")
}

func startHttpServer(port string, log zerolog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status": "ok"}`)
	})

	addr := fmt.Sprintf(":%s", port)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Info().Str("port", port).Msg("HTTP sunucusu (health) dinleniyor")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP sunucusu başlatılamadı")
		}
	}()
	return srv
}

// =================================================================
// GRPC HANDLER IMPLEMENTASYONU (Placeholder)
// =================================================================

type actionHandler struct {
	telephonyv1.UnimplementedTelephonyActionServiceServer
}

func (*actionHandler) PlayAudio(ctx context.Context, req *telephonyv1.PlayAudioRequest) (*telephonyv1.PlayAudioResponse, error) {
	log := zerolog.Ctx(ctx).With().Str("rpc", "PlayAudio").Str("call_id", req.GetCallId()).Logger()
	log.Info().Str("uri", req.GetAudioUri()).Msg("PlayAudio isteği alındı (Placeholder)")
	// TODO: Media Service çağrısı buraya gelecek.
	return &telephonyv1.PlayAudioResponse{Success: true}, nil
}

func (*actionHandler) TerminateCall(ctx context.Context, req *telephonyv1.TerminateCallRequest) (*telephonyv1.TerminateCallResponse, error) {
	log := zerolog.Ctx(ctx).With().Str("rpc", "TerminateCall").Str("call_id", req.GetCallId()).Logger()
	log.Info().Str("reason", req.GetReason()).Msg("TerminateCall isteği alındı (Placeholder)")
	// TODO: B2BUA Service veya Sip Signaling Service çağrısı buraya gelecek.
	return &telephonyv1.TerminateCallResponse{Success: true}, nil
}

func (*actionHandler) SendTextMessage(ctx context.Context, req *telephonyv1.SendTextMessageRequest) (*telephonyv1.SendTextMessageResponse, error) {
	log := zerolog.Ctx(ctx).With().Str("rpc", "SendTextMessage").Str("to", req.GetTo()).Logger()
	log.Info().Str("body", req.GetBody()).Msg("SendTextMessage isteği alındı (Placeholder)")
	// TODO: Messaging Gateway Service çağrısı buraya gelecek.
	return &telephonyv1.SendTextMessageResponse{Success: true}, nil
}

func (*actionHandler) StartRecording(ctx context.Context, req *telephonyv1.StartRecordingRequest) (*telephonyv1.StartRecordingResponse, error) {
	log := zerolog.Ctx(ctx).With().Str("rpc", "StartRecording").Str("call_id", req.GetCallId()).Logger()
	log.Info().Str("output", req.GetOutputUri()).Msg("StartRecording isteği alındı (Placeholder)")
	// TODO: Media Service çağrısı buraya gelecek.
	return &telephonyv1.StartRecordingResponse{Success: true}, nil
}

func (*actionHandler) StopRecording(ctx context.Context, req *telephonyv1.StopRecordingRequest) (*telephonyv1.StopRecordingResponse, error) {
	log := zerolog.Ctx(ctx).With().Str("rpc", "StopRecording").Str("call_id", req.GetCallId()).Logger()
	log.Info().Msg("StopRecording isteği alındı (Placeholder)")
	// TODO: Media Service çağrısı buraya gelecek.
	return &telephonyv1.StopRecordingResponse{Success: true}, nil
}

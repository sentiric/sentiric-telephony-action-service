package config

import (
	"os"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type Config struct {
	GRPCPort string
	HttpPort string
	CertPath string
	KeyPath  string
	CaPath   string
	LogLevel string
	Env      string

	// Bağımlı Servisler
	MediaServiceURL  string
	TtsGatewayURL    string
	SttGatewayURL    string
	DialogServiceURL string
	SipSignalingURL  string
}

func Load() (*Config, error) {
	godotenv.Load() // .env varsa yükle

	return &Config{
		GRPCPort: GetEnv("TELEPHONY_ACTION_SERVICE_GRPC_PORT", "13111"),
		HttpPort: GetEnv("TELEPHONY_ACTION_SERVICE_HTTP_PORT", "13110"),

		CertPath: GetEnvOrFail("TELEPHONY_ACTION_SERVICE_CERT_PATH"),
		KeyPath:  GetEnvOrFail("TELEPHONY_ACTION_SERVICE_KEY_PATH"),
		CaPath:   GetEnvOrFail("GRPC_TLS_CA_PATH"),
		
		LogLevel: GetEnv("LOG_LEVEL", "info"),
		Env:      GetEnv("ENV", "production"),

		MediaServiceURL:  GetEnv("MEDIA_SERVICE_TARGET_GRPC_URL", "media-service:13031"),
		TtsGatewayURL:    GetEnv("TTS_GATEWAY_TARGET_GRPC_URL", "tts-gateway:14011"),
		SttGatewayURL:    GetEnv("STT_GATEWAY_TARGET_GRPC_URL", "stt-gateway:15021"),
		DialogServiceURL: GetEnv("DIALOG_SERVICE_TARGET_GRPC_URL", "dialog-service:12061"),
		SipSignalingURL:  GetEnv("SIP_SIGNALING_TARGET_GRPC_URL", "sip-signaling-service:13021"),
	}, nil
}

func GetEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func GetEnvOrFail(key string) string {
	value, exists := os.LookupEnv(key)
	if !exists {
		log.Fatal().Str("variable", key).Msg("Gerekli ortam değişkeni eksik. Lütfen .env dosyasını kontrol edin.")
	}
	return value
}
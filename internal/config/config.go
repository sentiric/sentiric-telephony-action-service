// internal/config/config.go
package config

import (
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type Config struct {
	Env         string
	LogLevel    string
	ServiceVersion string

	// Server
	GRPCPort string
	HttpPort string
	MetricsPort string

	// Security (mTLS)
	CertPath string
	KeyPath  string
	CaPath   string

	// Dependencies (Gateways)
	MediaServiceURL    string
	SttGatewayURL      string
	TtsGatewayURL      string
	DialogServiceURL   string
	SipSignalingURL    string
}

func Load() (*Config, error) {
	_ = godotenv.Load() // .env varsa yükle, yoksa environment'a güven

	cfg := &Config{
		Env:            getEnv("ENV", "production"),
		LogLevel:       getEnv("LOG_LEVEL", "info"),
		ServiceVersion: getEnv("SERVICE_VERSION", "1.0.0"),

		GRPCPort:    getEnv("TELEPHONY_ACTION_SERVICE_GRPC_PORT", "13111"),
		HttpPort:    getEnv("TELEPHONY_ACTION_SERVICE_HTTP_PORT", "13110"),
		MetricsPort: getEnv("TELEPHONY_ACTION_SERVICE_METRICS_PORT", "13112"),

		CertPath: getEnvOrFail("TELEPHONY_ACTION_SERVICE_CERT_PATH"),
		KeyPath:  getEnvOrFail("TELEPHONY_ACTION_SERVICE_KEY_PATH"),
		CaPath:   getEnvOrFail("GRPC_TLS_CA_PATH"),

		MediaServiceURL:  fixUrl(getEnv("MEDIA_SERVICE_TARGET_GRPC_URL", "media-service:13031")),
		SttGatewayURL:    fixUrl(getEnv("STT_GATEWAY_TARGET_GRPC_URL", "stt-gateway-service:15021")),
		TtsGatewayURL:    fixUrl(getEnv("TTS_GATEWAY_TARGET_GRPC_URL", "tts-gateway-service:14011")),
		DialogServiceURL: fixUrl(getEnv("DIALOG_SERVICE_TARGET_GRPC_URL", "dialog-service:12061")),
		SipSignalingURL:  fixUrl(getEnv("SIP_SIGNALING_TARGET_GRPC_URL", "sip-signaling-service:13021")),
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvOrFail(key string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	log.Fatal().Str("var", key).Msg("Kritik ortam değişkeni eksik!")
	return ""
}

// fixUrl: gRPC bağlantıları için http/https ön ekini temizler (Go gRPC client için gerekli)
func fixUrl(url string) string {
	if strings.Contains(url, "://") {
		parts := strings.Split(url, "://")
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return url
}
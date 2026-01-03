.PHONY: up down logs test build clean

up: ## Servisi başlatır
	docker compose up --build -d

down: ## Servisi durdurur
	docker compose down

logs: ## Logları izler
	docker compose logs -f

build: ## Yerel derleme (Go yüklüyse)
	go build -o bin/telephony-action-service ./cmd/telephony-action-service

test: ## Birim testleri çalıştırır
	go test ./...

clean: ## Temizlik
	rm -rf bin/
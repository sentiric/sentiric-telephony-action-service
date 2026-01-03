# 1. Yeniden Derle ve Başlat (Cache temizleyerek)
docker compose up --build --force-recreate -d

# 2. Logları kontrol et (Artık DEBUG görmelisiniz)
docker compose logs -f telephony-action-service

# 3. İstemci Testi (Go client ile)
go run debug_client.go
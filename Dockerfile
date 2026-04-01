# --- STAGE 1: Builder ---
FROM rust:1.93-slim-bookworm AS builder

RUN apt-get update && \
    apt-get install -y git pkg-config libssl-dev protobuf-compiler curl && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY . .
RUN cargo build --release --bin sentiric-telephony-action-service

# --- STAGE 2: Final ---
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates netcat-openbsd curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/target/release/sentiric-telephony-action-service .

RUN useradd -m -u 1001 appuser
USER appuser

ENTRYPOINT ["./sentiric-telephony-action-service"]
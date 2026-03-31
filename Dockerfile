# --- Builder Stage ---
FROM rust:1.93-slim-bookworm AS builder

RUN apt-get update && apt-get install -y \
    pkg-config libssl-dev protobuf-compiler git curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main() {}" > src/main.rs && cargo build --release && rm -rf src

COPY . .
RUN CGO_ENABLED=0 cargo build --release

# --- Final Stage ---
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates netcat-openbsd curl && rm -rf /var/lib/apt/lists/*

RUN addgroup --system --gid 1001 appgroup && \
    adduser --system --no-create-home --uid 1001 --ingroup appgroup appuser

WORKDIR /app
COPY --from=builder /app/target/release/sentiric-telephony-action-service .
RUN chown appuser:appgroup ./sentiric-telephony-action-service

USER appuser
EXPOSE 13050 13051

ENTRYPOINT ["./sentiric-telephony-action-service"]
.PHONY: all fmt clippy build run test

all: fmt clippy test build

fmt:
	cargo fmt --all

clippy:
	cargo clippy --all-targets --all-features -- -D warnings

build:
	cargo build --release

run:
	cargo run --release

test:
	cargo test --all-features
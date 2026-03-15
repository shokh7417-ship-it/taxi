.PHONY: migrate-up migrate-down

# Load .env if present (optional)
-include .env
export

# Default DATABASE_URL for local Postgres
DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/taxi?sslmode=disable

migrate-up:
	go run ./cmd/migrate -up

migrate-down:
	go run ./cmd/migrate -down

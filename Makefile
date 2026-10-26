.PHONY: build run test clean docker-build docker-run docker-push migrate vet fmt help

# Variables
BINARY_NAME=flac-server
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags="-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

# Default target
all: build

# Build binary
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/server

# Build for local development (current OS/ARCH)
build-local:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/server

# Run locally
run: build-local
	./$(BINARY_NAME)

# Run with config
run-config: build-local
	CONFIG_PATH=config.yaml ./$(BINARY_NAME)

# Test
test:
	go test -v -race -count=1 ./...

test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# Lint & format
vet:
	go vet ./...

fmt:
	go fmt ./...

# Generate
generate:
	go generate ./...

# Clean
clean:
	rm -f $(BINARY_NAME)
	rm -f coverage.out
	docker rmi $(BINARY_NAME):latest 2>/dev/null || true

# Docker
docker-build:
	docker build -t $(BINARY_NAME):latest .

docker-build-amd64:
	docker build --platform linux/amd64 -t $(BINARY_NAME):latest-amd64 .

docker-run: docker-build
	docker-compose up -d

docker-stop:
	docker-compose down

docker-logs:
	docker-compose logs -f

docker-restart: docker-stop docker-run

# Database
migrate:
	go run ./cmd/migrate

# Install tools
tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install golang.org/x/tools/cmd/goimports@latest

# Help
help:
	@echo "Available targets:"
	@echo "  build         - Build ARM64 binary"
	@echo "  build-local   - Build for current platform"
	@echo "  run           - Build and run locally"
	@echo "  test          - Run tests"
	@echo "  vet           - Run go vet"
	@echo "  fmt           - Format code"
	@echo "  clean         - Clean build artifacts"
	@echo "  docker-build  - Build Docker image"
	@echo "  docker-run    - Build and run via docker-compose"
	@echo "  docker-stop   - Stop docker-compose"
	@echo "  docker-logs   - Follow docker-compose logs"
	@echo "  migrate       - Run database migrations"
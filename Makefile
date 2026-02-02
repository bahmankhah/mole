.PHONY: all build run clean test docker-build docker-up docker-down deps

# Variables
BINARY_NAME=crawler
MAIN_FILE=main.go
DOCKER_COMPOSE=docker-compose.yml

# Default target
all: build

# Download dependencies
deps:
	go mod download
	go mod tidy

# Build the application
build: deps
	go build -o $(BINARY_NAME) $(MAIN_FILE)

# Run the application
run: build
	./$(BINARY_NAME)

# Run with hot reload (requires air)
dev:
	air

# Clean build artifacts
clean:
	rm -f $(BINARY_NAME)
	go clean

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Build Docker image
docker-build:
	docker build -t resolver-crawler .

# Start services with Docker Compose
docker-up:
	docker-compose -f $(DOCKER_COMPOSE) up -d

# Stop services
docker-down:
	docker-compose -f $(DOCKER_COMPOSE) down

# View logs
docker-logs:
	docker-compose -f $(DOCKER_COMPOSE) logs -f

# Restart services
docker-restart: docker-down docker-up

# Setup MySQL locally (without Docker)
setup-mysql:
	@echo "Creating database..."
	mysql -u root -p -e "CREATE DATABASE IF NOT EXISTS crawler_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
	@echo "Database created!"

# Run database migrations (for development)
migrate:
	go run $(MAIN_FILE) migrate

# Lint code
lint:
	golangci-lint run

# Format code
fmt:
	go fmt ./...

# Help
help:
	@echo "Available targets:"
	@echo "  deps          - Download dependencies"
	@echo "  build         - Build the application"
	@echo "  run           - Build and run the application"
	@echo "  dev           - Run with hot reload (requires air)"
	@echo "  clean         - Clean build artifacts"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage"
	@echo "  docker-build  - Build Docker image"
	@echo "  docker-up     - Start services with Docker Compose"
	@echo "  docker-down   - Stop services"
	@echo "  docker-logs   - View Docker logs"
	@echo "  setup-mysql   - Setup MySQL database locally"
	@echo "  lint          - Lint code"
	@echo "  fmt           - Format code"

.PHONY: all build run serve worker test test-integration lint clean deps docker-build docker-up docker-down docker-logs

BINARY  := bin/mole
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -X github.com/resolver/crawler/cmd.version=$(VERSION) -X github.com/resolver/crawler/cmd.commit=$(COMMIT)
export GOTOOLCHAIN := local

all: build

deps:
	go mod download

# Static binary: the control node hands it to joining nodes, which may have an older libc.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Web UI + API + crawl worker
run serve: build
	./$(BINARY) serve

# Crawl worker only (e.g. make worker REGION=ir)
worker: build
	./$(BINARY) worker $(if $(REGION),--region $(REGION))

test:
	go test ./...

# Live network and real-browser tests
test-integration:
	MOLE_NET_TESTS=1 MOLE_BROWSER_TESTS=1 go test -count=1 ./netx ./modules

lint:
	gofmt -l . && go vet ./...

clean:
	rm -rf bin coverage.out coverage.html
	go clean

docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t mole:$(VERSION) -t mole:latest .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

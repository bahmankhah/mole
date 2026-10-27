# ── Build ────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder
WORKDIR /build
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X github.com/resolver/crawler/cmd.version=${VERSION} -X github.com/resolver/crawler/cmd.commit=${COMMIT}" \
    -o /out/mole .

# ── Runtime: Go binary + Python/Playwright for the headless browser ─────
FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PIP_NO_CACHE_DIR=1 TZ=Asia/Tehran
WORKDIR /app

# Headless browser support. Set INSTALL_ALL_PLUGINS=true to also install the
# semantic-search and stemming dependencies (several GB).
ARG INSTALL_ALL_PLUGINS=false
COPY plugins/requirements.txt /tmp/requirements.txt
RUN pip install playwright requests \
 && python -m playwright install --with-deps chromium \
 && if [ "$INSTALL_ALL_PLUGINS" = "true" ]; then pip install -r /tmp/requirements.txt; fi \
 && apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/mole /usr/local/bin/mole
COPY templates ./templates
COPY static ./static
COPY plugins ./plugins
COPY config.yaml ./config.yaml

RUN useradd --create-home --uid 10001 mole && mkdir -p /app/data && chown -R mole /app
USER mole

EXPOSE 5050
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:5050/healthz').status==200 else 1)"
ENTRYPOINT ["mole"]
CMD ["serve"]

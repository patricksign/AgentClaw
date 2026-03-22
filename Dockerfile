# ─── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

# gcc needed for go-sqlite3 (CGO)
RUN apk add --no-cache gcc musl-dev git

WORKDIR /src

# Cache dependencies first
COPY go.mod go.sum* ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /bin/agentclawd \
    ./cmd/agentclawd

# ─── Stage 2: Runtime ────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget

# Non-root user for security sandbox
RUN addgroup -S agentclaw && adduser -S agentclaw -G agentclaw

WORKDIR /app

COPY --from=builder /bin/agentclawd .

# Static dashboard frontend
COPY static/ /app/static/

# Pricing config — can be overridden via volume mount
COPY pricing/agent-pricing.json /app/pricing/agent-pricing.json

# Agent config — can be overridden via volume mount
COPY config/agents.json /app/config/agents.json

# Default dirs — overridden by volume mounts in compose
RUN mkdir -p /app/data /app/memory/agents /app/state/scope /app/state/old /app/state/resolved \
    && chown -R agentclaw:agentclaw /app

USER agentclaw

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/app/agentclawd"]

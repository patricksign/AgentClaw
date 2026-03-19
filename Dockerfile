# ─── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

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
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/agentclawd .

# Default dirs — overridden by volume mounts in compose
RUN mkdir -p /app/data /app/memory/agents /app/state/scope /app/state/old /app/state/resolved /app/static

EXPOSE 8080

ENTRYPOINT ["/app/agentclawd"]

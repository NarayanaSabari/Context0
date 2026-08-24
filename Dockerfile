# Stage 1: Build
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/kora-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/kora-consolidate ./cmd/consolidate

# Stage 2: Runtime (alpine for healthcheck support)
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget
RUN adduser -D -u 1000 kora

COPY --from=builder /bin/kora-server /usr/local/bin/kora-server
COPY --from=builder /bin/kora-consolidate /usr/local/bin/kora-consolidate

USER kora

EXPOSE 50051 8080

ENTRYPOINT ["kora-server"]

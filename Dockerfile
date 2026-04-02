# Stage 1: Build
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/context0-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/context0-consolidate ./cmd/consolidate

# Stage 2: Runtime (alpine for healthcheck support)
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget
RUN adduser -D -u 1000 context0

COPY --from=builder /bin/context0-server /usr/local/bin/context0-server
COPY --from=builder /bin/context0-consolidate /usr/local/bin/context0-consolidate

USER context0

EXPOSE 50051 8080

ENTRYPOINT ["context0-server"]

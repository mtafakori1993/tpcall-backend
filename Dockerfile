# ---- Build stage ----
FROM golang:1.22-alpine AS builder

WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/tpcall-server ./cmd/server

# ---- Runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 app

WORKDIR /app
COPY --from=builder /out/tpcall-server /app/tpcall-server
COPY --from=builder /src/migrations /app/migrations

USER app

EXPOSE 9090

ENTRYPOINT ["/app/tpcall-server"]

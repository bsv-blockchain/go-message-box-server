# Base images are pinned to their multi-arch index digests (not a per-platform
# manifest digest), so the same pin resolves to linux/amd64 and linux/arm64.
# Dependabot updates the tag and digest together.
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# nobdk selects go-wallet-toolbox's pure-Go signature backend. The default
# backend links go-sdk/bitcoin-sv gobdk, a prebuilt glibc C++ library that
# does not link on Alpine (musl). CGO stays on for mattn/go-sqlite3.
RUN CGO_ENABLED=1 go build -tags nobdk -o messagebox-server ./cmd/server

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/messagebox-server .

EXPOSE 3000

CMD ["./messagebox-server"]

# Base images are pinned to their multi-arch index digests (not a per-platform
# manifest digest), so the same pin resolves to linux/amd64 and linux/arm64.
# Dependabot updates the tag and digest together.
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# nobdk selects go-wallet-toolbox's pure-Go signature backend. The default
# backend links go-sdk/bitcoin-sv gobdk, a prebuilt glibc C++ library that
# does not link on Alpine (musl). CGO stays on for mattn/go-sqlite3.
RUN CGO_ENABLED=1 go build -tags nobdk -o messagebox-server ./cmd/server

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/messagebox-server .

EXPOSE 3000

CMD ["./messagebox-server"]

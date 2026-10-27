# Base images are pinned to their multi-arch index digests (not a per-platform
# manifest digest), so the same pin resolves to linux/amd64 and linux/arm64.
# Dependabot updates the tag and digest together.
FROM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# nobdk selects go-wallet-toolbox's pure-Go signature backend. The default
# backend links go-sdk/bitcoin-sv gobdk, a prebuilt glibc C++ library that
# does not link on Alpine (musl). CGO stays on for mattn/go-sqlite3.
RUN CGO_ENABLED=1 go build -tags nobdk -o messagebox-server ./cmd/server

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/messagebox-server .

EXPOSE 3000

CMD ["./messagebox-server"]

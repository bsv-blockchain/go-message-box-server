FROM golang:1.27-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# nobdk selects go-wallet-toolbox's pure-Go signature backend. The default
# backend links go-sdk/bitcoin-sv gobdk, a prebuilt glibc C++ library that
# does not link on Alpine (musl). CGO stays on for mattn/go-sqlite3.
RUN CGO_ENABLED=1 go build -tags nobdk -o messagebox-server ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/messagebox-server .

EXPOSE 3000

CMD ["./messagebox-server"]

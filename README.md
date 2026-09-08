# Go MessageBox Server

A Go reimplementation of the [BSV MessageBox Server](https://github.com/bsv-blockchain/message-box-server), providing peer-to-peer message storage and delivery with BRC-31 (Authrite) authentication and BRC-29 payment support.

## API Endpoints

All endpoints require BRC-31 authentication via the `go-bsv-middleware` auth middleware.

| Method | Path | Description |
|--------|------|-------------|
| POST | `/sendMessage` | Send a message to one or more recipients' message boxes |
| POST | `/listMessages` | List messages from a specific message box |
| POST | `/acknowledgeMessage` | Acknowledge (delete) received messages |
| POST | `/registerDevice` | Register device for FCM push notifications |
| GET | `/devices` | List registered devices |
| POST | `/permissions/set` | Set message permission (block, allow, or require payment) |
| GET | `/permissions/get` | Get permission for a sender/box combination |
| GET | `/permissions/list` | List all permissions with pagination |
| GET | `/permissions/quote` | Get delivery price quote for recipient(s) |

## Architecture

```
cmd/server/         - Entry point, storage backend selection, middleware wiring, CORS
pkg/
  config/           - Environment variable loading
  handlers/         - HTTP route handlers and fee/notification policy
  storage/          - Backend-neutral persistence interface and domain types
    sqlstore/       - SQLite and PostgreSQL implementation
    mongostore/     - MongoDB implementation
    storagetest/    - Conformance suite every implementation must pass
internal/
  firebase/         - FCM push notification delivery
  logger/           - Toggleable structured logger
test-client/        - Jest integration tests (TypeScript)
```

`pkg/config`, `pkg/handlers` and `pkg/storage` are exported so the server can be
embedded. An embedder that wants its own persistence implements
`storage.Store` and passes it to `handlers.NewServer` — running
`storagetest.RunStoreTests` against it is the way to know it is correct.

### Key Dependencies

| Node.js Original | Go Equivalent |
|---|---|
| `@bsv/sdk` | `github.com/bsv-blockchain/go-sdk` |
| `@bsv/auth-express-middleware` | `github.com/bsv-blockchain/go-bsv-middleware` (auth) |
| `@bsv/payment-express-middleware` | `github.com/bsv-blockchain/go-bsv-middleware` (payment) |
| Express.js | `net/http` (Go 1.22+ routing) |
| Knex + MySQL | `pkg/storage` over `database/sql` (SQLite/PostgreSQL) or MongoDB |
| — | `github.com/bsv-blockchain/go-wallet-toolbox` (wallet) |

## Storage

Persistence sits behind the `storage.Store` interface in `pkg/storage`. Pick a
backend with `STORAGE_BACKEND`:

| Backend | Value | Configured by |
|---|---|---|
| SQLite (default) | `sql` | `DB_DRIVER=sqlite3`, `DB_SOURCE=messagebox.db` |
| PostgreSQL | `sql` | `DB_DRIVER=postgres`, `DB_SOURCE=postgres://...` |
| MongoDB | `mongo` | `MONGO_URI`, `MONGO_DATABASE` |

The schema is created on startup and the data it holds is the same either way:

- **message boxes** — Named message boxes per identity key
- **messages** — Stored messages with sender, recipient, body
- **message permissions** — Per-sender or box-wide fee/block settings
- **server fees** — Server-level delivery fees per box type
- **device registrations** — FCM tokens for push notifications

The SQL backend keeps the original relational shape, including the `messageBox`
table and its integer foreign key; MongoDB stores the box name on the message
instead. Neither detail is visible through the interface or the HTTP API.

### Testing a backend

`pkg/storage/storagetest` holds the conformance suite. SQLite runs it on every
`go test ./...`; PostgreSQL and MongoDB run it when pointed at a database:

```bash
POSTGRES_TEST_DSN='postgres://user:pass@localhost:5432/messagebox_test?sslmode=disable' \
  go test ./pkg/storage/sqlstore/
MONGO_TEST_URI='mongodb://localhost:27017' go test ./pkg/storage/mongostore/
```

Both drop their tables/collections first, so point them at a throwaway database.

## Wallet

Uses `go-wallet-toolbox` with local SQLite storage for production wallet functionality. The wallet provides:

- BRC-31 authentication via `go-bsv-middleware`
- BRC-29 payment processing
- Identity key derivation from `SERVER_PRIVATE_KEY`
- Automatic storage migration on startup

Network is configurable via `BSV_NETWORK` (mainnet/testnet).

## Differences from the Original

1. **Database**: SQLite instead of MySQL by default, with PostgreSQL and MongoDB also supported (see [Storage](#storage))
2. **WebSockets**: Not yet implemented (HTTP API is fully compatible)

## Quick Start

```bash
cp .env.example .env
# Edit .env with your SERVER_PRIVATE_KEY (64-char hex)

go build -o messagebox-server ./cmd/server
./messagebox-server
```

## Docker

```bash
docker build -t messagebox-server .
docker run -p 8080:8080 -e SERVER_PRIVATE_KEY=your-hex-key messagebox-server
```

## Testing

### Go unit tests

```bash
go test ./...
```

### Jest integration tests

The `test-client/` directory contains 26 integration tests against a running
server: `messagebox.test.ts` drives the message lifecycle through
`@bsv/message-box-client`, and `devices-permissions.test.ts` drives the device,
permission and quote endpoints over `AuthFetch` directly (the client does not
cover those routes). Both use an `@bsv/sdk` `ProtoWallet` for BRC-31 auth.

Point them at a server on any backend — the responses are identical.

```bash
# Terminal 1: Start the server
SERVER_PRIVATE_KEY=$(openssl rand -hex 32) go run ./cmd/server

# Terminal 2: Run tests
cd test-client
npm install
npx jest --verbose
```

**Tests cover:**
- Send message (plaintext, JSON body, send-to-self)
- List messages (populated box, empty box)
- Acknowledge messages (valid, already-acknowledged, nonexistent)
- Register and list devices (upsert on re-registration, platform validation)
- Set, get and list permissions (box-wide vs sender-specific, order, paging, filtering)
- Delivery quotes
- Input validation (empty recipient, empty body)
- Multiple messages in the same box

All tests use real BRC-31 AuthFetch authentication against the running server.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PRIVATE_KEY` | (required) | Hex-encoded private key for server identity |
| `NODE_ENV` | `development` | Environment (`development`, `production`) |
| `PORT` | `8080` (dev) / `3000` (prod) | HTTP listen port |
| `ROUTING_PREFIX` | `` | URL prefix for all routes |
| `STORAGE_BACKEND` | `sql` | Storage backend (`sql`, `mongo`) |
| `DB_DRIVER` | `sqlite3` | SQL driver (`sqlite3`, `postgres`) |
| `DB_SOURCE` | `messagebox.db` | SQL connection string or file path |
| `MONGO_URI` | `mongodb://localhost:27017` | MongoDB connection URI |
| `MONGO_DATABASE` | `messagebox` | MongoDB database name |
| `BSV_NETWORK` | `mainnet` | BSV network (`mainnet`, `testnet`) |
| `ENABLE_WEBSOCKETS` | `true` | Enable WebSocket support (not yet implemented) |

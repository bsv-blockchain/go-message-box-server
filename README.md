# Go MessageBox Server

A Go reimplementation of the [BSV MessageBox Server](https://github.com/bsv-blockchain/message-box-server), providing peer-to-peer message storage and delivery with BRC-31 (Authrite) authentication and BRC-29 payment support.

## API Endpoints

All endpoints below require BRC-31 authentication via the `go-bsv-middleware`
auth middleware. The optional [paymail profile lookup](#paymail-profile-lookup)
adds public, unauthenticated routes alongside them.

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
  handles/          - Handle validation, look-alike skeletons, BRFC ids
  profilecert/      - Public profile certificate parsing and verification
  storage/          - Backend-neutral persistence interface and domain types
    sqlstore/       - SQLite and PostgreSQL implementation
    mongostore/     - MongoDB implementation, plus the handle registry
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

With Docker Compose:

```bash
docker compose up                                           # PostgreSQL
STORAGE_BACKEND=mongo docker compose --profile mongo up      # MongoDB
```

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

## Paymail profile lookup

An optional handle registry that maps `handle@domain` to an identity key and
serves a self-signed BRC-52 profile certificate for it, discovered the paymail
way (`_bsvalias._tcp` SRV record plus `/.well-known/bsvalias`). The server is a
registry and a cache only — it cannot forge a profile, and clients must verify
every certificate they receive; see
[the design spec](docs/specs/2026-09-18-paymail-profile-lookup-design.md).

The feature is off until `PAYMAIL_DOMAIN` is set, and it **requires
`STORAGE_BACKEND=mongo`**: with `PAYMAIL_DOMAIN` set on any other backend the
server refuses to start rather than come up without the registry. Production
points `MONGO_URI` at a MongoDB Atlas `mongodb+srv://…` URI; local development
uses MongoDB Community:

```bash
docker compose --profile mongo up -d mongo
```

### Configuration

| Env | Meaning | Default |
|---|---|---|
| `PAYMAIL_DOMAIN` | Domain served, e.g. `example.com`. Unset → feature off, no routes mounted | unset |
| `PAYMAIL_HOST` | Public base URL used in capability templates, e.g. `https://mb.example.com` | required when domain set |
| `HANDLE_COOLDOWN_DAYS` | Days before a released handle may be claimed by a different key | `30` |
| `ADMIN_IDENTITY_KEYS` | Comma list of compressed pubkey hex allowed to call admin routes | empty (admin routes reject all) |
| `LOOKUP_RATE_PER_MIN` | Per-IP cap, shared across all five public routes; exactly `0` **disables** the limiter (it does not block traffic). Any other value that is not a non-negative integer, a negative one included, falls back to the default | `60` |
| `TRUST_PROXY` | `true` → client IP taken from `X-Forwarded-For` instead of the connection (set when behind a load balancer) | `false` |
| `TRUSTED_PROXY_HOPS` | How many proxies of your own sit in front of this process; the client's address is counted that many entries from the right of `X-Forwarded-For`. Only read when `TRUST_PROXY=true`; values below `1` are read as `1` | `1` |
| `CLIENT_IP_HEADER` | Name of a single-valued header a proxy sets to the real client address, e.g. `CF-Connecting-IP` behind Cloudflare. Must be a syntactically valid HTTP header field name or the process refuses to start. **Wins over `TRUST_PROXY`/`TRUSTED_PROXY_HOPS`** whenever the header is present exactly once and its value parses as an IP; otherwise falls back to the `TRUST_PROXY` logic above. See the caveat below | unset |

The rate limiter is in-memory and therefore per-replica: three replicas behind
one load balancer allow three times `LOOKUP_RATE_PER_MIN` between them. Behind a
load balancer every request also arrives from the balancer's own address, so
without `TRUST_PROXY=true` the entire fleet shares a single bucket.

Set `TRUSTED_PROXY_HOPS` to the number of proxies you run in front of the
server — one for a single load balancer, two for a CDN in front of it. Proxies
*append* to `X-Forwarded-For` rather than rewriting it (AWS ALB, Google Cloud
Load Balancing, Cloudflare and nginx's `$proxy_add_x_forwarded_for` all do), so
the leftmost entry is whatever the client chose to send and only the entry your
own nearest proxies appended means anything. Counting from the right is what
stops a client picking a fresh bucket for every request, or spending another
client's. A header with fewer entries than there are hops falls back to the
connection address, which over-counts rather than letting traffic past — so a
hop count that is too high is safe, and one that is too low is not.

`TRUST_PROXY` assumes the proxy in front of the process *appends* to
`X-Forwarded-For`. Some proxies don't: Traefik without
`forwardedHeaders.trustedIPs` configured *replaces* the header with the
address it saw the connection from, so counting hops from the right just
recovers the proxy's own address no matter how it's set. If that proxy (or
something upstream of it, e.g. Cloudflare) instead forwards a dedicated
single-value header untouched, point `CLIENT_IP_HEADER` at it —
`CF-Connecting-IP` for Cloudflare. When both are set, `CLIENT_IP_HEADER` wins
whenever it is present exactly once and parses as an IP; `TRUST_PROXY` is only
consulted as the fallback.

**Security caveat:** `CLIENT_IP_HEADER` is only trustworthy when the origin is
reachable *exclusively* through the proxy that sets it. Anything that can reach
the process directly can set the header itself, and because every distinct value
is its own bucket, a caller sending a fresh value on each request gets a fresh
bucket each time: it is not rate limited **at all**, and it adds an entry per
request to the limiter's in-memory map until the minute rolls. That is *weaker*
than leaving the variable unset, where the same traffic keys on one connection
address and is capped at `LOOKUP_RATE_PER_MIN`. So set it only once the origin
accepts nothing but the proxy — Cloudflare's published IP ranges, or a tunnel
with no other route in. A common way for this not to hold: an ingress
controller whose load balancer is internet-facing *and* fronts a Cloudflare
tunnel, so the same route answers requests that never touched Cloudflare. Close
that first, for instance with an ingress-level source allow-list that admits
only the tunnel's addresses on this host. The header bounds fairness between clients that really do arrive through the proxy;
it is **not** an authentication signal and must never be used as one.

### Routes

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/.well-known/bsvalias` | none | Capability discovery document |
| PUT | `/api/handle` | the certificate itself | Register, update or release a handle |
| GET | `/api/handle/{query}` | none | Search, ranked, max 10 (capability `0ace65da5987`) |
| GET | `/api/handle/available/{handle}` | none | Advisory availability check |
| GET | `/api/identityKey/{pubkey}` | none | Reverse lookup (capability `43dcf83ddc5f`) |
| POST | `/admin/handle/release` | BRC-104 + `ADMIN_IDENTITY_KEYS` | Operator release after lost keys |

The public routes carry no session auth by design: a `PUT` is authorised by the
certificate in its body, a statement the identity key signed naming the handle.
They sit outside the auth and payment middleware and are mounted at fixed paths;
`ROUTING_PREFIX` applies to the admin route only.

### DNS

```
_bsvalias._tcp.example.com. 3600 IN SRV 10 10 443 mb.example.com.
```

The zone must be DNSSEC-signed — clients reject an unsigned SRV answer, because
an attacker who can forge it chooses which server answers for the domain.
`PAYMAIL_HOST` must be the HTTPS origin the SRV target serves (`https://mb.example.com`
above): it is what the capability document hands clients as the base of every
lookup URL, so a client that took the trouble to reach a signed SRV target may
refuse a plain-http URL. The server logs a warning at startup when
`PAYMAIL_HOST` is neither an `https://` origin nor loopback. Without an SRV
record clients fall back to `https://example.com:443/.well-known/bsvalias`.

### Client verification

Normative for anyone implementing a resolver:

1. DNSSEC + SRV `_bsvalias._tcp.<domain>` → host; GET well-known; find capability.
2. For every certificate received: check `type`, `subject == certifier`,
   signature, `fields.paymail` ends with `@<queried domain>`, no `released`.
3. Search results are suggestions. Show full `fields.paymail`; user selects.
   Never auto-select `result[0]`. If the user entered a complete
   `handle@domain`, require `fields.paymail` to equal it exactly.
4. Reverse lookup: additionally require `subject ==` queried key; optionally
   forward-resolve `fields.paymail` and compare.

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

Point them at a server on any backend. The responses are byte-identical, which
is a property the conformance suite enforces rather than a hope: timestamps are
written UTC so a non-UTC host cannot skew them, and the permission list is
ordered bytewise so PostgreSQL's libc collation cannot reorder a page.

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

The optional paymail profile lookup reads six more, listed under
[Paymail profile lookup](#paymail-profile-lookup).

# Paymail Profile Lookup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Host a paymail-discoverable handle registry in go-message-box-server that maps `handle@domain` ⇄ identity key, serving self-signed BRC-52 profile certificates.

**Architecture:** Pure-function packages (`pkg/handles`, `pkg/profilecert`) validate input; a new `storage.HandleStore` contract (mongostore + the handlers fake, both under a shared conformance suite) gives first-come-first-served via unique indexes and single-document conditional writes. **Storage target is MongoDB Atlas; local dev/test uses MongoDB Community (docker `mongo` image). SQL is not targeted: sqlstore does not implement `HandleStore`, `HandleStore` is NOT embedded in `storage.Store`, and the lookup feature requires `STORAGE_BACKEND=mongo`.** Public unauthenticated handlers mount on `rootMux` (cert-as-auth `PUT`, search, reverse lookup, availability, well-known) plus one BRC-104 admin release route.

**Tech Stack:** Go 1.25, stdlib `net/http` ServeMux, mongo-driver v2 (Atlas-compatible: no transactions, no `$where`, no server-side JS), go-sdk v1.2.18 `auth/certificates`, stdlib `testing` (no testify).

**Spec:** `docs/specs/2026-09-18-paymail-profile-lookup-design.md` — read it first.

## Global Constraints

- No new module dependencies. No go-paymail import.
- BRFC ids: lookup `0ace65da5987`, reverse `43dcf83ddc5f` (title + `Deggen` + `v1.0.0`).
- Certificate type constant: `SbatVXXssDW3AO0J9bxIljkHGbPBGCVAXg94gXFf0cE=` (base64 SHA-256 of `public profile lookup`).
- Handle regex `^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$`; reserved list and skeleton folds exactly as in spec.
- Limits: ≤ 32 fields, each value ≤ 1024 bytes, body ≤ 16384 bytes, search limit 10, min query length 2, substring tier only when `len(q) ≥ 3`.
- Feature is off (no routes mounted) when `PAYMAIL_DOMAIN` unset.
- Storage: MongoDB only. No multi-document transactions, no check-then-insert; documents never deleted; mongostore and the fake both pass `storagetest.RunHandleStoreTests`. Do not touch `pkg/storage/sqlstore`. Do not add `HandleStore` to the `storage.Store` interface.
- Mongo tests MUST actually run (not skip). A community MongoDB is listening on `mongodb://localhost:27017`. Always run mongostore tests as:
  `MONGO_TEST_URI=mongodb://localhost:27017 MONGO_TEST_DATABASE=messagebox_lookup_test go test ./pkg/storage/mongostore/`
  (the suite drops that database — never point it at any other name). If it is unreachable: `docker compose --profile mongo up -d mongo`.
- Tests: stdlib `testing` only. Errors via existing `writeError(w, status, code, description)`.
- Run from repo root. Full check before each commit: `go build ./... && go test ./...`.
- Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.

## File Structure

| File | Responsibility |
|---|---|
| `pkg/handles/handles.go` | `Validate`, `Skeleton`, reserved list, BRFC constants + `BRFCID` |
| `pkg/profilecert/profilecert.go` | Parse + verify certificate rules → `Profile` |
| `pkg/storage/handles.go` | `HandleStore` contract, types, errors, `DiagnoseClaim`, `DiagnoseRelease` |
| `pkg/storage/storagetest/handles.go` | Conformance tests for `HandleStore` |
| `pkg/handlers/fakestore_handles_test.go` | fake implementation |
| `pkg/storage/mongostore/handles.go` | Mongo implementation (+ indexes in `mongostore.go`) |
| `pkg/config/config.go` | new env vars |
| `pkg/handlers/ratelimit.go` | per-IP fixed-window limiter middleware |
| `pkg/handlers/lookup.go` | well-known, PUT, search, reverse, available |
| `pkg/handlers/admin_handle.go` | admin release |
| `cmd/server/main.go` | wiring |
| `README.md` | DNS + env docs |

---

### Task 1: `pkg/handles` — validation, skeleton, BRFC ids

**Files:**
- Create: `pkg/handles/handles.go`
- Test: `pkg/handles/handles_test.go`

**Interfaces:**
- Produces:
  - `const BRFCLookup = "0ace65da5987"`, `const BRFCReverseLookup = "43dcf83ddc5f"`
  - `func BRFCID(title, author, version string) string`
  - `var ErrInvalidHandle, ErrReservedHandle error`
  - `func Validate(handle string) error`
  - `func Skeleton(s string) string` (accepts any lowercase string, incl. partial queries; may return `""`)

- [ ] **Step 1: Write the failing test**

```go
package handles

import (
	"errors"
	"testing"
)

func TestBRFCID(t *testing.T) {
	if got := BRFCID("public profile lookup", "Deggen", "v1.0.0"); got != BRFCLookup {
		t.Errorf("lookup id = %s, want %s", got, BRFCLookup)
	}
	if got := BRFCID("public profile reverse lookup", "Deggen", "v1.0.0"); got != BRFCReverseLookup {
		t.Errorf("reverse id = %s, want %s", got, BRFCReverseLookup)
	}
	// Vectors from go-paymail's own brfc_test.go.
	if got := BRFCID("BRFC Specifications", "andy (nChain)", "1"); got != "57dd1f54fc67" {
		t.Errorf("paymail vector = %s, want 57dd1f54fc67", got)
	}
	if got := BRFCID("  bsvalias Integration with Simplified Payment Protocol  ", "  andy (nChain)  ", "1"); got != "0036f9b8860f" {
		t.Errorf("trimmed vector = %s, want 0036f9b8860f", got)
	}
}

func TestSkeleton(t *testing.T) {
	cases := map[string]string{
		"deggen":    "degen",
		"d.e-g_gen": "degen",
		"degg3n":    "deg3n",
		"paypa1":    "paypal",
		"paypai":    "paypal",
		"paypal":    "paypal",
		"g00gle":    "gogle",
		"google":    "gogle",
		"rnike":     "mlke",
		"mike":      "mlke",
		"vvill":     "wl",
		"will":      "wl",
		"5am":       "sam",
		"2ed":       "zed",
		"8ob":       "bob",
		"--":        "",
	}
	for in, want := range cases {
		if got := Skeleton(in); got != want {
			t.Errorf("Skeleton(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []string{"deg", "deggen", "a.b-c_d", "abc123", "a23456789012345678901234567890bc"}
	for _, h := range ok {
		if err := Validate(h); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", h, err)
		}
	}
	invalid := []string{"", "ab", ".abc", "abc.", "Deggen", "deg gen", "deg+gen", "dég", "a23456789012345678901234567890bcd"}
	for _, h := range invalid {
		if err := Validate(h); !errors.Is(err, ErrInvalidHandle) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidHandle", h, err)
		}
	}
	reserved := []string{"admin", "adm1n", "a.d.m.i.n", "support", "r00t", "available"}
	for _, h := range reserved {
		if err := Validate(h); !errors.Is(err, ErrReservedHandle) {
			t.Errorf("Validate(%q) = %v, want ErrReservedHandle", h, err)
		}
	}
}
```

The vectors come from `/Users/personal/git/go/go-paymail/brfc_test.go`
(`TestBRFCSpec_Generate`). Do not use ids from `brfc_definintions.go` as
vectors: some published ids (e.g. PayTo `7bd25e5a1fc6`) do not match their own
metadata.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/handles/`
Expected: FAIL — `undefined: BRFCID` etc.

- [ ] **Step 3: Write implementation**

```go
// Package handles holds the pure rules for paymail-style handles: what is a
// valid handle, which handles are reserved, and the skeleton that decides
// whether two handles look alike.
package handles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// BRFC ids of the two capabilities this server publishes. BRFCID derives them.
const (
	BRFCLookup        = "0ace65da5987"
	BRFCReverseLookup = "43dcf83ddc5f"
)

// BRFCID is the paymail BRFC id: double SHA-256 of title+author+version,
// byte-reversed, hex, first 12 characters.
func BRFCID(title, author, version string) string {
	s := strings.TrimSpace(title) + strings.TrimSpace(author) + strings.TrimSpace(version)
	h1 := sha256.Sum256([]byte(s))
	h2 := sha256.Sum256(h1[:])
	for i, j := 0, len(h2)-1; i < j; i, j = i+1, j-1 {
		h2[i], h2[j] = h2[j], h2[i]
	}
	return hex.EncodeToString(h2[:])[:12]
}

var (
	ErrInvalidHandle  = errors.New("invalid handle")
	ErrReservedHandle = errors.New("reserved handle")
)

var handleRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$`)

var reserved = []string{
	"admin", "administrator", "root", "support", "help", "postmaster", "abuse",
	"security", "info", "available", "api", "bsvalias", "system", "official", "noreply",
}

var reservedSkeletons = func() map[string]bool {
	m := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		m[Skeleton(r)] = true
	}
	return m
}()

// Validate reports whether handle may be registered at all. It does not look
// at what is already registered.
func Validate(handle string) error {
	if !handleRE.MatchString(handle) {
		return ErrInvalidHandle
	}
	if reservedSkeletons[Skeleton(handle)] {
		return ErrReservedHandle
	}
	return nil
}

var (
	separators = strings.NewReplacer(".", "", "_", "", "-", "")
	multiFold  = strings.NewReplacer("rn", "m", "vv", "w")
	singleFold = strings.NewReplacer("0", "o", "1", "l", "i", "l", "5", "s", "2", "z", "8", "b")
)

// Skeleton folds s to the form under which look-alike handles collide.
func Skeleton(s string) string {
	s = strings.ToLower(s)
	s = separators.Replace(s)
	s = multiFold.Replace(s)
	s = singleFold.Replace(s)
	var b strings.Builder
	var last rune = -1
	for _, r := range s {
		if r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/handles/`
Expected: PASS. (Expectations were pre-computed with the spec's fold order:
strip → multi → single → collapse. Do not change the order to satisfy a test.)

- [ ] **Step 5: Commit**

```bash
git add pkg/handles && git commit -m "feat(handles): handle validation, skeleton and BRFC ids"
```

---

### Task 2: `pkg/profilecert` — certificate rules

**Files:**
- Create: `pkg/profilecert/profilecert.go`
- Test: `pkg/profilecert/profilecert_test.go`
- Create: `pkg/profilecert/certtest/certtest.go` (test helper reused by handler tests)

**Interfaces:**
- Consumes: go-sdk `auth/certificates`, `wallet`, `primitives/ec`, `transaction`.
- Produces:
  - `const Type = "SbatVXXssDW3AO0J9bxIljkHGbPBGCVAXg94gXFf0cE="`, `const MaxBody = 16384`
  - `var ErrInvalid, ErrWrongDomain error`
  - `type Profile struct { Handle, Paymail, IdentityKey, SerialNumber, JSON string; IssuedAt time.Time; Released bool }`
  - `func Parse(ctx context.Context, body []byte, domain string) (*Profile, error)` — `JSON` is the canonical re-marshalled certificate; `IssuedAt` truncated to milliseconds UTC. Does **not** call `handles.Validate`.
  - `certtest.New(t, key *ec.PrivateKey, fields map[string]string) []byte` — signed self-cert JSON with zero outpoint, random serial.
  - `certtest.Profile(t, key, handle, domain string, issuedAt time.Time, extra map[string]string) []byte`

- [ ] **Step 1: Write the test helper**

```go
// Package certtest builds signed profile certificates for tests.
package certtest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth/certificates"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

const profileType = "SbatVXXssDW3AO0J9bxIljkHGbPBGCVAXg94gXFf0cE="

// ZeroOutpoint is the only revocation outpoint a profile certificate may carry.
const ZeroOutpoint = "0000000000000000000000000000000000000000000000000000000000000000.0"

// Build returns an unsigned self-certificate the caller may tamper with before Sign.
func Build(t *testing.T, key *ec.PrivateKey, fields map[string]string) *certificates.Certificate {
	t.Helper()
	serial := make([]byte, 32)
	if _, err := rand.Read(serial); err != nil {
		t.Fatal(err)
	}
	op, err := transaction.OutpointFromString(ZeroOutpoint)
	if err != nil {
		t.Fatal(err)
	}
	f := map[wallet.CertificateFieldNameUnder50Bytes]wallet.StringBase64{}
	for k, v := range fields {
		f[wallet.CertificateFieldNameUnder50Bytes(k)] = wallet.StringBase64(v)
	}
	pub := key.PubKey()
	return certificates.NewCertificate(
		wallet.StringBase64(profileType),
		wallet.StringBase64(base64.StdEncoding.EncodeToString(serial)),
		*pub, *pub, op, f, nil,
	)
}

// Sign signs c with key and returns its JSON.
func Sign(t *testing.T, key *ec.PrivateKey, c *certificates.Certificate) []byte {
	t.Helper()
	w, err := wallet.NewCompletedProtoWallet(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Sign(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// New returns signed certificate JSON with the given fields.
func New(t *testing.T, key *ec.PrivateKey, fields map[string]string) []byte {
	t.Helper()
	return Sign(t, key, Build(t, key, fields))
}

// Profile returns a valid signed profile for handle@domain.
func Profile(t *testing.T, key *ec.PrivateKey, handle, domain string, issuedAt time.Time, extra map[string]string) []byte {
	t.Helper()
	fields := map[string]string{
		"paymail":  handle + "@" + domain,
		"issuedAt": issuedAt.UTC().Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		fields[k] = v
	}
	return New(t, key, fields)
}
```

If `wallet.NewCompletedProtoWallet` does not satisfy `certificates.CertifierWallet`
in go-sdk v1.2.18, use `wallet.NewProtoWallet(wallet.ProtoWalletArgs{Type: wallet.ProtoWalletArgsTypePrivateKey, PrivateKey: key})`
(check `wallet/proto_wallet.go` in the module cache for the exact arg names).
`Sign` overwrites `Certifier` with the wallet identity key — which is `key`, so
self-signed holds.

- [ ] **Step 2: Write the failing test**

```go
package profilecert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
)

const domain = "example.com"

func newKey(t *testing.T) *ec.PrivateKey {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestParse_Valid(t *testing.T) {
	key := newKey(t)
	at := time.Date(2026, 9, 18, 12, 0, 0, 123_456_789, time.UTC)
	body := certtest.Profile(t, key, "deggen", domain, at, map[string]string{"displayName": "Deggen"})

	p, err := Parse(context.Background(), body, domain)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Handle != "deggen" || p.Paymail != "deggen@example.com" {
		t.Errorf("handle/paymail = %q/%q", p.Handle, p.Paymail)
	}
	if p.IdentityKey != key.PubKey().ToDERHex() {
		t.Errorf("identity key = %s", p.IdentityKey)
	}
	if !p.IssuedAt.Equal(at.Truncate(time.Millisecond)) {
		t.Errorf("issuedAt = %v", p.IssuedAt)
	}
	if p.Released {
		t.Error("Released = true")
	}
	var round map[string]any
	if err := json.Unmarshal([]byte(p.JSON), &round); err != nil || round["signature"] == "" {
		t.Errorf("JSON not a certificate: %v", err)
	}
}

func TestParse_Released(t *testing.T) {
	key := newKey(t)
	body := certtest.Profile(t, key, "deggen", domain, time.Now(), map[string]string{"released": "true"})
	p, err := Parse(context.Background(), body, domain)
	if err != nil || !p.Released {
		t.Fatalf("Released = %v, err = %v", p != nil && p.Released, err)
	}
}

func TestParse_Rejects(t *testing.T) {
	key, other := newKey(t), newKey(t)
	now := time.Now()
	ctx := context.Background()

	tamper := func(mut func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(certtest.Profile(t, key, "deggen", domain, now, nil), &m); err != nil {
			t.Fatal(err)
		}
		mut(m)
		b, _ := json.Marshal(m)
		return b
	}
	manyFields := map[string]string{}
	for i := 0; i < 31; i++ { // + paymail + issuedAt = 33
		manyFields["f"+strings.Repeat("x", i+1)] = "v"
	}

	cases := []struct {
		name string
		body []byte
		want error
	}{
		{"not json", []byte("{"), ErrInvalid},
		{"oversize", append(certtest.Profile(t, key, "deggen", domain, now, nil), make([]byte, MaxBody)...), ErrInvalid},
		{"bad signature", tamper(func(m map[string]any) {
			m["fields"].(map[string]any)["displayName"] = "Mallory"
		}), ErrInvalid},
		{"subject != certifier", tamper(func(m map[string]any) {
			m["subject"] = other.PubKey().ToDERHex()
		}), ErrInvalid},
		{"missing paymail", certtest.New(t, key, map[string]string{"issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		{"missing issuedAt", certtest.New(t, key, map[string]string{"paymail": "deggen@" + domain}), ErrInvalid},
		{"bad issuedAt", certtest.New(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": "yesterday"}), ErrInvalid},
		{"uppercase paymail", certtest.New(t, key, map[string]string{"paymail": "Deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		{"no at sign", certtest.New(t, key, map[string]string{"paymail": "deggen", "issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		{"wrong domain", certtest.Profile(t, key, "deggen", "evil.com", now, nil), ErrWrongDomain},
		{"too many fields", certtest.Profile(t, key, "deggen", domain, now, manyFields), ErrInvalid},
		{"field too large", certtest.Profile(t, key, "deggen", domain, now, map[string]string{"bio": strings.Repeat("a", 1025)}), ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(ctx, c.body, domain); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}

	t.Run("wrong type", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.Type = wallet.StringBase64("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
	t.Run("non-zero revocation outpoint", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.RevocationOutpoint.Index = 1
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/profilecert/...`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 4: Write implementation**

```go
// Package profilecert checks that a certificate is a well-formed public
// profile: self-signed, of the profile type, naming a paymail on this server's
// domain. It knows nothing about which handles are registered.
package profilecert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth/certificates"
)

const (
	// Type is base64(SHA-256("public profile lookup")).
	Type = "SbatVXXssDW3AO0J9bxIljkHGbPBGCVAXg94gXFf0cE="
	// MaxBody bounds the serialized certificate.
	MaxBody       = 16384
	maxFields     = 32
	maxFieldValue = 1024
	zeroTxid      = "0000000000000000000000000000000000000000000000000000000000000000"
)

var (
	ErrInvalid     = errors.New("invalid profile certificate")
	ErrWrongDomain = errors.New("paymail is for another domain")
)

// Profile is a verified profile certificate.
type Profile struct {
	Handle       string
	Paymail      string
	IdentityKey  string
	SerialNumber string
	IssuedAt     time.Time // UTC, millisecond precision
	Released     bool
	JSON         string // canonical certificate JSON
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Parse verifies body as a profile certificate for domain.
func Parse(ctx context.Context, body []byte, domain string) (*Profile, error) {
	if len(body) > MaxBody {
		return nil, invalid("larger than %d bytes", MaxBody)
	}
	var c certificates.Certificate
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, invalid("not a certificate: %v", err)
	}
	if string(c.Type) != Type {
		return nil, invalid("wrong certificate type")
	}
	if !c.Subject.IsEqual(&c.Certifier) {
		return nil, invalid("subject and certifier differ")
	}
	if c.RevocationOutpoint == nil || c.RevocationOutpoint.Index != 0 || c.RevocationOutpoint.Txid.String() != zeroTxid {
		return nil, invalid("revocationOutpoint must be the zero outpoint")
	}
	if len(c.Fields) > maxFields {
		return nil, invalid("more than %d fields", maxFields)
	}
	for name, v := range c.Fields {
		if len(v) > maxFieldValue {
			return nil, invalid("field %s larger than %d bytes", name, maxFieldValue)
		}
	}
	if err := c.Verify(ctx); err != nil {
		return nil, invalid("signature: %v", err)
	}

	paymail := string(c.Fields["paymail"])
	handle, dom, ok := strings.Cut(paymail, "@")
	if !ok || handle == "" || paymail != strings.ToLower(paymail) {
		return nil, invalid("paymail field must be lowercase handle@domain")
	}
	if dom != strings.ToLower(domain) {
		return nil, ErrWrongDomain
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, string(c.Fields["issuedAt"]))
	if err != nil {
		return nil, invalid("issuedAt field must be RFC 3339")
	}

	canonical, err := json.Marshal(&c)
	if err != nil {
		return nil, invalid("re-marshal: %v", err)
	}
	return &Profile{
		Handle:       handle,
		Paymail:      paymail,
		IdentityKey:  c.Subject.ToDERHex(),
		SerialNumber: string(c.SerialNumber),
		IssuedAt:     issuedAt.UTC().Truncate(time.Millisecond),
		Released:     string(c.Fields["released"]) == "true",
		JSON:         string(canonical),
	}, nil
}
```

`time.RFC3339Nano` parses plain RFC 3339 too. If `c.Subject.IsEqual` has a
different name in v1.2.18, compare `c.Subject.ToDERHex() == c.Certifier.ToDERHex()`.

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/profilecert/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/profilecert && git commit -m "feat(profilecert): verify self-signed public profile certificates"
```

---

### Task 3: `HandleStore` contract, conformance suite, fake store

The contract lands together with the fake so the suite has one passing
implementation. `HandleStore` is a standalone interface: it is **never** embedded
in `storage.Store` (sqlstore does not implement it).

**Files:**
- Create: `pkg/storage/handles.go`
- Create: `pkg/storage/storagetest/handles.go`
- Create: `pkg/handlers/fakestore_handles_test.go`
- Modify: `pkg/handlers/fakestore_test.go` (add `handles` map to struct + constructor; run handle suite)

**Interfaces — Produces (`package storage`):**

```go
type HandleRecord struct {
	Handle          string
	Skeleton        string
	IdentityKey     *string // nil when released
	LastIdentityKey string
	Certificate     *string // nil when released
	SerialNumber    string
	IssuedAt        time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ReleasedAt      *time.Time
	CooldownUntil   *time.Time
	ReleasedBy      *string
}
type HandleClaim struct {
	Handle, Skeleton, IdentityKey, Certificate, SerialNumber string
	IssuedAt time.Time // ms precision
	Now      time.Time // for cooldown comparison
}
type ClaimResult int
const ( ClaimCreated ClaimResult = iota + 1; ClaimUpdated; ClaimUnchanged )
type HandleRelease struct {
	Handle        string
	Owner         *string    // set: owner tombstone; nil: admin release
	IssuedAt      *time.Time // required with Owner
	ReleasedBy    string     // "owner" or admin identity key
	CooldownUntil *time.Time // nil = none
	Now           time.Time
}
type HandleField int   // HandleFieldHandle, HandleFieldSkeleton
type HandleMatchMode int // HandleMatchPrefix, HandleMatchContains
type HandleMatch struct { Field HandleField; Mode HandleMatchMode; Value string; Limit int }

type HandleReader interface {
	GetHandle(ctx, handle string) (*HandleRecord, error)                 // any state; (nil,nil) if none
	GetHandleBySkeleton(ctx, skeleton string) (*HandleRecord, error)     // any state
	GetHandleByIdentityKey(ctx, identityKey string) (*HandleRecord, error) // active only
}
type HandleStore interface {
	HandleReader
	ClaimHandle(ctx, c HandleClaim) (ClaimResult, error)
	ReleaseHandle(ctx, r HandleRelease) error
	FindHandles(ctx, m HandleMatch) ([]HandleRecord, error) // active only, Handle ascending
}
var ErrHandleTaken, ErrHandleTooSimilar, ErrKeyHasHandle, ErrHandleCooldown, ErrStaleCertificate, ErrHandleNotFound
func DiagnoseClaim(ctx, r HandleReader, c HandleClaim, cause error) (ClaimResult, error)
func DiagnoseRelease(ctx, r HandleReader, rel HandleRelease, cause error) error
```

- [ ] **Step 1: Write the contract**

`pkg/storage/handles.go`:

```go
package storage

import (
	"context"
	"errors"
	"time"
)

var (
	ErrHandleTaken      = errors.New("handle taken")
	ErrHandleTooSimilar = errors.New("handle too similar to an existing handle")
	ErrKeyHasHandle     = errors.New("identity key already has a handle")
	ErrHandleCooldown   = errors.New("handle is in cooldown")
	ErrStaleCertificate = errors.New("certificate is not newer than the stored one")
	ErrHandleNotFound   = errors.New("handle not found")
)

// HandleRecord is one row of the handle registry. Rows are never deleted: a
// released handle keeps its IssuedAt (the replay guard) and its Skeleton.
type HandleRecord struct {
	Handle          string
	Skeleton        string
	IdentityKey     *string
	LastIdentityKey string
	Certificate     *string
	SerialNumber    string
	IssuedAt        time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ReleasedAt      *time.Time
	CooldownUntil   *time.Time
	ReleasedBy      *string
}

// Active reports whether the handle currently belongs to a key.
func (r *HandleRecord) Active() bool { return r.IdentityKey != nil }

// HandleClaim asks for Handle on behalf of IdentityKey. Times are millisecond
// precision UTC.
type HandleClaim struct {
	Handle       string
	Skeleton     string
	IdentityKey  string
	Certificate  string
	SerialNumber string
	IssuedAt     time.Time
	Now          time.Time
}

// ClaimResult says what a successful ClaimHandle did.
type ClaimResult int

const (
	ClaimCreated ClaimResult = iota + 1
	ClaimUpdated
	ClaimUnchanged
)

// HandleRelease releases a handle. With Owner set it is the owner's tombstone
// and IssuedAt must be newer than the stored value; with Owner nil it is an
// operator release.
type HandleRelease struct {
	Handle        string
	Owner         *string
	IssuedAt      *time.Time
	ReleasedBy    string
	CooldownUntil *time.Time
	Now           time.Time
}

type HandleField int

const (
	HandleFieldHandle HandleField = iota + 1
	HandleFieldSkeleton
)

type HandleMatchMode int

const (
	HandleMatchPrefix HandleMatchMode = iota + 1
	HandleMatchContains
)

// HandleMatch selects active handles whose Field matches Value literally (no
// wildcards are interpreted).
type HandleMatch struct {
	Field HandleField
	Mode  HandleMatchMode
	Value string
	Limit int
}

// HandleReader is the read side, split out so the diagnosis helpers can run
// against any backend.
type HandleReader interface {
	// GetHandle returns the record in any state, or (nil, nil).
	GetHandle(ctx context.Context, handle string) (*HandleRecord, error)
	// GetHandleBySkeleton returns the record in any state, or (nil, nil).
	GetHandleBySkeleton(ctx context.Context, skeleton string) (*HandleRecord, error)
	// GetHandleByIdentityKey returns the key's active record, or (nil, nil).
	GetHandleByIdentityKey(ctx context.Context, identityKey string) (*HandleRecord, error)
}

// HandleStore is the handle registry.
//
// ClaimHandle must be safe to race: of any number of concurrent claims whose
// Handle, Skeleton or IdentityKey collide, at most one is Created. Backends get
// that from unique keys and single conditional writes, never from
// check-then-insert. The order is: owner update, reclaim of a released row,
// insert; whatever fails falls through to DiagnoseClaim.
type HandleStore interface {
	HandleReader

	// ClaimHandle registers, updates or reclaims. Errors: ErrHandleTaken,
	// ErrHandleTooSimilar, ErrKeyHasHandle, ErrHandleCooldown,
	// ErrStaleCertificate.
	ClaimHandle(ctx context.Context, c HandleClaim) (ClaimResult, error)

	// ReleaseHandle clears IdentityKey and Certificate and records the release.
	// Errors: ErrHandleNotFound (no active row), ErrHandleTaken (Owner is not
	// the owner), ErrStaleCertificate.
	ReleaseHandle(ctx context.Context, r HandleRelease) error

	// FindHandles returns active records ordered by Handle ascending.
	FindHandles(ctx context.Context, m HandleMatch) ([]HandleRecord, error)
}

// DiagnoseClaim explains why none of a backend's claim writes applied. cause is
// the backend error, if any, returned when the registry offers no explanation.
func DiagnoseClaim(ctx context.Context, r HandleReader, c HandleClaim, cause error) (ClaimResult, error) {
	rec, err := r.GetHandle(ctx, c.Handle)
	if err != nil {
		return 0, err
	}
	if rec != nil {
		if rec.Active() {
			if *rec.IdentityKey != c.IdentityKey {
				return 0, ErrHandleTaken
			}
			if rec.SerialNumber == c.SerialNumber && rec.IssuedAt.Equal(c.IssuedAt) {
				return ClaimUnchanged, nil
			}
			return 0, ErrStaleCertificate
		}
		if !c.IssuedAt.After(rec.IssuedAt) {
			return 0, ErrStaleCertificate
		}
	}
	other, err := r.GetHandleByIdentityKey(ctx, c.IdentityKey)
	if err != nil {
		return 0, err
	}
	if other != nil {
		return 0, ErrKeyHasHandle
	}
	if rec != nil {
		return 0, ErrHandleCooldown
	}
	sim, err := r.GetHandleBySkeleton(ctx, c.Skeleton)
	if err != nil {
		return 0, err
	}
	if sim != nil {
		return 0, ErrHandleTooSimilar
	}
	if cause == nil {
		cause = errors.New("claim applied nowhere and the registry shows no conflict")
	}
	return 0, cause
}

// DiagnoseRelease explains why a release write matched nothing.
func DiagnoseRelease(ctx context.Context, r HandleReader, rel HandleRelease, cause error) error {
	rec, err := r.GetHandle(ctx, rel.Handle)
	if err != nil {
		return err
	}
	if rec == nil || !rec.Active() {
		return ErrHandleNotFound
	}
	if rel.Owner != nil && *rec.IdentityKey != *rel.Owner {
		return ErrHandleTaken
	}
	if rel.Owner != nil {
		return ErrStaleCertificate
	}
	if cause == nil {
		cause = errors.New("release applied nowhere")
	}
	return cause
}
```

- [ ] **Step 2: Write the conformance suite**

`pkg/storage/storagetest/handles.go`:

```go
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// NewHandleStoreFunc returns a clean, empty handle store. Called once per subtest.
type NewHandleStoreFunc func(t *testing.T) storage.HandleStore

var handleT0 = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func claim(handle, skeleton, key, serial string, issuedAt time.Time) storage.HandleClaim {
	return storage.HandleClaim{
		Handle: handle, Skeleton: skeleton, IdentityKey: key,
		Certificate: `{"serialNumber":"` + serial + `"}`, SerialNumber: serial,
		IssuedAt: issuedAt, Now: issuedAt,
	}
}

func mustClaim(t *testing.T, s storage.HandleStore, c storage.HandleClaim, want storage.ClaimResult) {
	t.Helper()
	got, err := s.ClaimHandle(context.Background(), c)
	if err != nil || got != want {
		t.Fatalf("ClaimHandle(%s) = %v, %v; want %v, nil", c.Handle, got, err, want)
	}
}

func wantClaimErr(t *testing.T, s storage.HandleStore, c storage.HandleClaim, want error) {
	t.Helper()
	if _, err := s.ClaimHandle(context.Background(), c); !errors.Is(err, want) {
		t.Fatalf("ClaimHandle(%s) err = %v, want %v", c.Handle, err, want)
	}
}

// RunHandleStoreTests runs the HandleStore contract against a backend.
func RunHandleStoreTests(t *testing.T, newStore NewHandleStoreFunc) {
	t.Helper()
	ctx := context.Background()

	t.Run("RegisterAndRead", func(t *testing.T) {
		s := newStore(t)
		before := time.Now()
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		after := time.Now()

		rec, err := s.GetHandle(ctx, "deggen")
		if err != nil || rec == nil {
			t.Fatalf("GetHandle = %v, %v", rec, err)
		}
		if !rec.Active() || *rec.IdentityKey != alice || rec.LastIdentityKey != alice {
			t.Errorf("owner = %v / %s", rec.IdentityKey, rec.LastIdentityKey)
		}
		if rec.Certificate == nil || *rec.Certificate != `{"serialNumber":"s1"}` || rec.SerialNumber != "s1" {
			t.Errorf("certificate = %v serial = %s", rec.Certificate, rec.SerialNumber)
		}
		if !rec.IssuedAt.Equal(handleT0) || rec.Skeleton != "degen" {
			t.Errorf("issuedAt = %v skeleton = %s", rec.IssuedAt, rec.Skeleton)
		}
		if rec.ReleasedAt != nil || rec.CooldownUntil != nil || rec.ReleasedBy != nil {
			t.Error("release fields set on a fresh row")
		}
		assertRecentUTC(t, "CreatedAt", rec.CreatedAt, before, after)
		assertRecentUTC(t, "UpdatedAt", rec.UpdatedAt, before, after)

		if r, _ := s.GetHandleBySkeleton(ctx, "degen"); r == nil || r.Handle != "deggen" {
			t.Errorf("GetHandleBySkeleton = %v", r)
		}
		if r, _ := s.GetHandleByIdentityKey(ctx, alice); r == nil || r.Handle != "deggen" {
			t.Errorf("GetHandleByIdentityKey = %v", r)
		}
		for name, get := range map[string]func() (*storage.HandleRecord, error){
			"handle":   func() (*storage.HandleRecord, error) { return s.GetHandle(ctx, "nobody") },
			"skeleton": func() (*storage.HandleRecord, error) { return s.GetHandleBySkeleton(ctx, "nobody") },
			"key":      func() (*storage.HandleRecord, error) { return s.GetHandleByIdentityKey(ctx, bob) },
		} {
			if r, err := get(); r != nil || err != nil {
				t.Errorf("missing %s = %v, %v; want nil, nil", name, r, err)
			}
		}
	})

	t.Run("Conflicts", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)

		wantClaimErr(t, s, claim("deggen", "degen", bob, "s2", handleT0.Add(time.Hour)), storage.ErrHandleTaken)
		wantClaimErr(t, s, claim("d3ggen", "degen", bob, "s2", handleT0), storage.ErrHandleTooSimilar)
		wantClaimErr(t, s, claim("other", "other", alice, "s2", handleT0.Add(time.Hour)), storage.ErrKeyHasHandle)
	})

	t.Run("Update", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		first, _ := s.GetHandle(ctx, "deggen")
		separateWrites()

		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimUnchanged)
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s2", handleT0), storage.ErrStaleCertificate)
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s2", handleT0.Add(-time.Second)), storage.ErrStaleCertificate)
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s1", handleT0.Add(time.Second)), storage.ErrStaleCertificate)

		mustClaim(t, s, claim("deggen", "degen", alice, "s2", handleT0.Add(time.Second)), storage.ClaimUpdated)
		rec, _ := s.GetHandle(ctx, "deggen")
		if rec.SerialNumber != "s2" || !rec.IssuedAt.Equal(handleT0.Add(time.Second)) {
			t.Errorf("after update: serial %s issuedAt %v", rec.SerialNumber, rec.IssuedAt)
		}
		if !rec.CreatedAt.Equal(first.CreatedAt) || !rec.UpdatedAt.After(first.UpdatedAt) {
			t.Errorf("createdAt %v→%v updatedAt %v→%v", first.CreatedAt, rec.CreatedAt, first.UpdatedAt, rec.UpdatedAt)
		}
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ErrStaleCertificate)
	})

	t.Run("OwnerReleaseAndCooldown", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)

		relAt := handleT0.Add(time.Hour)
		until := relAt.Add(30 * 24 * time.Hour)
		a, b := alice, bob

		stale := handleT0
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &stale, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); !errors.Is(err, storage.ErrStaleCertificate) {
			t.Fatalf("stale release err = %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &b, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); !errors.Is(err, storage.ErrHandleTaken) {
			t.Fatalf("non-owner release err = %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "nobody", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", Now: relAt}); !errors.Is(err, storage.ErrHandleNotFound) {
			t.Fatalf("missing release err = %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &until, ReleasedBy: "owner", Now: until}); !errors.Is(err, storage.ErrHandleNotFound) {
			t.Fatalf("double release err = %v", err)
		}

		rec, _ := s.GetHandle(ctx, "deggen")
		if rec.Active() || rec.Certificate != nil || rec.LastIdentityKey != alice {
			t.Errorf("released row = %+v", rec)
		}
		if rec.ReleasedAt == nil || !rec.ReleasedAt.Equal(relAt) || rec.CooldownUntil == nil || !rec.CooldownUntil.Equal(until) || rec.ReleasedBy == nil || *rec.ReleasedBy != "owner" {
			t.Errorf("release fields = %v %v %v", rec.ReleasedAt, rec.CooldownUntil, rec.ReleasedBy)
		}
		if !rec.IssuedAt.Equal(relAt) {
			t.Errorf("issuedAt after tombstone = %v, want %v", rec.IssuedAt, relAt)
		}
		if r, _ := s.GetHandleByIdentityKey(ctx, alice); r != nil {
			t.Error("released key still resolves")
		}
		if r, _ := s.GetHandleBySkeleton(ctx, "degen"); r == nil {
			t.Error("skeleton reservation lost on release")
		}

		// Replay of the original certificate, and of anything not newer than the tombstone.
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ErrStaleCertificate)
		// Another key inside cooldown.
		in := claim("deggen", "degen", bob, "s3", relAt.Add(time.Minute))
		wantClaimErr(t, s, in, storage.ErrHandleCooldown)
		// A look-alike is still blocked while the row exists.
		wantClaimErr(t, s, claim("d3ggen", "degen", bob, "s3", relAt.Add(time.Minute)), storage.ErrHandleTooSimilar)
		// Alice frees herself to take another handle.
		mustClaim(t, s, claim("alice", "alice", alice, "s4", relAt.Add(time.Minute)), storage.ClaimCreated)
		// ...and so cannot reclaim the old one while holding the new.
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s5", relAt.Add(2*time.Minute)), storage.ErrKeyHasHandle)
		// After cooldown another key may claim.
		late := claim("deggen", "degen", bob, "s6", until.Add(time.Second))
		mustClaim(t, s, late, storage.ClaimCreated)
		rec, _ = s.GetHandle(ctx, "deggen")
		if !rec.Active() || *rec.IdentityKey != bob || rec.LastIdentityKey != bob || rec.ReleasedAt != nil || rec.CooldownUntil != nil || rec.ReleasedBy != nil {
			t.Errorf("reclaimed row = %+v", rec)
		}
	})

	t.Run("PreviousOwnerReclaimsInsideCooldown", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		a := alice
		relAt := handleT0.Add(time.Hour)
		until := relAt.Add(time.Hour * 720)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); err != nil {
			t.Fatal(err)
		}
		mustClaim(t, s, claim("deggen", "degen", alice, "s2", relAt.Add(time.Minute)), storage.ClaimCreated)
	})

	t.Run("AdminReleaseSkipsCooldown", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		now := handleT0.Add(time.Hour)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", ReleasedBy: carol, Now: now}); err != nil {
			t.Fatalf("admin release: %v", err)
		}
		rec, _ := s.GetHandle(ctx, "deggen")
		if rec.Active() || rec.CooldownUntil != nil || rec.ReleasedBy == nil || *rec.ReleasedBy != carol || !rec.IssuedAt.Equal(handleT0) {
			t.Errorf("admin released row = %+v", rec)
		}
		mustClaim(t, s, claim("deggen", "degen", bob, "s2", now.Add(time.Minute)), storage.ClaimCreated)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "nobody", ReleasedBy: carol, Now: now}); !errors.Is(err, storage.ErrHandleNotFound) {
			t.Fatalf("admin release of missing handle err = %v", err)
		}
	})

	t.Run("FindHandles", func(t *testing.T) {
		s := newStore(t)
		for i, h := range []struct{ handle, skeleton string }{
			{"deggen", "degen"}, {"deg", "deg"}, {"degas", "degas"}, {"a_deg", "adeg"}, {"bob", "bob"}, {"d3x", "d3x"},
		} {
			mustClaim(t, s, claim(h.handle, h.skeleton, fmt.Sprintf("02key%d", i), "s", handleT0), storage.ClaimCreated)
		}
		// A released handle must not be found.
		k := "02key4"
		rel := handleT0.Add(time.Second)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "bob", Owner: &k, IssuedAt: &rel, ReleasedBy: "owner", Now: rel}); err != nil {
			t.Fatal(err)
		}

		find := func(f storage.HandleField, m storage.HandleMatchMode, v string, limit int) []string {
			t.Helper()
			recs, err := s.FindHandles(ctx, storage.HandleMatch{Field: f, Mode: m, Value: v, Limit: limit})
			if err != nil {
				t.Fatalf("FindHandles: %v", err)
			}
			out := make([]string, len(recs))
			for i, r := range recs {
				out[i] = r.Handle
			}
			return out
		}
		check := func(label string, got, want []string) {
			t.Helper()
			if !equalStrings(got, want) {
				t.Errorf("%s = %v, want %v", label, got, want)
			}
		}
		check("handle prefix", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "deg", 10), []string{"deg", "degas", "deggen"})
		check("handle prefix limit", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "deg", 2), []string{"deg", "degas"})
		check("handle contains", find(storage.HandleFieldHandle, storage.HandleMatchContains, "deg", 10), []string{"a_deg", "deg", "degas", "deggen"})
		check("skeleton prefix", find(storage.HandleFieldSkeleton, storage.HandleMatchPrefix, "dege", 10), []string{"deggen"})
		check("released excluded", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "bob", 10), []string{})
		// "_" and "%" and "." are literals, not wildcards.
		check("underscore literal", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "a_", 10), []string{"a_deg"})
		check("underscore is not wildcard", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "d_x", 10), []string{})
		check("percent literal", find(storage.HandleFieldHandle, storage.HandleMatchContains, "%", 10), []string{})
		check("dot literal", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "d.x", 10), []string{})
	})

	t.Run("ConcurrentClaimsOneWinner", func(t *testing.T) {
		s := newStore(t)
		const n = 16
		var wg sync.WaitGroup
		results := make([]storage.ClaimResult, n)
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Distinct handles and keys, one shared skeleton.
				c := claim(fmt.Sprintf("pay-pal%02d", i), "paypal", fmt.Sprintf("02racer%02d", i), "s", handleT0)
				results[i], errs[i] = s.ClaimHandle(ctx, c)
			}(i)
		}
		wg.Wait()
		created := 0
		for i := range results {
			switch {
			case errs[i] == nil && results[i] == storage.ClaimCreated:
				created++
			case errs[i] == nil:
				t.Errorf("racer %d: result %v without error", i, results[i])
			case !errors.Is(errs[i], storage.ErrHandleTooSimilar):
				t.Logf("racer %d lost with a backend error (acceptable, retryable): %v", i, errs[i])
			}
		}
		if created != 1 {
			t.Fatalf("%d claims created, want exactly 1", created)
		}
	})
}
```

- [ ] **Step 3: Write the fake + hook the suite**

In `pkg/handlers/fakestore_test.go` add field `handles map[string]*storage.HandleRecord`
to `fakeStore` and `handles: map[string]*storage.HandleRecord{},` to `newFakeStore`.
Append to `TestConformance_Fake`'s file a new test:

```go
func TestConformance_FakeHandles(t *testing.T) {
	storagetest.RunHandleStoreTests(t, func(t *testing.T) storage.HandleStore { return newFakeStore() })
}
```

`pkg/handlers/fakestore_handles_test.go`:

```go
package handlers

import (
	"context"
	"sort"
	"strings"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

func copyHandle(r *storage.HandleRecord) *storage.HandleRecord {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

func (f *fakeStore) GetHandle(_ context.Context, handle string) (*storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyHandle(f.handles[handle]), nil
}

func (f *fakeStore) GetHandleBySkeleton(_ context.Context, skeleton string) (*storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.handles {
		if r.Skeleton == skeleton {
			return copyHandle(r), nil
		}
	}
	return nil, nil
}

func (f *fakeStore) getByKeyLocked(identityKey string) *storage.HandleRecord {
	for _, r := range f.handles {
		if r.IdentityKey != nil && *r.IdentityKey == identityKey {
			return r
		}
	}
	return nil
}

func (f *fakeStore) GetHandleByIdentityKey(_ context.Context, identityKey string) (*storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyHandle(f.getByKeyLocked(identityKey)), nil
}

func (f *fakeStore) ClaimHandle(ctx context.Context, c storage.HandleClaim) (storage.ClaimResult, error) {
	if res, ok := f.tryClaim(c); ok {
		return res, nil
	}
	return storage.DiagnoseClaim(ctx, f, c, nil)
}

func (f *fakeStore) tryClaim(c storage.HandleClaim) (storage.ClaimResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, cert := c.IdentityKey, c.Certificate
	rec := f.handles[c.Handle]

	if rec != nil && rec.IdentityKey != nil {
		if *rec.IdentityKey == c.IdentityKey && rec.IssuedAt.Before(c.IssuedAt) && rec.SerialNumber != c.SerialNumber {
			rec.Certificate, rec.SerialNumber, rec.IssuedAt, rec.UpdatedAt = &cert, c.SerialNumber, c.IssuedAt, f.tick()
			return storage.ClaimUpdated, true
		}
		return 0, false
	}
	if f.getByKeyLocked(c.IdentityKey) != nil {
		return 0, false
	}
	if rec != nil {
		free := rec.LastIdentityKey == c.IdentityKey || rec.CooldownUntil == nil || !rec.CooldownUntil.After(c.Now)
		if !rec.IssuedAt.Before(c.IssuedAt) || !free {
			return 0, false
		}
		rec.IdentityKey, rec.LastIdentityKey, rec.Certificate = &key, key, &cert
		rec.SerialNumber, rec.IssuedAt, rec.UpdatedAt = c.SerialNumber, c.IssuedAt, f.tick()
		rec.ReleasedAt, rec.CooldownUntil, rec.ReleasedBy = nil, nil, nil
		return storage.ClaimCreated, true
	}
	for _, r := range f.handles {
		if r.Skeleton == c.Skeleton {
			return 0, false
		}
	}
	ts := f.tick()
	f.handles[c.Handle] = &storage.HandleRecord{
		Handle: c.Handle, Skeleton: c.Skeleton, IdentityKey: &key, LastIdentityKey: key,
		Certificate: &cert, SerialNumber: c.SerialNumber, IssuedAt: c.IssuedAt, CreatedAt: ts, UpdatedAt: ts,
	}
	return storage.ClaimCreated, true
}

func (f *fakeStore) ReleaseHandle(ctx context.Context, r storage.HandleRelease) error {
	if f.tryRelease(r) {
		return nil
	}
	return storage.DiagnoseRelease(ctx, f, r, nil)
}

func (f *fakeStore) tryRelease(r storage.HandleRelease) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.handles[r.Handle]
	if rec == nil || rec.IdentityKey == nil {
		return false
	}
	if r.Owner != nil {
		if *rec.IdentityKey != *r.Owner || r.IssuedAt == nil || !rec.IssuedAt.Before(*r.IssuedAt) {
			return false
		}
		rec.IssuedAt = *r.IssuedAt
	}
	now, by := r.Now, r.ReleasedBy
	rec.IdentityKey, rec.Certificate = nil, nil
	rec.ReleasedAt, rec.ReleasedBy, rec.CooldownUntil, rec.UpdatedAt = &now, &by, r.CooldownUntil, f.tick()
	return true
}

func (f *fakeStore) FindHandles(_ context.Context, m storage.HandleMatch) ([]storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []storage.HandleRecord{}
	for _, r := range f.handles {
		if r.IdentityKey == nil {
			continue
		}
		v := r.Handle
		if m.Field == storage.HandleFieldSkeleton {
			v = r.Skeleton
		}
		hit := strings.HasPrefix(v, m.Value)
		if m.Mode == storage.HandleMatchContains {
			hit = strings.Contains(v, m.Value)
		}
		if hit {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	if m.Limit > 0 && len(out) > m.Limit {
		out = out[:m.Limit]
	}
	return out, nil
}
```

(`CooldownUntil` stored by pointer from the request: copy it — `if r.CooldownUntil != nil { cu := *r.CooldownUntil; rec.CooldownUntil = &cu }` — if `go vet` flags aliasing.)

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./pkg/handlers/ -run 'TestConformance_Fake' -v 2>&1 | tail -30`
Expected: all `TestConformance_FakeHandles/*` PASS. Any failure here is a bug in
the fake **or** in the suite's expectations — resolve against the spec, not by
weakening assertions.

- [ ] **Step 5: Commit**

```bash
git add pkg/storage/handles.go pkg/storage/storagetest/handles.go pkg/handlers/fakestore_test.go pkg/handlers/fakestore_handles_test.go
git commit -m "feat(storage): HandleStore contract, conformance suite and fake"
```

---

### Task 4: (removed)

SQL is not a target for this feature (MongoDB Atlas only). There is no sqlstore
implementation of `HandleStore`. Skip to Task 5.

---

### Task 5: mongostore implementation

**Files:**
- Create: `pkg/storage/mongostore/handles.go`
- Modify: `pkg/storage/mongostore/mongostore.go` (collection const + indexes)
- Modify: `pkg/storage/mongostore/mongostore_test.go`
- Modify: `pkg/storage/storage.go` (package doc comment only: mention that `HandleStore` in `handles.go` is an optional contract implemented by mongostore)

**Interfaces:**
- Produces: `*mongostore.Store` implements `storage.HandleStore` (compile-time assertion in `handles.go`). `storage.Store` is unchanged.

- [ ] **Step 1: Hook the suite**

`mongostore_test.go`:

```go
func TestHandleConformance_Mongo(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}
	storagetest.RunHandleStoreTests(t, func(t *testing.T) storage.HandleStore {
		return newMongo(t, uri, database).(*Store)
	})
}
```

- [ ] **Step 2: Indexes**

In `mongostore.go` add `handlesColl = "handles"` to the collection constants and
this entry to the `indexes` map in `EnsureSchema`:

```go
		handlesColl: {
			// Look-alike handles collide here; released rows keep their skeleton.
			{Keys: bson.D{{Key: "skeleton", Value: 1}}, Options: options.Index().SetUnique(true)},
			// One handle per key. Partial, because a released row has no
			// identityKey and any number of those must coexist.
			{
				Keys: bson.D{{Key: "identityKey", Value: 1}},
				Options: options.Index().SetUnique(true).
					SetPartialFilterExpression(bson.D{{Key: "identityKey", Value: bson.D{{Key: "$type", Value: "string"}}}}),
			},
		},
```

- [ ] **Step 3: Implementation**

`pkg/storage/mongostore/handles.go`:

```go
package mongostore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// handleDoc is one registry row. The handle is the _id, so the first claim of
// a handle wins on the primary key. Released rows have no identityKey and no
// certificate field at all (unset, not null) so the partial unique index and
// the $exists filters agree.
type handleDoc struct {
	Handle          string     `bson:"_id"`
	Skeleton        string     `bson:"skeleton"`
	IdentityKey     *string    `bson:"identityKey,omitempty"`
	LastIdentityKey string     `bson:"lastIdentityKey"`
	Certificate     *string    `bson:"certificate,omitempty"`
	SerialNumber    string     `bson:"serialNumber"`
	IssuedAt        time.Time  `bson:"issuedAt"`
	CreatedAt       time.Time  `bson:"createdAt"`
	UpdatedAt       time.Time  `bson:"updatedAt"`
	ReleasedAt      *time.Time `bson:"releasedAt,omitempty"`
	CooldownUntil   *time.Time `bson:"cooldownUntil,omitempty"`
	ReleasedBy      *string    `bson:"releasedBy,omitempty"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toHandle(d handleDoc) storage.HandleRecord {
	return storage.HandleRecord{
		Handle: d.Handle, Skeleton: d.Skeleton, IdentityKey: d.IdentityKey, LastIdentityKey: d.LastIdentityKey,
		Certificate: d.Certificate, SerialNumber: d.SerialNumber,
		IssuedAt: d.IssuedAt.UTC(), CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
		ReleasedAt: utcPtr(d.ReleasedAt), CooldownUntil: utcPtr(d.CooldownUntil), ReleasedBy: d.ReleasedBy,
	}
}

func (s *Store) getHandleWhere(ctx context.Context, filter bson.M) (*storage.HandleRecord, error) {
	var d handleDoc
	err := s.db.Collection(handlesColl).FindOne(ctx, filter).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := toHandle(d)
	return &r, nil
}

// GetHandle implements storage.HandleReader.
func (s *Store) GetHandle(ctx context.Context, handle string) (*storage.HandleRecord, error) {
	return s.getHandleWhere(ctx, bson.M{"_id": handle})
}

// GetHandleBySkeleton implements storage.HandleReader.
func (s *Store) GetHandleBySkeleton(ctx context.Context, skeleton string) (*storage.HandleRecord, error) {
	return s.getHandleWhere(ctx, bson.M{"skeleton": skeleton})
}

// GetHandleByIdentityKey implements storage.HandleReader.
func (s *Store) GetHandleByIdentityKey(ctx context.Context, identityKey string) (*storage.HandleRecord, error) {
	return s.getHandleWhere(ctx, bson.M{"identityKey": identityKey})
}

// ClaimHandle implements storage.HandleStore: owner update, reclaim, insert —
// one write each, the unique indexes deciding any race.
func (s *Store) ClaimHandle(ctx context.Context, c storage.HandleClaim) (storage.ClaimResult, error) {
	coll := s.db.Collection(handlesColl)
	ts := now()

	res, err := coll.UpdateOne(ctx,
		bson.M{"_id": c.Handle, "identityKey": c.IdentityKey, "issuedAt": bson.M{"$lt": c.IssuedAt}, "serialNumber": bson.M{"$ne": c.SerialNumber}},
		bson.M{"$set": bson.M{"certificate": c.Certificate, "serialNumber": c.SerialNumber, "issuedAt": c.IssuedAt, "updatedAt": ts}})
	if err != nil {
		return storage.DiagnoseClaim(ctx, s, c, err)
	}
	if res.MatchedCount > 0 {
		return storage.ClaimUpdated, nil
	}

	res, err = coll.UpdateOne(ctx,
		bson.M{
			"_id": c.Handle, "identityKey": bson.M{"$exists": false}, "issuedAt": bson.M{"$lt": c.IssuedAt},
			"$or": bson.A{
				bson.M{"lastIdentityKey": c.IdentityKey},
				bson.M{"cooldownUntil": bson.M{"$exists": false}},
				bson.M{"cooldownUntil": bson.M{"$lte": c.Now}},
			},
		},
		bson.M{
			"$set": bson.M{"identityKey": c.IdentityKey, "lastIdentityKey": c.IdentityKey, "certificate": c.Certificate,
				"serialNumber": c.SerialNumber, "issuedAt": c.IssuedAt, "updatedAt": ts},
			"$unset": bson.M{"releasedAt": "", "cooldownUntil": "", "releasedBy": ""},
		})
	if err != nil {
		return storage.DiagnoseClaim(ctx, s, c, err)
	}
	if res.MatchedCount > 0 {
		return storage.ClaimCreated, nil
	}

	key, cert := c.IdentityKey, c.Certificate
	_, err = coll.InsertOne(ctx, handleDoc{
		Handle: c.Handle, Skeleton: c.Skeleton, IdentityKey: &key, LastIdentityKey: key, Certificate: &cert,
		SerialNumber: c.SerialNumber, IssuedAt: c.IssuedAt, CreatedAt: ts, UpdatedAt: ts,
	})
	if err != nil {
		return storage.DiagnoseClaim(ctx, s, c, err)
	}
	return storage.ClaimCreated, nil
}

// ReleaseHandle implements storage.HandleStore.
func (s *Store) ReleaseHandle(ctx context.Context, r storage.HandleRelease) error {
	filter := bson.M{"_id": r.Handle, "identityKey": bson.M{"$exists": true}}
	set := bson.M{"updatedAt": now(), "releasedAt": r.Now, "releasedBy": r.ReleasedBy}
	unset := bson.M{"identityKey": "", "certificate": ""}
	if r.Owner != nil {
		if r.IssuedAt == nil {
			return fmt.Errorf("owner release without IssuedAt")
		}
		filter["identityKey"] = *r.Owner
		filter["issuedAt"] = bson.M{"$lt": *r.IssuedAt}
		set["issuedAt"] = *r.IssuedAt
	}
	if r.CooldownUntil != nil {
		set["cooldownUntil"] = *r.CooldownUntil
	} else {
		unset["cooldownUntil"] = ""
	}
	res, err := s.db.Collection(handlesColl).UpdateOne(ctx, filter, bson.M{"$set": set, "$unset": unset})
	if err != nil || res.MatchedCount == 0 {
		return storage.DiagnoseRelease(ctx, s, r, err)
	}
	return nil
}

// FindHandles implements storage.HandleStore.
func (s *Store) FindHandles(ctx context.Context, m storage.HandleMatch) ([]storage.HandleRecord, error) {
	field := "_id"
	if m.Field == storage.HandleFieldSkeleton {
		field = "skeleton"
	}
	pattern := regexp.QuoteMeta(m.Value)
	if m.Mode == storage.HandleMatchPrefix {
		pattern = "^" + pattern
	}
	limit := int64(m.Limit)
	if limit <= 0 {
		limit = 10
	}
	cur, err := s.db.Collection(handlesColl).Find(ctx,
		bson.M{"identityKey": bson.M{"$exists": true}, field: bson.M{"$regex": pattern}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var docs []handleDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]storage.HandleRecord, 0, len(docs))
	for _, d := range docs {
		out = append(out, toHandle(d))
	}
	return out, nil
}

var _ storage.HandleStore = (*Store)(nil)
```

Timestamps handed in (`IssuedAt`, `Now`, `CooldownUntil`) are already
millisecond precision (profilecert truncates; handlers truncate `now`), which is
what BSON stores.

- [ ] **Step 4: Run against a real MongoDB (mandatory)**

```bash
go build ./... && go vet ./pkg/storage/...
MONGO_TEST_URI=mongodb://localhost:27017 MONGO_TEST_DATABASE=messagebox_lookup_test \
  go test ./pkg/storage/mongostore/ -run 'Handle' -v 2>&1 | tail -40
MONGO_TEST_URI=mongodb://localhost:27017 MONGO_TEST_DATABASE=messagebox_lookup_test \
  go test -race -count=3 ./pkg/storage/mongostore/
```
Expected: every `TestHandleConformance_Mongo/*` subtest PASS (not SKIP), including
`ConcurrentClaimsOneWinner`; the pre-existing `TestConformance_Mongo` still PASS.
A skipped Mongo suite is a task failure — report it, do not claim success.

Also verify the indexes really exist and the partial filter took:

```bash
mongosh --quiet mongodb://localhost:27017/messagebox_lookup_test --eval 'printjson(db.handles.getIndexes())'
```
Expected: `skeleton_1` unique; `identityKey_1` unique with
`partialFilterExpression: { identityKey: { $type: "string" } }`.

Atlas compatibility rules for this file: no transactions/sessions, no
`$where`/`$function`, no `collMod`, only `createIndexes`/`find`/`insertOne`/
`updateOne`. User input reaches a query only as (a) an exact-match string value
or (b) `regexp.QuoteMeta`-escaped `$regex` — never as a raw pattern, operator
document or field name.

- [ ] **Step 5: Commit**

```bash
git add pkg/storage && git commit -m "feat(mongostore): handle registry"
```

---

### Task 6: config

**Files:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/config_test.go` (create if absent)
- Modify: `.env.example`

**Interfaces — Produces** on `config.Config`:
`PaymailDomain string`, `PaymailHost string`, `HandleCooldown time.Duration`, `AdminIdentityKeys []string`, `LookupRatePerMin int`, `TrustProxy bool`.

- [ ] **Step 1: Failing test**

```go
package config

import (
	"testing"
	"time"
)

func TestLoad_Lookup(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaymailDomain != "" || cfg.HandleCooldown != 30*24*time.Hour || cfg.LookupRatePerMin != 60 || cfg.TrustProxy || len(cfg.AdminIdentityKeys) != 0 {
		t.Errorf("defaults = %+v", cfg)
	}

	t.Setenv("PAYMAIL_DOMAIN", "Example.COM")
	if _, err := Load(); err == nil {
		t.Error("PAYMAIL_DOMAIN without PAYMAIL_HOST must fail")
	}

	t.Setenv("PAYMAIL_HOST", "https://mb.example.com/")
	t.Setenv("HANDLE_COOLDOWN_DAYS", "7")
	t.Setenv("ADMIN_IDENTITY_KEYS", " 02aa , 03BB,")
	t.Setenv("LOOKUP_RATE_PER_MIN", "5")
	t.Setenv("TRUST_PROXY", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaymailDomain != "example.com" || cfg.PaymailHost != "https://mb.example.com" {
		t.Errorf("domain/host = %q %q", cfg.PaymailDomain, cfg.PaymailHost)
	}
	if cfg.HandleCooldown != 7*24*time.Hour || cfg.LookupRatePerMin != 5 || !cfg.TrustProxy {
		t.Errorf("cfg = %+v", cfg)
	}
	if len(cfg.AdminIdentityKeys) != 2 || cfg.AdminIdentityKeys[0] != "02aa" || cfg.AdminIdentityKeys[1] != "03bb" {
		t.Errorf("admins = %v", cfg.AdminIdentityKeys)
	}
}
```

Run: `go test ./pkg/config/` → FAIL.

- [ ] **Step 2: Implement**

Add fields to `Config` under a `// Paymail profile lookup (optional)` comment.
Add imports `strings`, `time`. In `Load`, after the `ServerPrivateKey` check:

```go
	cfg.PaymailDomain = strings.ToLower(strings.TrimSpace(os.Getenv("PAYMAIL_DOMAIN")))
	cfg.PaymailHost = strings.TrimRight(strings.TrimSpace(os.Getenv("PAYMAIL_HOST")), "/")
	if cfg.PaymailDomain != "" && cfg.PaymailHost == "" {
		return nil, fmt.Errorf("PAYMAIL_HOST is required when PAYMAIL_DOMAIN is set")
	}
	cfg.HandleCooldown = time.Duration(getEnvInt("HANDLE_COOLDOWN_DAYS", 30)) * 24 * time.Hour
	cfg.LookupRatePerMin = getEnvInt("LOOKUP_RATE_PER_MIN", 60)
	cfg.TrustProxy = os.Getenv("TRUST_PROXY") == "true"
	for _, k := range strings.Split(os.Getenv("ADMIN_IDENTITY_KEYS"), ",") {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			cfg.AdminIdentityKeys = append(cfg.AdminIdentityKeys, k)
		}
	}
```

and the helper:

```go
func getEnvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return fallback
}
```

Append to `.env.example`:

```
# Paymail profile lookup (feature off when PAYMAIL_DOMAIN is empty)
PAYMAIL_DOMAIN=
PAYMAIL_HOST=
HANDLE_COOLDOWN_DAYS=30
ADMIN_IDENTITY_KEYS=
LOOKUP_RATE_PER_MIN=60
TRUST_PROXY=false
```

- [ ] **Step 3: Run + commit**

Run: `go test ./pkg/config/` → PASS.

```bash
git add pkg/config .env.example && git commit -m "feat(config): paymail lookup settings"
```

---

### Task 7: rate limiter

**Files:**
- Create: `pkg/handlers/ratelimit.go`
- Test: `pkg/handlers/ratelimit_test.go`

**Interfaces — Produces:**
- `func NewRateLimiter(perMinute int, trustProxy bool) *RateLimiter` (`perMinute <= 0` disables)
- `func (l *RateLimiter) Wrap(next http.Handler) http.Handler` — 429 `ERR_RATE_LIMITED`
- unexported `now func() time.Time` field for tests.

- [ ] **Step 1: Failing test**

```go
package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter(2, false)
	l.now = func() time.Time { return clock }
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	hit := func(remote, xff string) int {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if hit("1.1.1.1:1000", "") != 204 || hit("1.1.1.1:2000", "") != 204 {
		t.Fatal("first two requests must pass")
	}
	if got := hit("1.1.1.1:3000", ""); got != 429 {
		t.Fatalf("third request = %d, want 429", got)
	}
	if hit("2.2.2.2:1000", "") != 204 {
		t.Fatal("another IP must pass")
	}
	// X-Forwarded-For is ignored unless the proxy is trusted.
	if got := hit("1.1.1.1:4000", "9.9.9.9"); got != 429 {
		t.Fatalf("spoofed XFF = %d, want 429", got)
	}
	clock = clock.Add(time.Minute)
	if hit("1.1.1.1:5000", "") != 204 {
		t.Fatal("new window must pass")
	}

	trusted := NewRateLimiter(1, true)
	trusted.now = func() time.Time { return clock }
	th := trusted.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for i, want := range []int{204, 429} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("X-Forwarded-For", "9.9.9.9, 10.0.0.1")
		w := httptest.NewRecorder()
		th.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("trusted hit %d = %d, want %d", i, w.Code, want)
		}
	}

	off := NewRateLimiter(0, false)
	oh := off.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		oh.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 204 {
			t.Fatal("disabled limiter must pass everything")
		}
	}
}
```

Run: `go test ./pkg/handlers/ -run TestRateLimiter` → FAIL.

- [ ] **Step 2: Implement**

```go
package handlers

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter is a per-IP fixed-window limiter for the unauthenticated lookup
// routes. It is in-memory, so each replica counts on its own.
type RateLimiter struct {
	perMinute  int
	trustProxy bool
	now        func() time.Time

	mu      sync.Mutex
	window  int64 // unix minute the counts belong to
	counts  map[string]int
}

// NewRateLimiter returns a limiter; perMinute <= 0 disables it.
func NewRateLimiter(perMinute int, trustProxy bool) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, trustProxy: trustProxy, now: time.Now, counts: map[string]int{}}
}

func (l *RateLimiter) clientIP(r *http.Request) string {
	if l.trustProxy {
		if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (l *RateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Dropping the whole map each minute is the sweep: memory is bounded by one
	// minute of distinct clients.
	if w := l.now().Unix() / 60; w != l.window {
		l.window, l.counts = w, map[string]int{}
	}
	l.counts[ip]++
	return l.counts[ip] <= l.perMinute
}

// Wrap applies the limit to next.
func (l *RateLimiter) Wrap(next http.Handler) http.Handler {
	if l.perMinute <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(l.clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "ERR_RATE_LIMITED", "Too many requests; retry in a minute.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 3: Run + commit**

Run: `go test ./pkg/handlers/ -run TestRateLimiter` → PASS (run `gofmt -l pkg/handlers`; fix alignment).

```bash
git add pkg/handlers/ratelimit.go pkg/handlers/ratelimit_test.go && git commit -m "feat(handlers): per-IP rate limiter for public routes"
```

---

### Task 8: public lookup handlers

**Files:**
- Create: `pkg/handlers/lookup.go`
- Modify: `pkg/handlers/helpers.go` (Server struct: add `lookup *LookupConfig`, `handles storage.HandleStore`, `now func() time.Time`)
- Test: `pkg/handlers/lookup_test.go`

**Interfaces:**
- Consumes: `handles.Validate/Skeleton/BRFC*`, `profilecert.Parse/MaxBody/Err*`, `storage.HandleStore` via the new `s.handles` field (NOT `s.Store` — `storage.Store` has no handle methods), `certtest`.
- Produces:
  - `type LookupConfig struct { Domain, Host string; Cooldown time.Duration; AdminKeys []string }`
  - `func (s *Server) EnableLookup(c LookupConfig, hs storage.HandleStore)`
  - `func (s *Server) LookupRoutes() *http.ServeMux` — mounts `GET /.well-known/bsvalias`, `PUT /api/handle`, `GET /api/handle/available/{handle}`, `GET /api/handle/{query}`, `GET /api/identityKey/{pubkey}`
  - unexported `s.clock() time.Time` (ms-truncated UTC; uses `s.now` when set)

- [ ] **Step 1: Failing tests**

```go
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
)

const testDomain = "example.com"

type lookupEnv struct {
	t     *testing.T
	srv   *Server
	mux   *http.ServeMux
	clock time.Time
}

func newLookupEnv(t *testing.T) *lookupEnv {
	t.Helper()
	e := &lookupEnv{t: t, srv: setupTestServer(t), clock: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	e.srv.EnableLookup(LookupConfig{Domain: testDomain, Host: "https://mb.example.com", Cooldown: 30 * 24 * time.Hour, AdminKeys: []string{mockIdentityKey}}, e.srv.Store.(*fakeStore))
	e.srv.now = func() time.Time { return e.clock }
	e.mux = e.srv.LookupRoutes()
	return e
}

func (e *lookupEnv) do(method, path string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
	return w
}

func (e *lookupEnv) put(key *ec.PrivateKey, handle string, issuedAt time.Time, extra map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("PUT", "/api/handle", certtest.Profile(e.t, key, handle, testDomain, issuedAt, extra))
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body)
	}
	if code != "" {
		var e ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		if e.Code != code {
			t.Fatalf("code = %q, want %q", e.Code, code)
		}
	}
}

func testKey(t *testing.T) *ec.PrivateKey {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWellKnown(t *testing.T) {
	e := newLookupEnv(t)
	w := e.do("GET", "/.well-known/bsvalias", nil)
	wantStatus(t, w, 200, "")
	var doc struct {
		Bsvalias     string            `json:"bsvalias"`
		Capabilities map[string]string `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Bsvalias != "1.0" ||
		doc.Capabilities["0ace65da5987"] != "https://mb.example.com/api/handle/{query}" ||
		doc.Capabilities["43dcf83ddc5f"] != "https://mb.example.com/api/identityKey/{pubkey}" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestPutHandle_Lifecycle(t *testing.T) {
	e := newLookupEnv(t)
	alice, bob := testKey(t), testKey(t)
	t0 := e.clock

	wantStatus(t, e.put(alice, "deggen", t0, map[string]string{"displayName": "Deggen"}), 201, "")

	// A newer certificate updates; resubmitting it is a no-op.
	same := certtest.Profile(t, alice, "deggen", testDomain, t0.Add(time.Second), nil)
	wantStatus(t, e.do("PUT", "/api/handle", same), 200, "")
	wantStatus(t, e.do("PUT", "/api/handle", same), 200, "")

	// Conflicts.
	wantStatus(t, e.put(bob, "deggen", t0, nil), 409, "ERR_HANDLE_TAKEN")
	wantStatus(t, e.put(bob, "d3ggen", t0, nil), 409, "ERR_HANDLE_TOO_SIMILAR")
	wantStatus(t, e.put(alice, "another", t0.Add(time.Hour), nil), 409, "ERR_KEY_HAS_HANDLE")
	wantStatus(t, e.put(alice, "deggen", t0, nil), 409, "ERR_STALE_CERTIFICATE")

	// Forward lookup returns the bare certificate array.
	w := e.do("GET", "/api/handle/deggen@example.com", nil)
	wantStatus(t, w, 200, "")
	var certs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &certs); err != nil || len(certs) != 1 {
		t.Fatalf("certs = %s", w.Body)
	}
	if certs[0]["subject"] != alice.PubKey().ToDERHex() || certs[0]["signature"] == nil {
		t.Errorf("cert = %v", certs[0])
	}
	if certs[0]["fields"].(map[string]any)["paymail"] != "deggen@example.com" {
		t.Errorf("fields = %v", certs[0]["fields"])
	}

	// Reverse lookup returns the same single document.
	w = e.do("GET", "/api/identityKey/"+alice.PubKey().ToDERHex(), nil)
	wantStatus(t, w, 200, "")
	var one map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one["serialNumber"] != certs[0]["serialNumber"] {
		t.Errorf("reverse = %s", w.Body)
	}
	wantStatus(t, e.do("GET", "/api/identityKey/"+bob.PubKey().ToDERHex(), nil), 404, "ERR_HANDLE_NOT_FOUND")
	wantStatus(t, e.do("GET", "/api/identityKey/nothex", nil), 400, "ERR_INVALID_LOOKUP")

	// Tombstone.
	e.clock = t0.Add(time.Hour)
	wantStatus(t, e.put(alice, "deggen", e.clock, map[string]string{"released": "true"}), 200, "")
	wantStatus(t, e.do("GET", "/api/identityKey/"+alice.PubKey().ToDERHex(), nil), 404, "ERR_HANDLE_NOT_FOUND")
	if w := e.do("GET", "/api/handle/deggen", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("search after release = %s", w.Body)
	}
	// Tombstone for a handle the key does not hold.
	wantStatus(t, e.put(bob, "nobody", e.clock, map[string]string{"released": "true"}), 404, "ERR_HANDLE_NOT_FOUND")

	// Cooldown blocks bob, not alice; the old cert cannot be replayed.
	wantStatus(t, e.put(bob, "deggen", e.clock.Add(time.Minute), nil), 409, "ERR_HANDLE_COOLDOWN")
	wantStatus(t, e.do("PUT", "/api/handle", same), 409, "ERR_STALE_CERTIFICATE")
	e.clock = e.clock.Add(31 * 24 * time.Hour)
	wantStatus(t, e.put(bob, "deggen", e.clock, nil), 201, "")
}

func TestPutHandle_Rejects(t *testing.T) {
	e := newLookupEnv(t)
	key := testKey(t)
	now := e.clock

	wantStatus(t, e.do("PUT", "/api/handle", []byte("{")), 400, "ERR_INVALID_CERTIFICATE")
	wantStatus(t, e.do("PUT", "/api/handle", bytes.Repeat([]byte("a"), 20000)), 413, "ERR_BODY_TOO_LARGE")
	wantStatus(t, e.do("PUT", "/api/handle", certtest.Profile(t, key, "deggen", "evil.com", now, nil)), 400, "ERR_WRONG_DOMAIN")
	wantStatus(t, e.put(key, "ab", now, nil), 400, "ERR_INVALID_HANDLE")
	wantStatus(t, e.put(key, "deg+gen", now, nil), 400, "ERR_INVALID_HANDLE")
	wantStatus(t, e.put(key, "adm1n", now, nil), 409, "ERR_HANDLE_RESERVED")

	var m map[string]any
	_ = json.Unmarshal(certtest.Profile(t, key, "deggen", testDomain, now, nil), &m)
	m["fields"].(map[string]any)["paymail"] = "victim@example.com"
	forged, _ := json.Marshal(m)
	wantStatus(t, e.do("PUT", "/api/handle", forged), 400, "ERR_INVALID_CERTIFICATE")
}

func TestSearch(t *testing.T) {
	e := newLookupEnv(t)
	for _, h := range []string{"deggen", "deg", "degas", "a_deg", "d3gx", "zed"} {
		wantStatus(t, e.put(testKey(t), h, e.clock, nil), 201, "")
	}
	for i := 0; i < 12; i++ {
		wantStatus(t, e.put(testKey(t), fmt.Sprintf("many%02d", i), e.clock, nil), 201, "")
	}

	search := func(q string) []string {
		t.Helper()
		w := e.do("GET", "/api/handle/"+q, nil)
		wantStatus(t, w, 200, "")
		var certs []struct {
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &certs); err != nil {
			t.Fatalf("%s: %v", w.Body, err)
		}
		out := []string{}
		for _, c := range certs {
			out = append(out, strings.TrimSuffix(c.Fields["paymail"], "@"+testDomain))
		}
		return out
	}
	eq := func(label string, got, want []string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
	}

	// Tiers: exact, handle prefix, skeleton prefix, substring (3+ chars).
	eq("deg", search("deg"), []string{"deg", "degas", "deggen", "a_deg"})
	eq("upper + domain", search("DEG@Example.com"), []string{"deg", "degas", "deggen", "a_deg"})
	eq("skeleton typo", search("de9gen"), []string{})
	eq("skeleton fold", search("d.e.g.g.e.n"), []string{"deggen"})
	eq("lookalike digits", search("z3d"), []string{})
	eq("fold 2->z", search("2ed"), []string{"zed"})
	eq("too short", search("d"), []string{})
	eq("other domain", search("deg@evil.com"), []string{})
	eq("bad chars", search("de%25"), []string{})
	if got := search("many"); len(got) != 10 {
		t.Errorf("limit: %d results, want 10", len(got))
	}
	// Substring tier needs three characters.
	eq("two-char substring", search("_d"), []string{})
}

func TestAvailable(t *testing.T) {
	e := newLookupEnv(t)
	key := testKey(t)
	wantStatus(t, e.put(key, "deggen", e.clock, nil), 201, "")

	check := func(handle string, available bool, reason string) {
		t.Helper()
		w := e.do("GET", "/api/handle/available/"+handle, nil)
		wantStatus(t, w, 200, "")
		var r AvailabilityResponse
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		if r.Available != available || r.Reason != reason {
			t.Errorf("%s = %+v, want %v %q", handle, r, available, reason)
		}
	}
	check("free", true, "")
	check("deggen", false, "taken")
	check("d3ggen", false, "too_similar")
	check("adm1n", false, "reserved")
	check("x", false, "invalid")

	e.clock = e.clock.Add(time.Hour)
	wantStatus(t, e.put(key, "deggen", e.clock, map[string]string{"released": "true"}), 200, "")
	check("deggen", false, "cooldown")
	check("d3ggen", false, "too_similar")
	e.clock = e.clock.Add(31 * 24 * time.Hour)
	check("deggen", true, "")
}

func TestPutHandle_ConcurrentOneWinner(t *testing.T) {
	e := newLookupEnv(t)
	const n = 12
	codes := make(chan int, n)
	// Twelve distinct handles, one skeleton ("paypal").
	variants := []string{"paypal", "pay.pal", "pay-pal", "pay_pal", "p.aypal", "pa.ypal",
		"payp.al", "paypa.l", "paypa1", "paypai", "p-aypal", "pa-ypal"}
	for _, h := range variants {
		body := certtest.Profile(t, testKey(t), h, testDomain, e.clock, nil)
		go func() { codes <- e.do("PUT", "/api/handle", body).Code }()
	}
	created := 0
	for i := 0; i < n; i++ {
		if c := <-codes; c == 201 {
			created++
		} else if c != 409 {
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("%d created, want 1", created)
	}
}
```

`TestSearch` expectations were derived by hand from the tier rules (exact →
handle prefix → skeleton prefix → substring when `len(q) ≥ 3`; de-duplicated;
each tier ordered by handle). The `many00`…`many11` handles have pairwise
distinct skeletons. If a test fails, fix the handler, not the expectation.

Run: `go test ./pkg/handlers/ -run 'TestWellKnown|TestPutHandle|TestSearch|TestAvailable'` → FAIL (undefined).

- [ ] **Step 2: Server plumbing** (`helpers.go`)

```go
// Server holds shared dependencies for all handlers.
type Server struct {
	Store  storage.Store
	wallet sdk.Interface

	lookup  *LookupConfig
	handles storage.HandleStore // nil unless EnableLookup was called
	now     func() time.Time    // test seam; nil means time.Now
}
```

- [ ] **Step 3: Implement `lookup.go`**

```go
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handles"
	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// LookupConfig switches on the paymail profile lookup routes.
type LookupConfig struct {
	Domain    string        // paymail domain served, lowercase
	Host      string        // public base URL, no trailing slash
	Cooldown  time.Duration // before another key may claim a released handle
	AdminKeys []string      // identity keys allowed to release on a user's behalf
}

const (
	searchLimit      = 10
	searchMinLength  = 2
	substringMinimum = 3
)

var (
	pubkeyRE = regexp.MustCompile(`^0[23][0-9a-f]{64}$`)
	queryRE  = regexp.MustCompile(`^[a-z0-9._-]+$`)
)

// AvailabilityResponse is the body of GET /api/handle/available/{handle}.
type AvailabilityResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // taken | too_similar | reserved | invalid | cooldown
}

// EnableLookup turns the lookup feature on. The handle registry is passed
// separately because it is not part of storage.Store: only the Mongo backend
// implements it.
func (s *Server) EnableLookup(c LookupConfig, hs storage.HandleStore) {
	s.lookup, s.handles = &c, hs
}

// clock is the current time at the precision every backend stores.
func (s *Server) clock() time.Time {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	return now().UTC().Truncate(time.Millisecond)
}

// LookupRoutes returns the unauthenticated lookup routes. EnableLookup must
// have been called.
func (s *Server) LookupRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/bsvalias", s.WellKnown)
	mux.HandleFunc("PUT /api/handle", s.PutHandle)
	mux.HandleFunc("GET /api/handle/available/{handle}", s.HandleAvailable)
	mux.HandleFunc("GET /api/handle/{query}", s.SearchHandles)
	mux.HandleFunc("GET /api/identityKey/{pubkey}", s.LookupIdentityKey)
	return mux
}

// WellKnown serves the paymail capability document.
// @Summary Paymail capability discovery
// @Tags lookup
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /.well-known/bsvalias [get]
func (s *Server) WellKnown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"bsvalias": "1.0",
		"capabilities": map[string]string{
			handles.BRFCLookup:        s.lookup.Host + "/api/handle/{query}",
			handles.BRFCReverseLookup: s.lookup.Host + "/api/identityKey/{pubkey}",
		},
	})
}

// writeCertificates writes stored certificate JSON verbatim.
func writeCertificates(w http.ResponseWriter, certs []string) {
	raw := make([]json.RawMessage, len(certs))
	for i, c := range certs {
		raw[i] = json.RawMessage(c)
	}
	writeJSON(w, http.StatusOK, raw)
}

var claimErrors = []struct {
	err  error
	code string
	text string
}{
	{storage.ErrHandleTaken, "ERR_HANDLE_TAKEN", "That handle belongs to another identity key."},
	{storage.ErrHandleTooSimilar, "ERR_HANDLE_TOO_SIMILAR", "That handle looks too much like an existing one."},
	{storage.ErrKeyHasHandle, "ERR_KEY_HAS_HANDLE", "This identity key already has a handle; release it first."},
	{storage.ErrHandleCooldown, "ERR_HANDLE_COOLDOWN", "That handle was released recently and is not yet available."},
	{storage.ErrStaleCertificate, "ERR_STALE_CERTIFICATE", "issuedAt and serialNumber must be newer than the stored certificate."},
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrHandleNotFound) {
		writeError(w, http.StatusNotFound, "ERR_HANDLE_NOT_FOUND", "No active handle.")
		return
	}
	for _, e := range claimErrors {
		if errors.Is(err, e.err) {
			writeError(w, http.StatusConflict, e.code, e.text)
			return
		}
	}
	logger.Error("handle store failure", "error", err)
	writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "Storage failure; safe to retry.")
}

// PutHandle registers, updates or releases a handle. The certificate is the
// authorisation: it is signed by the identity key and names the handle.
// @Summary Register, update or release a handle
// @Tags lookup
// @Accept json
// @Produce json
// @Param certificate body map[string]interface{} true "Self-signed profile certificate"
// @Success 200 {object} map[string]interface{}
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /api/handle [put]
func (s *Server) PutHandle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, profilecert.MaxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE", "Certificate larger than 16 KB.")
		return
	}
	p, err := profilecert.Parse(r.Context(), body, s.lookup.Domain)
	if errors.Is(err, profilecert.ErrWrongDomain) {
		writeError(w, http.StatusBadRequest, "ERR_WRONG_DOMAIN", "paymail field must end with @"+s.lookup.Domain)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_CERTIFICATE", err.Error())
		return
	}
	switch err := handles.Validate(p.Handle); {
	case errors.Is(err, handles.ErrReservedHandle):
		writeError(w, http.StatusConflict, "ERR_HANDLE_RESERVED", "That handle is reserved.")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "ERR_INVALID_HANDLE", "Handles are 3-32 characters of a-z 0-9 . _ - and start and end with a letter or digit.")
		return
	}

	now := s.clock()
	if p.Released {
		until := now.Add(s.lookup.Cooldown)
		err := s.handles.ReleaseHandle(r.Context(), storage.HandleRelease{
			Handle: p.Handle, Owner: &p.IdentityKey, IssuedAt: &p.IssuedAt,
			ReleasedBy: "owner", CooldownUntil: &until, Now: now,
		})
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, json.RawMessage(p.JSON))
		return
	}

	res, err := s.handles.ClaimHandle(r.Context(), storage.HandleClaim{
		Handle: p.Handle, Skeleton: handles.Skeleton(p.Handle), IdentityKey: p.IdentityKey,
		Certificate: p.JSON, SerialNumber: p.SerialNumber, IssuedAt: p.IssuedAt, Now: now,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if res == storage.ClaimCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, json.RawMessage(p.JSON))
}

// normaliseQuery lowercases q and strips an @domain suffix. ok is false when
// the query can match nothing on this server.
func (s *Server) normaliseQuery(q string) (string, bool) {
	q = strings.ToLower(strings.TrimSpace(q))
	if local, dom, found := strings.Cut(q, "@"); found {
		// A partially typed domain is fine; a different one is not.
		if !strings.HasPrefix(s.lookup.Domain, dom) {
			return "", false
		}
		q = local
	}
	if len(q) < searchMinLength || len(q) > 32 || !queryRE.MatchString(q) {
		return "", false
	}
	return q, true
}

// searchTiers lists the matches to try, best first.
func searchTiers(q string) []storage.HandleMatch {
	tiers := []storage.HandleMatch{{Field: storage.HandleFieldHandle, Mode: storage.HandleMatchPrefix, Value: q}}
	if sk := handles.Skeleton(q); len(sk) >= searchMinLength {
		tiers = append(tiers, storage.HandleMatch{Field: storage.HandleFieldSkeleton, Mode: storage.HandleMatchPrefix, Value: sk})
	}
	if len(q) >= substringMinimum {
		tiers = append(tiers, storage.HandleMatch{Field: storage.HandleFieldHandle, Mode: storage.HandleMatchContains, Value: q})
	}
	return tiers
}

func (s *Server) search(ctx context.Context, q string) ([]string, error) {
	certs := []string{}
	seen := map[string]bool{}
	add := func(r storage.HandleRecord) {
		if !seen[r.Handle] && r.Certificate != nil && len(certs) < searchLimit {
			seen[r.Handle] = true
			certs = append(certs, *r.Certificate)
		}
	}
	// The exact handle leads even when ten other handles share the prefix.
	exact, err := s.handles.GetHandle(ctx, q)
	if err != nil {
		return nil, err
	}
	if exact != nil && exact.Active() {
		add(*exact)
	}
	for _, m := range searchTiers(q) {
		if len(certs) >= searchLimit {
			break
		}
		m.Limit = searchLimit
		recs, err := s.handles.FindHandles(ctx, m)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			add(r)
		}
	}
	return certs, nil
}

// SearchHandles returns profile certificates whose handle matches the query.
// @Summary Search handles (capability 0ace65da5987)
// @Tags lookup
// @Produce json
// @Param query path string true "Partial or full handle, optionally @domain"
// @Success 200 {array} map[string]interface{}
// @Router /api/handle/{query} [get]
func (s *Server) SearchHandles(w http.ResponseWriter, r *http.Request) {
	q, ok := s.normaliseQuery(r.PathValue("query"))
	if !ok {
		writeCertificates(w, nil)
		return
	}
	certs, err := s.search(r.Context(), q)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeCertificates(w, certs)
}

// LookupIdentityKey returns the profile certificate of an identity key.
// @Summary Reverse lookup (capability 43dcf83ddc5f)
// @Tags lookup
// @Produce json
// @Param pubkey path string true "Compressed public key, hex"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} ErrorResponse
// @Router /api/identityKey/{pubkey} [get]
func (s *Server) LookupIdentityKey(w http.ResponseWriter, r *http.Request) {
	key := strings.ToLower(r.PathValue("pubkey"))
	if !pubkeyRE.MatchString(key) {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_LOOKUP", "Expected a 33-byte compressed public key in hex.")
		return
	}
	rec, err := s.handles.GetHandleByIdentityKey(r.Context(), key)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if rec == nil || rec.Certificate == nil {
		s.writeStoreError(w, storage.ErrHandleNotFound)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(*rec.Certificate))
}

// HandleAvailable is an advisory pre-check; PutHandle is the source of truth.
// @Summary Check whether a handle can be registered
// @Tags lookup
// @Produce json
// @Param handle path string true "Handle"
// @Success 200 {object} AvailabilityResponse
// @Router /api/handle/available/{handle} [get]
func (s *Server) HandleAvailable(w http.ResponseWriter, r *http.Request) {
	handle := strings.ToLower(r.PathValue("handle"))
	reason, err := s.unavailableReason(r.Context(), handle)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AvailabilityResponse{Available: reason == "", Reason: reason})
}

func (s *Server) unavailableReason(ctx context.Context, handle string) (string, error) {
	switch err := handles.Validate(handle); {
	case errors.Is(err, handles.ErrReservedHandle):
		return "reserved", nil
	case err != nil:
		return "invalid", nil
	}
	rec, err := s.handles.GetHandleBySkeleton(ctx, handles.Skeleton(handle))
	if err != nil || rec == nil {
		return "", err
	}
	switch {
	case rec.Handle != handle:
		return "too_similar", nil
	case rec.Active():
		return "taken", nil
	case rec.CooldownUntil != nil && rec.CooldownUntil.After(s.clock()):
		return "cooldown", nil
	}
	return "", nil
}
```

Design notes the implementer must keep:
- `writeCertificates(w, nil)` must emit `[]`, not `null` — `make([]json.RawMessage, 0)` does. Verify in `TestSearch`.
- Stored certificate JSON is served verbatim (`json.RawMessage`) so signatures survive byte-for-byte.
- Percent-encoded path values arrive decoded in `PathValue`; `queryRE` rejects `%`.

- [ ] **Step 4: Run**

Run: `go test ./pkg/handlers/ -v -run 'TestWellKnown|TestPutHandle|TestSearch|TestAvailable' 2>&1 | tail -30`
Expected: PASS. Then `go test -race ./pkg/handlers/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/handlers && git commit -m "feat(handlers): paymail profile lookup routes"
```

---

### Task 9: admin release

**Files:**
- Create: `pkg/handlers/admin_handle.go`
- Test: `pkg/handlers/admin_handle_test.go`

**Interfaces:**
- Consumes: `getIdentityKey(r)`, `s.lookup.AdminKeys`, `storage.HandleRelease`.
- Produces: `func (s *Server) AdminReleaseHandle(w, r)` (route handler) and unexported `func (s *Server) adminReleaseHandle(w http.ResponseWriter, r *http.Request, caller string)` (testable core — the auth middleware identity cannot be faked in tests).

- [ ] **Step 1: Failing test**

```go
package handlers

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminReleaseHandle(t *testing.T) {
	e := newLookupEnv(t) // AdminKeys = [mockIdentityKey]
	alice, bob := testKey(t), testKey(t)
	wantStatus(t, e.put(alice, "deggen", e.clock, nil), 201, "")

	call := func(caller, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		e.srv.adminReleaseHandle(w, httptest.NewRequest("POST", "/admin/handle/release", bytes.NewBufferString(body)), caller)
		return w
	}

	wantStatus(t, call("", `{"handle":"deggen"}`), 401, "ERR_AUTH_REQUIRED")
	wantStatus(t, call(bob.PubKey().ToDERHex(), `{"handle":"deggen"}`), 403, "ERR_NOT_ADMIN")
	wantStatus(t, call(mockIdentityKey, `{`), 400, "ERR_INVALID_REQUEST")
	wantStatus(t, call(mockIdentityKey, `{"handle":"nobody"}`), 404, "ERR_HANDLE_NOT_FOUND")

	// Default skips the cooldown: the user's new key claims at once.
	wantStatus(t, call(mockIdentityKey, `{"handle":"Deggen"}`), 200, "")
	e.clock = e.clock.Add(time.Minute)
	wantStatus(t, e.put(bob, "deggen", e.clock, nil), 201, "")

	// skipCooldown:false applies it.
	wantStatus(t, call(mockIdentityKey, `{"handle":"deggen","skipCooldown":false}`), 200, "")
	e.clock = e.clock.Add(time.Minute)
	wantStatus(t, e.put(alice, "deggen", e.clock, nil), 409, "ERR_HANDLE_COOLDOWN")

	// The no-auth route wrapper rejects.
	w := httptest.NewRecorder()
	e.srv.AdminReleaseHandle(w, httptest.NewRequest("POST", "/admin/handle/release", bytes.NewBufferString(`{"handle":"deggen"}`)))
	wantStatus(t, w, 401, "ERR_AUTH_REQUIRED")
}
```

Run → FAIL (undefined).

- [ ] **Step 2: Implement**

```go
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// AdminReleaseRequest is the body of POST /admin/handle/release.
type AdminReleaseRequest struct {
	Handle string `json:"handle"`
	// SkipCooldown defaults to true: the usual case is a user who lost their
	// key and, after out-of-band verification, re-registers with a new one.
	SkipCooldown *bool `json:"skipCooldown,omitempty"`
}

// AdminReleaseHandle releases a handle on the operator's authority.
// @Summary Operator release of a handle (lost keys)
// @Tags lookup
// @Accept json
// @Produce json
// @Param request body AdminReleaseRequest true "Handle to release"
// @Success 200 {object} map[string]string
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BSVAuth
// @Router /admin/handle/release [post]
func (s *Server) AdminReleaseHandle(w http.ResponseWriter, r *http.Request) {
	s.adminReleaseHandle(w, r, getIdentityKey(r))
}

func (s *Server) adminReleaseHandle(w http.ResponseWriter, r *http.Request, caller string) {
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "ERR_AUTH_REQUIRED", "Authentication required.")
		return
	}
	if s.lookup == nil || s.handles == nil || !slices.Contains(s.lookup.AdminKeys, strings.ToLower(caller)) {
		writeError(w, http.StatusForbidden, "ERR_NOT_ADMIN", "This identity key may not release handles.")
		return
	}
	var req AdminReleaseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Handle == "" {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_REQUEST", "Expected {\"handle\": \"...\"}.")
		return
	}
	handle := strings.ToLower(strings.TrimSpace(req.Handle))

	prev, err := s.handles.GetHandle(r.Context(), handle)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	now := s.clock()
	rel := storage.HandleRelease{Handle: handle, ReleasedBy: caller, Now: now}
	if req.SkipCooldown != nil && !*req.SkipCooldown {
		until := now.Add(s.lookup.Cooldown)
		rel.CooldownUntil = &until
	}
	if err := s.handles.ReleaseHandle(r.Context(), rel); err != nil {
		s.writeStoreError(w, err)
		return
	}

	previousKey := ""
	if prev != nil && prev.IdentityKey != nil {
		previousKey = *prev.IdentityKey
	}
	// Always logged, not only in development: this is the audit trail.
	slog.Warn("admin released handle", "handle", handle, "admin", caller, "previousIdentityKey", previousKey, "cooldown", rel.CooldownUntil != nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success", "handle": handle})
}
```

Check the `@Security` annotation name against an existing authed handler
(e.g. `send_message.go`) and copy theirs.

- [ ] **Step 3: Run + commit**

Run: `go test ./pkg/handlers/` → PASS.

```bash
git add pkg/handlers/admin_handle.go pkg/handlers/admin_handle_test.go && git commit -m "feat(handlers): operator release of handles"
```

---

### Task 10: wiring, docs, end-to-end check

**Files:**
- Modify: `cmd/server/main.go` (after `srv := handlers.NewServer(...)`, and at `rootMux`)
- Modify: `README.md`
- Regenerate: `docs/` swagger (if `swag` installed)

- [ ] **Step 1: Wire**

After the existing `mux.HandleFunc(...)` block and before `rootMux.Handle("/", ...)`.
`mbstorage` is the existing import alias of `pkg/storage` in `main.go`:

```go
	lookupEnabled := cfg.PaymailDomain != ""
	if lookupEnabled {
		// The handle registry exists only on the Mongo backend.
		registry, ok := store.(mbstorage.HandleStore)
		if !ok {
			slog.Error("PAYMAIL_DOMAIN requires STORAGE_BACKEND=mongo", "backend", cfg.StorageBackend)
			os.Exit(1)
		}
		srv.EnableLookup(handlers.LookupConfig{
			Domain:    cfg.PaymailDomain,
			Host:      cfg.PaymailHost,
			Cooldown:  cfg.HandleCooldown,
			AdminKeys: cfg.AdminIdentityKeys,
		}, registry)
		mux.HandleFunc("POST "+prefix+"/admin/handle/release", srv.AdminReleaseHandle)
	}
```

and next to the swagger mount on `rootMux` (ServeMux picks the most specific
pattern regardless of registration order):

```go
	// Paymail profile lookup: public by design, so outside auth and payment.
	if lookupEnabled {
		public := handlers.NewRateLimiter(cfg.LookupRatePerMin, cfg.TrustProxy).Wrap(srv.LookupRoutes())
		rootMux.Handle("/.well-known/bsvalias", public)
		rootMux.Handle("/api/handle", public)
		rootMux.Handle("/api/handle/", public)
		rootMux.Handle("/api/identityKey/", public)
		slog.Info("paymail profile lookup enabled", "domain", cfg.PaymailDomain)
	}
```

The `os.Exit(1)` must happen before `ListenAndServe` and after `defer store.Close()`
is registered — note `os.Exit` skips defers; mirror how the existing fatal paths
in `main()` handle that (they also `os.Exit(1)` after `slog.Error`). Update the
stack comment to `CORS -> rootMux -> (public lookup | Auth -> Payment -> Routes)`.

- [ ] **Step 2: Build + end-to-end against local MongoDB**

```bash
go build ./... && go vet ./... && go test -race ./...
MONGO_TEST_URI=mongodb://localhost:27017 MONGO_TEST_DATABASE=messagebox_lookup_test go test -race ./pkg/storage/mongostore/
```
Expected: PASS, Mongo suite not skipped.

Write a throwaway Go program under the scratch dir given in your task prompt
(NOT in the repo) that uses `certtest`-equivalent code (go-sdk
`certificates.NewCertificate` + `Sign`) to print a signed profile certificate
JSON for a random key, handle `e2euser`, domain `example.com`, `issuedAt` now —
or simpler: add nothing to the repo and drive the e2e from a Go test file placed
in the scratch dir with a `replace`-free `go run` inside the repo module
(`go run /abs/scratch/mkcert.go` works when run from the repo root because the
file is compiled in the repo's module context).

Run the server on Mongo in the background (use a free port, e.g. 18080):

```bash
SERVER_PRIVATE_KEY=$(openssl rand -hex 32) STORAGE_BACKEND=mongo MONGO_URI=mongodb://localhost:27017 \
  MONGO_DATABASE=messagebox_lookup_e2e PAYMAIL_DOMAIN=example.com PAYMAIL_HOST=http://localhost:18080 \
  PORT=18080 NODE_ENV=development go run ./cmd/server
```
```bash
curl -s localhost:18080/.well-known/bsvalias
curl -s -X PUT --data-binary @cert.json -w '\n%{http_code}\n' localhost:18080/api/handle     # 201
curl -s -X PUT --data-binary @cert.json -w '\n%{http_code}\n' localhost:18080/api/handle     # 200 (unchanged)
curl -s localhost:18080/api/handle/e2e                                                       # [ {cert} ]
curl -s localhost:18080/api/handle/available/e2euser                                         # taken
curl -s localhost:18080/api/handle/available/e2eu5er                                         # too_similar
curl -s localhost:18080/api/identityKey/<subject from cert.json>                             # {cert}
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:18080/listMessages                # 401
```
Then restart with `STORAGE_BACKEND=sql DB_SOURCE=<scratch>/x.db` and
`PAYMAIL_DOMAIN` still set: the process must exit non-zero with
`PAYMAIL_DOMAIN requires STORAGE_BACKEND=mongo`. Then restart on mongo without
`PAYMAIL_DOMAIN`: `/.well-known/bsvalias` must hit the auth wall (401), proving
the feature is off. Stop every server you started; drop the e2e database:
`mongosh --quiet mongodb://localhost:27017/messagebox_lookup_e2e --eval 'db.dropDatabase()'`.
Paste the actual outputs in your report.

- [ ] **Step 3: README**

Add a `## Paymail profile lookup` section containing: what it is (2 sentences +
link to the spec); that it **requires `STORAGE_BACKEND=mongo`** (production:
MongoDB Atlas `mongodb+srv://…` URI in `MONGO_URI`; local dev: MongoDB Community
via `docker compose --profile mongo up -d mongo`); the env var table from the spec; the route table
(method, path, auth, purpose); the DNS block:

```
_bsvalias._tcp.example.com. 3600 IN SRV 10 10 443 mb.example.com.
```
plus: the zone must be DNSSEC-signed, clients reject unsigned SRV answers;
`PAYMAIL_HOST` must be the HTTPS origin the SRV target serves; without an SRV
record clients fall back to `https://example.com:443/.well-known/bsvalias`.
Add the client verification checklist verbatim from the spec, and a note that
the rate limiter is per-replica and needs `TRUST_PROXY=true` behind a load
balancer.

- [ ] **Step 4: Swagger**

`swag` is installed at `/Users/personal/go/bin/swag` (v1.16.x). Run
`swag init -g cmd/server/main.go -o docs` from the repo root (check README / git
log for the exact flags previously used and match them). Confirm the five new
public routes and the admin route appear in `docs/swagger.json`. Do not
hand-edit generated files.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go README.md docs && git commit -m "feat: mount paymail profile lookup; docs"
```

---

## Self-Review Notes

- Spec coverage: capabilities/BRFC (T1, T8), cert rules 1–6 (T2), handle rules (T1), storage + FCFS + cooldown + replay guard (T3, T5; Mongo only), config (T6), rate limit + body cap (T7, T8), all five public routes (T8), admin release + audit (T9), feature-off + mounting outside auth (T10), README DNS + client verification (T10). Out-of-scope items untouched.
- `serialNumber must differ on update` maps to `ErrStaleCertificate` (spec lists no separate code).
- `ERR_BODY_TOO_LARGE` (413) and `ERR_RATE_LIMITED` (429), `ERR_INVALID_LOOKUP`, `ERR_NOT_ADMIN`, `ERR_INTERNAL` are the concrete codes for statuses the spec gives without names.
- Partial `@domain` while typing (`deg@exa`) is accepted via prefix match on the configured domain — a small extension of the spec's "strip `@domain`", needed for as-you-type search.

---

## Post-implementation review amendments (2026-09-18)

A whole-branch review changed behaviour the task bodies above prescribe. The
spec is the normative record; these are the points where the code blocks in
this plan no longer describe what ships.

- **`issuedAt` is bounded against the server clock** (`profilecert.Parse` now
  takes a `now`; `MaxClockSkew` is five minutes). Unbounded, one unauthenticated
  `PUT` dated in the far future left a handle and its whole look-alike class
  permanently unclaimable, by its owner and by an operator alike, because every
  write has to beat the stored value and the last instant RFC 3339 can spell
  cannot be beaten. Spec rule 4 records the bound.
- **The rate limiter keys on a trusted hop, not the leftmost `X-Forwarded-For`
  entry.** Proxies append rather than rewrite, so the leftmost entry is
  client-controlled: as written, the limiter could be bypassed entirely and used
  to spend another client's budget. `TRUSTED_PROXY_HOPS` (default 1) says how
  far from the right to count, and `NewRateLimiter` takes it.
- **A cooldown only lets back in the key that released the handle itself**
  (`lastIdentityKey = caller AND releasedBy = 'owner'`). An operator release
  carrying a cooldown previously parked the handle against everyone *except* the
  key the operator was removing. `lastIdentityKey` is left alone, so the row
  stays an audit record.
- **`ClaimUnchanged` compares the certificate**, not just serial and `issuedAt`:
  the old rule answered `200` with the submitted document for an edit the
  registry discarded.
- **Replaying the owner tombstone a row already carries is a `200` no-op**
  rather than `404`. `RunHandleStoreTests`'s double-release case is unchanged —
  it releases with a *newer* `issuedAt`, which is still not found.
- **A malformed release** (owner set, no `issuedAt`) is `ErrInvalidRelease` on
  every backend, rather than a plain error on Mongo and `ErrStaleCertificate` on
  the fake.
- **A claim refused while the diagnosis finds no conflict retries once**
  (`ErrClaimRaced`) instead of surfacing a driver error as `500`.
- **A zero `HandleMatch.Mode` is the anchored search on both backends**; Mongo
  read it as a substring search.
- **Availability answers `stale`** for a released row whose `issuedAt` is not yet
  past, instead of calling it free.
- **The mount decision lives in `cmd/server/lookup.go`** (`checkLookupBackend`,
  `lookupRegistry`, `mountLookup`) so the feature-off and Mongo-only rules have
  tests; `main` only calls them.

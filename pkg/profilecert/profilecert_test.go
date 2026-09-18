package profilecert

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth/certificates"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
)

const domain = "example.com"

// otherType is a valid 32-byte certificate type that is not the profile type.
// It must not be all zeroes: the go-sdk serializer refuses to serialize (and so
// to sign) a certificate whose type is the zero array.
const otherType = "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func newKey(t *testing.T) *ec.PrivateKey {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// wireJSON re-serializes body the way a JSON.stringify client does: with '<',
// '>' and '&' literal rather than escaped. The signature covers the decoded
// field values, not their JSON encoding, so the certificate stays valid.
func wireJSON(t *testing.T, body []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func TestParse_Valid(t *testing.T) {
	key := newKey(t)
	at := time.Date(2026, 9, 18, 12, 0, 0, 123_456_789, time.UTC)
	body := certtest.Profile(t, key, "deggen", domain, at, map[string]string{
		"displayName": "Deggen <dev & co>", // the characters json.Marshal escapes
		// One byte under the field-name limit, so the limit is pinned both ways.
		strings.Repeat("n", maxFieldName-1): "v",
	})

	p, err := Parse(context.Background(), body, domain, at)
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
	if err := json.Unmarshal([]byte(p.JSON), &round); err != nil {
		t.Fatalf("JSON not a certificate: %v", err)
	}
	if sig, _ := round["signature"].(string); sig == "" {
		t.Error("canonical JSON lost its signature")
	}
	if round["type"] != Type {
		t.Errorf("canonical type = %v", round["type"])
	}
	if len(p.JSON) > MaxBody {
		t.Errorf("canonical JSON = %d bytes, want <= %d", len(p.JSON), MaxBody)
	}
	// The canonical JSON is what later tasks store and serve, so re-parsing it
	// must yield the same profile.
	again, err := Parse(context.Background(), []byte(p.JSON), domain, at)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if *again != *p {
		t.Errorf("re-parse = %+v, want %+v", *again, *p)
	}
}

// A JSON.stringify client (@bsv/sdk) sends '<', '>' and '&' literally, and
// json.Marshal expands each to a 6-byte escape. Canonicalising with it turns an
// accepted body into a document several times MaxBody that Parse then refuses —
// and that later tasks would store and serve.
func TestParse_CanonicalJSONNotEscaped(t *testing.T) {
	key := newKey(t)
	extra := map[string]string{}
	for i := 0; i < 14; i++ { // 14 KB of '<' is ~86 KB once escaped
		extra["f"+strconv.Itoa(i)] = strings.Repeat("<", maxFieldValue)
	}
	now := time.Now()
	body := wireJSON(t, certtest.Profile(t, key, "deggen", domain, now, extra))
	if len(body) > MaxBody {
		t.Fatalf("test body = %d bytes, want <= %d", len(body), MaxBody)
	}

	p, err := Parse(context.Background(), body, domain, now)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.JSON) > MaxBody {
		t.Errorf("canonical JSON = %d bytes from a %d byte body, want <= %d", len(p.JSON), len(body), MaxBody)
	}
	if !strings.Contains(p.JSON, strings.Repeat("<", maxFieldValue)) {
		t.Error("canonical JSON escaped the field values")
	}
	if _, err := Parse(context.Background(), []byte(p.JSON), domain, now); err != nil {
		t.Errorf("re-parse of canonical JSON: %v", err)
	}
}

// Parse is the write-path authority, so it must not fall back to accepting any
// domain when the caller passes none.
func TestParse_NoDomain(t *testing.T) {
	key := newKey(t)
	now := time.Now()
	body := certtest.Profile(t, key, "deggen", domain, now, nil)
	if _, err := Parse(context.Background(), body, "", now); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// A tombstone is the one field whose value destroys a registration, so only the
// exact string may carry it: a client that spells the field out on every
// publish must not release the handle it is registering.
func TestParse_Released(t *testing.T) {
	key := newKey(t)
	now := time.Now()
	ctx := context.Background()

	for _, c := range []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"false", false}, {"0", false}, {"1", false}, {"no", false}, {"yes", false},
		{"TRUE", false}, {"True", false}, {" true", false}, {"true ", false}, {"", false},
	} {
		t.Run("released="+strconv.Quote(c.value), func(t *testing.T) {
			body := certtest.Profile(t, key, "deggen", domain, now, map[string]string{"released": c.value})
			p, err := Parse(ctx, body, domain, now)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.Released != c.want {
				t.Errorf("Released = %v, want %v", p.Released, c.want)
			}
		})
	}
	t.Run("field absent", func(t *testing.T) {
		p, err := Parse(ctx, certtest.Profile(t, key, "deggen", domain, now, nil), domain, now)
		if err != nil || p.Released {
			t.Fatalf("Released = %v, err = %v", p != nil && p.Released, err)
		}
	})
}

// issuedAt is the registry's replay guard, and every later write has to beat the
// stored value, so a certificate from the future is not merely early: it settles
// every claim that handle will ever see.
func TestParse_RejectsFutureIssuedAt(t *testing.T) {
	key := newKey(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	at := func(d time.Duration) time.Time { return now.Add(d) }
	for _, c := range []struct {
		name     string
		issuedAt time.Time
		ok       bool
	}{
		{"an hour old", at(-time.Hour), true},
		{"exactly now", now, true},
		{"the whole skew allowance", at(MaxClockSkew), true},
		{"a second past the allowance", at(MaxClockSkew + time.Second), false},
		{"a day ahead", at(24 * time.Hour), false},
		{"a century ahead", now.AddDate(100, 0, 0), false},
		// The last instant RFC 3339's four-digit year can spell: a row stamped
		// with it can never be beaten, so nothing may ever store it.
		{"the end of representable time", time.Date(9999, 12, 31, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := certtest.Profile(t, key, "deggen", domain, c.issuedAt, nil)
			_, err := Parse(ctx, body, domain, now)
			if c.ok && err != nil {
				t.Fatalf("Parse = %v, want accepted", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse = %v, want ErrInvalid", err)
			}
		})
	}

	// A negative offset denotes an instant later than the same digits in UTC, so
	// the bound has to be on the instant rather than on how the date is written.
	t.Run("late instant behind an offset", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{
			"paymail": "deggen@" + domain, "issuedAt": "9999-12-31T23:59:59.999-23:59",
		})
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})

	// A caller that forgets the clock rejects everything rather than trusting
	// whatever a certificate claims.
	t.Run("zero clock accepts nothing", func(t *testing.T) {
		body := certtest.Profile(t, key, "deggen", domain, now, nil)
		if _, err := Parse(ctx, body, domain, time.Time{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
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
		// Trailing bytes that are not JSON: the decoder rejects these whatever
		// the body bound does, which is why the bound has its own case below.
		{"trailing junk", append(certtest.Profile(t, key, "deggen", domain, now, nil), make([]byte, 16)...), ErrInvalid},
		{"bad signature", tamper(func(m map[string]any) {
			m["fields"].(map[string]any)["displayName"] = "Mallory"
		}), ErrInvalid},
		{"tampered subject", tamper(func(m map[string]any) {
			m["subject"] = other.PubKey().ToDERHex()
		}), ErrInvalid},
		// An absent key (unlike a null one) never reaches PublicKey.UnmarshalJSON,
		// leaving a zero key whose X/Y are nil.
		{"missing subject", tamper(func(m map[string]any) {
			delete(m, "subject")
		}), ErrInvalid},
		{"missing certifier", tamper(func(m map[string]any) {
			delete(m, "certifier")
		}), ErrInvalid},
		{"missing subject and certifier", tamper(func(m map[string]any) {
			delete(m, "subject")
			delete(m, "certifier")
		}), ErrInvalid},
		// Pins the nil outpoint check ahead of Verify: the serializer
		// dereferences the outpoint.
		{"nil revocation outpoint", tamper(func(m map[string]any) {
			m["revocationOutpoint"] = nil
		}), ErrInvalid},
		{"missing revocation outpoint", tamper(func(m map[string]any) {
			delete(m, "revocationOutpoint")
		}), ErrInvalid},
		{"missing paymail", certtest.New(t, key, map[string]string{"issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		{"missing issuedAt", certtest.New(t, key, map[string]string{"paymail": "deggen@" + domain}), ErrInvalid},
		{"bad issuedAt", certtest.New(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": "yesterday"}), ErrInvalid},
		{"uppercase paymail", certtest.New(t, key, map[string]string{"paymail": "Deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		{"no at sign", certtest.New(t, key, map[string]string{"paymail": "deggen", "issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		// Malformed, not another domain: there is no domain to be wrong about.
		{"empty paymail domain", certtest.New(t, key, map[string]string{"paymail": "deggen@", "issuedAt": now.UTC().Format(time.RFC3339)}), ErrInvalid},
		{"wrong domain", certtest.Profile(t, key, "deggen", "evil.com", now, nil), ErrWrongDomain},
		{"too many fields", certtest.Profile(t, key, "deggen", domain, now, manyFields), ErrInvalid},
		{"field too large", certtest.Profile(t, key, "deggen", domain, now, map[string]string{"bio": strings.Repeat("a", 1025)}), ErrInvalid},
		// go-sdk names the type CertificateFieldNameUnder50Bytes: 50 is already over.
		{"field name too large", certtest.Profile(t, key, "deggen", domain, now, map[string]string{strings.Repeat("n", maxFieldName): "v"}), ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(ctx, c.body, domain, now); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}

	// Rule 2 is the whole authorisation model of the write route: the subject is
	// the identity key the handle is bound to. Tampering with a signed body
	// cannot pin it — the signature fails either way — so the case has to be a
	// certificate somebody else really issued about this key, which verifies.
	t.Run("third party issued", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{
			"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339),
		})
		body := certtest.Sign(t, other, c) // Sign replaces the certifier with the signer
		var signed certificates.Certificate
		if err := json.Unmarshal(body, &signed); err != nil {
			t.Fatal(err)
		}
		if signed.Subject.IsEqual(&signed.Certifier) {
			t.Fatal("test body is self-signed; it cannot pin subject == certifier")
		}
		if err := signed.Verify(ctx); err != nil {
			t.Fatalf("test body must be signature-valid or it proves nothing: %v", err)
		}
		if _, err := Parse(ctx, body, domain, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})

	// Over the cap on the wire, small once canonicalised: only the body bound
	// can refuse it, so it fails the moment that bound stops being applied.
	t.Run("oversize", func(t *testing.T) {
		body := append(bytes.Repeat([]byte(" "), MaxBody), certtest.Profile(t, key, "deggen", domain, now, nil)...)
		if len(body) <= MaxBody {
			t.Fatalf("test body = %d bytes, want more than %d", len(body), MaxBody)
		}
		if _, err := Parse(ctx, body, domain, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.Type = wallet.StringBase64(otherType)
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
	t.Run("non-zero revocation outpoint", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.RevocationOutpoint.Index = 1
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
	// A short serial still signs and verifies (the serializer zero-pads it to 32
	// bytes), so only Parse can hold rule 6.
	t.Run("short serialNumber", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.SerialNumber = wallet.StringBase64(base64.StdEncoding.EncodeToString([]byte("a")))
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
	// Go's decoder skips newlines and ignores non-zero trailing bits, so decoding
	// to 32 bytes is not enough: the serial is stored verbatim and compared as a
	// string on update, and both of these decode to the same 32 bytes.
	canon := base64.StdEncoding.EncodeToString(make([]byte, serialBytes))
	for name, serial := range map[string]string{
		"serialNumber with newlines":     strings.Join(strings.Split(canon, ""), "\n"),
		"serialNumber trailing bits set": canon[:len(canon)-2] + "B=",
	} {
		t.Run(name, func(t *testing.T) {
			c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
			c.SerialNumber = wallet.StringBase64(serial)
			if _, err := Parse(ctx, certtest.Sign(t, key, c), domain, now); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

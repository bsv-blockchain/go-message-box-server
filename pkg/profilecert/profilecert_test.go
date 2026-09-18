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
	again, err := Parse(context.Background(), []byte(p.JSON), domain)
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
	body := wireJSON(t, certtest.Profile(t, key, "deggen", domain, time.Now(), extra))
	if len(body) > MaxBody {
		t.Fatalf("test body = %d bytes, want <= %d", len(body), MaxBody)
	}

	p, err := Parse(context.Background(), body, domain)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.JSON) > MaxBody {
		t.Errorf("canonical JSON = %d bytes from a %d byte body, want <= %d", len(p.JSON), len(body), MaxBody)
	}
	if !strings.Contains(p.JSON, strings.Repeat("<", maxFieldValue)) {
		t.Error("canonical JSON escaped the field values")
	}
	if _, err := Parse(context.Background(), []byte(p.JSON), domain); err != nil {
		t.Errorf("re-parse of canonical JSON: %v", err)
	}
}

// Parse is the write-path authority, so it must not fall back to accepting any
// domain when the caller passes none.
func TestParse_NoDomain(t *testing.T) {
	key := newKey(t)
	body := certtest.Profile(t, key, "deggen", domain, time.Now(), nil)
	if _, err := Parse(context.Background(), body, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
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
			if _, err := Parse(ctx, c.body, domain); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}

	t.Run("wrong type", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.Type = wallet.StringBase64(otherType)
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
	// A short serial still signs and verifies (the serializer zero-pads it to 32
	// bytes), so only Parse can hold rule 6.
	t.Run("short serialNumber", func(t *testing.T) {
		c := certtest.Build(t, key, map[string]string{"paymail": "deggen@" + domain, "issuedAt": now.UTC().Format(time.RFC3339)})
		c.SerialNumber = wallet.StringBase64(base64.StdEncoding.EncodeToString([]byte("a")))
		if _, err := Parse(ctx, certtest.Sign(t, key, c), domain); !errors.Is(err, ErrInvalid) {
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
			if _, err := Parse(ctx, certtest.Sign(t, key, c), domain); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

// Package profilecert checks that a certificate is a well-formed public
// profile: self-signed, of the profile type, naming a paymail on this server's
// domain. It knows nothing about which handles are registered.
package profilecert

import (
	"bytes"
	"context"
	"encoding/base64"
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
	maxFieldName  = 50 // exclusive: go-sdk's CertificateFieldNameUnder50Bytes, unenforced there
	maxFieldValue = 1024
	serialBytes   = 32
	zeroTxid      = "0000000000000000000000000000000000000000000000000000000000000000"
)

var (
	// ErrInvalid is returned when the body is not a usable profile certificate.
	ErrInvalid = errors.New("invalid profile certificate")
	// ErrWrongDomain is returned when the paymail names another server.
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
	// Fail closed: without a domain every paymail would be for another server,
	// and one whose domain part is empty would match.
	if domain == "" {
		return nil, invalid("no domain configured")
	}
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
	// The serializer zero-pads a short serial rather than rejecting it, so the
	// 32-byte rule only holds here. The round trip is part of it: the decoder
	// skips newlines and ignores non-zero trailing bits, and the serial is stored
	// verbatim and compared as a string on update.
	if b, err := base64.StdEncoding.DecodeString(string(c.SerialNumber)); err != nil ||
		len(b) != serialBytes || base64.StdEncoding.EncodeToString(b) != string(c.SerialNumber) {
		return nil, invalid("serialNumber must be %d bytes in canonical base64", serialBytes)
	}
	// Checked before the comparison: an absent (as opposed to null) JSON key
	// never reaches PublicKey.UnmarshalJSON and leaves a zero key, whose nil
	// X/Y are dereferenced by IsEqual, Compressed and Verify alike.
	if c.Subject.X == nil || c.Subject.Y == nil || c.Certifier.X == nil || c.Certifier.Y == nil {
		return nil, invalid("subject and certifier are required")
	}
	if !c.Subject.IsEqual(&c.Certifier) {
		return nil, invalid("subject and certifier differ")
	}
	// Checked before Verify: serializing a certificate dereferences the
	// outpoint, so a nil one must never reach it.
	if c.RevocationOutpoint == nil || c.RevocationOutpoint.Index != 0 || c.RevocationOutpoint.Txid.String() != zeroTxid {
		return nil, invalid("revocationOutpoint must be the zero outpoint")
	}
	if len(c.Fields) > maxFields {
		return nil, invalid("more than %d fields", maxFields)
	}
	for name, v := range c.Fields {
		if len(name) >= maxFieldName {
			return nil, invalid("field name must be under %d bytes", maxFieldName)
		}
		if len(v) > maxFieldValue {
			return nil, invalid("field %s larger than %d bytes", name, maxFieldValue)
		}
	}
	if err := c.Verify(ctx); err != nil {
		return nil, invalid("signature: %v", err)
	}

	paymail := string(c.Fields["paymail"])
	handle, dom, ok := strings.Cut(paymail, "@")
	if !ok || handle == "" || dom == "" || paymail != strings.ToLower(paymail) {
		return nil, invalid("paymail field must be lowercase handle@domain")
	}
	if dom != strings.ToLower(domain) {
		return nil, ErrWrongDomain
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, string(c.Fields["issuedAt"]))
	if err != nil {
		return nil, invalid("issuedAt field must be RFC 3339")
	}

	// Not json.Marshal: it escapes '<', '>' and '&' as six bytes each, and
	// JSON.stringify clients send them literally, so a body under MaxBody could
	// canonicalize to several times MaxBody — a document this very function would
	// then refuse, stored and served by the rest of the feature.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&c); err != nil {
		return nil, invalid("re-marshal: %v", err)
	}
	canonical := bytes.TrimRight(buf.Bytes(), "\n") // Encode appends one
	if len(canonical) > MaxBody {
		return nil, invalid("canonical form larger than %d bytes", MaxBody)
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
